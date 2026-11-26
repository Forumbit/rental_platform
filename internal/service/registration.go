package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/forumbit/rental_platform/internal/auth"
	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/internal/model"
)

type RegistrationUserRepository interface {
	GetByEmail(ctx context.Context, email string) (*model.User, error)
	CreateUnverified(ctx context.Context, email, passwordHash, fullName string) (*model.User, error)
}

type VerificationRepository interface {
	Replace(ctx context.Context, verification model.EmailVerification, cooldown time.Duration) error
	GetByEmail(ctx context.Context, email string) (*model.EmailVerification, error)
	GetByTokenHash(ctx context.Context, hash []byte) (*model.EmailVerification, error)
	Consume(ctx context.Context, userID int64, id string, hash []byte, byCode bool, maxAttempts int) error
	DeliveryFailed(ctx context.Context, userID int64, id string) error
}

type VerificationMailer interface {
	Available() bool
	SendVerification(ctx context.Context, email model.VerificationEmail) error
}

type RegistrationService struct {
	users         RegistrationUserRepository
	verifications VerificationRepository
	mailer        VerificationMailer
	manager       *auth.VerificationManager
	cfg           config.VerificationConfig
	baseURL       *url.URL
}

func NewRegistrationService(users RegistrationUserRepository, verifications VerificationRepository, mailer VerificationMailer, cfg config.VerificationConfig) (*RegistrationService, error) {
	manager, err := auth.NewVerificationManager(cfg.Secret, cfg.TTL)
	if err != nil {
		return nil, err
	}
	baseURL, err := url.Parse(cfg.PublicBaseURL)
	if err != nil || baseURL.Host == "" || (baseURL.Scheme != "http" && baseURL.Scheme != "https") ||
		baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, fmt.Errorf("PUBLIC_BASE_URL must be an absolute http(s) URL without credentials, query or fragment")
	}
	if cfg.ResendInterval < time.Second || cfg.MaxAttempts < 1 || cfg.MaxAttempts > 100 {
		return nil, fmt.Errorf("verification resend_interval must be >= 1s and max_attempts between 1 and 100")
	}
	return &RegistrationService{users: users, verifications: verifications, mailer: mailer, manager: manager, cfg: cfg, baseURL: baseURL}, nil
}

func (s *RegistrationService) Register(ctx context.Context, input model.RegisterInput) (*model.RegistrationResponse, error) {
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return nil, err
	}
	if len(input.Password) < 8 || len(input.Password) > 72 {
		return nil, &ValidationError{"password", "must contain between 8 and 72 bytes"}
	}
	fullName := strings.TrimSpace(input.FullName)
	if fullName == "" || utf8.RuneCountInString(fullName) > 150 || strings.ContainsRune(fullName, '\x00') {
		return nil, &ValidationError{"full_name", "must contain between 1 and 150 characters without null characters"}
	}
	if !s.mailer.Available() {
		return nil, model.ErrMailUnavailable
	}
	if _, err := s.users.GetByEmail(ctx, email); err == nil {
		return nil, model.ErrEmailTaken
	} else if !errors.Is(err, model.ErrUserNotFound) {
		return nil, err
	}
	hash, err := auth.HashPassword(input.Password)
	if err != nil {
		return nil, fmt.Errorf("hash registration password: %w", err)
	}
	user, err := s.users.CreateUnverified(ctx, email, hash, fullName)
	if err != nil {
		return nil, err
	}
	if err := s.send(ctx, user); err != nil {
		return nil, err
	}
	return &model.RegistrationResponse{Email: email, Message: "Check your email to verify your account."}, nil
}

func (s *RegistrationService) Resend(ctx context.Context, email string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if !s.mailer.Available() {
		return model.ErrMailUnavailable
	}
	user, err := s.users.GetByEmail(ctx, email)
	if errors.Is(err, model.ErrUserNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if user.EmailVerified {
		return nil
	}
	if err := s.send(ctx, user); errors.Is(err, model.ErrInvalidVerification) {
		// A simultaneous verification may have completed while preparing the email.
		return nil
	} else {
		return err
	}
}

func (s *RegistrationService) Verify(ctx context.Context, input model.VerifyEmailInput) error {
	if input.Token != "" {
		if input.Email != "" || input.Code != "" {
			return &ValidationError{"verification", "must contain either token or email and code"}
		}
		if len(input.Token) != 64 {
			return model.ErrInvalidVerification
		}
		if _, err := hex.DecodeString(input.Token); err != nil {
			return model.ErrInvalidVerification
		}
		hash := auth.HashVerificationToken(input.Token)
		verification, err := s.verifications.GetByTokenHash(ctx, hash)
		if err != nil {
			return err
		}
		return s.verifications.Consume(ctx, verification.UserID, verification.ID, hash, false, s.cfg.MaxAttempts)
	}
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return err
	}
	if len(input.Code) != 6 || strings.IndexFunc(input.Code, func(r rune) bool { return r < '0' || r > '9' }) != -1 {
		return &ValidationError{"code", "must contain exactly six digits"}
	}
	verification, err := s.verifications.GetByEmail(ctx, email)
	if err != nil {
		return err
	}
	hash := s.manager.HashCode(verification.UserID, verification.ID, input.Code)
	return s.verifications.Consume(ctx, verification.UserID, verification.ID, hash, true, s.cfg.MaxAttempts)
}

func (s *RegistrationService) send(ctx context.Context, user *model.User) error {
	verification, code, token, err := s.manager.Generate(user.ID)
	if err != nil {
		return err
	}
	if err := s.verifications.Replace(ctx, verification, s.cfg.ResendInterval); err != nil {
		return err
	}
	link := *s.baseURL
	link.Path = strings.TrimRight(link.Path, "/") + "/auth/verify-email"
	link.Fragment = url.Values{"token": {token}}.Encode()
	if err := s.mailer.SendVerification(ctx, model.VerificationEmail{
		To: user.Email, FullName: user.FullName, Code: code, URL: link.String(),
		ExpiresAt: verification.ExpiresAt, ValidMinutes: int(s.cfg.TTL / time.Minute),
	}); err != nil {
		log.Printf("send verification email: %v", err)
		// The user can retry delivery immediately via resend after an SMTP failure.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := s.verifications.DeliveryFailed(cleanupCtx, user.ID, verification.ID); err != nil {
			log.Printf("clean up undelivered verification: %v", err)
		}
		return model.ErrMailUnavailable
	}
	return nil
}
