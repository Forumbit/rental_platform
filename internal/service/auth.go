package service

import (
	"context"
	"errors"

	"github.com/forumbit/rental_platform/internal/auth"
	"github.com/forumbit/rental_platform/internal/model"
)

type LoginUserRepository interface {
	GetByEmail(ctx context.Context, email string) (*model.User, error)
}

type SessionRepository interface {
	Create(ctx context.Context, session model.RefreshSession) error
	Rotate(ctx context.Context, previousHash []byte, next model.RefreshSession) error
	Revoke(ctx context.Context, sessionID string, userID int64) error
	GetUser(ctx context.Context, sessionID string, userID int64) (*model.User, error)
}

type AuthService struct {
	users    LoginUserRepository
	sessions SessionRepository
	tokens   *auth.TokenManager
}

func NewAuthService(users LoginUserRepository, sessions SessionRepository, tokens *auth.TokenManager) *AuthService {
	return &AuthService{users: users, sessions: sessions, tokens: tokens}
}

func (s *AuthService) Login(ctx context.Context, input model.LoginInput) (*model.TokenPair, error) {
	email, err := normalizeEmail(input.Email)
	if err != nil {
		return nil, err
	}
	if input.Password == "" || len(input.Password) > 72 {
		return nil, &ValidationError{"password", "must contain between 1 and 72 bytes"}
	}
	user, err := s.users.GetByEmail(ctx, email)
	if errors.Is(err, model.ErrUserNotFound) {
		auth.CheckPassword("", input.Password)
		return nil, model.ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if !auth.CheckPassword(user.PasswordHash, input.Password) {
		return nil, model.ErrInvalidCredentials
	}
	if !user.EmailVerified {
		return nil, model.ErrEmailNotVerified
	}
	sessionID, err := auth.NewSessionID()
	if err != nil {
		return nil, err
	}
	pair, err := s.tokens.Issue(user.ID, sessionID)
	if err != nil {
		return nil, err
	}
	if err := s.sessions.Create(ctx, sessionForPair(sessionID, user.ID, pair)); err != nil {
		return nil, err
	}
	return pair, nil
}

func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (*model.TokenPair, error) {
	if refreshToken == "" {
		return nil, &ValidationError{"refresh_token", "is required"}
	}
	identity, err := s.tokens.ParseRefresh(refreshToken)
	if err != nil {
		return nil, err
	}
	pair, err := s.tokens.Issue(identity.UserID, identity.SessionID)
	if err != nil {
		return nil, err
	}
	if err := s.sessions.Rotate(ctx, auth.HashRefreshToken(refreshToken),
		sessionForPair(identity.SessionID, identity.UserID, pair)); err != nil {
		return nil, err
	}
	return pair, nil
}

func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return &ValidationError{"refresh_token", "is required"}
	}
	identity, err := s.tokens.ParseRefresh(refreshToken)
	if err != nil {
		return err
	}
	return s.sessions.Revoke(ctx, identity.SessionID, identity.UserID)
}

func (s *AuthService) Authenticate(ctx context.Context, accessToken string) (*model.User, error) {
	identity, err := s.tokens.ParseAccess(accessToken)
	if err != nil {
		return nil, err
	}
	// Session lookup also invalidates access tokens immediately after logout/reuse.
	return s.sessions.GetUser(ctx, identity.SessionID, identity.UserID)
}

func sessionForPair(id string, userID int64, pair *model.TokenPair) model.RefreshSession {
	return model.RefreshSession{
		ID: id, UserID: userID, TokenHash: auth.HashRefreshToken(pair.RefreshToken), ExpiresAt: pair.RefreshExpiresAt,
	}
}
