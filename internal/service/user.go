package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/forumbit/rental_platform/internal/auth"
	"github.com/forumbit/rental_platform/internal/model"
)

type UserRepository interface {
	GetByEmail(ctx context.Context, email string) (*model.User, error)
	CreateIfNotExists(ctx context.Context, email, passwordHash, fullName string) (*model.User, bool, error)
}

type UserService struct {
	repository UserRepository
}

func NewUserService(repository UserRepository) *UserService {
	return &UserService{repository: repository}
}

// EnsureUser is used by cmd/seed-user; an existing account is never modified.
func (s *UserService) EnsureUser(ctx context.Context, email, password, fullName string) (*model.User, bool, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, false, err
	}
	if len(password) < 8 || len(password) > 72 {
		return nil, false, &ValidationError{"password", "must contain between 8 and 72 bytes"}
	}
	fullName = strings.TrimSpace(fullName)
	if fullName == "" || utf8.RuneCountInString(fullName) > 150 || strings.ContainsRune(fullName, '\x00') {
		return nil, false, &ValidationError{"full_name", "must contain between 1 and 150 characters without null characters"}
	}
	user, err := s.repository.GetByEmail(ctx, email)
	if err == nil {
		return user, false, nil
	}
	if !errors.Is(err, model.ErrUserNotFound) {
		return nil, false, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, false, fmt.Errorf("hash password: %w", err)
	}
	return s.repository.CreateIfNotExists(ctx, email, hash, fullName)
}

func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || utf8.RuneCountInString(email) > 255 {
		return "", &ValidationError{"email", "must be a valid email address"}
	}
	return email, nil
}
