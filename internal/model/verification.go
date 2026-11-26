package model

import (
	"errors"
	"time"
)

var (
	ErrEmailTaken          = errors.New("email is already registered")
	ErrEmailNotVerified    = errors.New("email is not verified")
	ErrInvalidVerification = errors.New("invalid or expired verification")
	ErrMailUnavailable     = errors.New("email delivery is unavailable")
)

type RegisterInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	FullName string `json:"full_name"`
}

type EmailInput struct {
	Email string `json:"email"`
}

// VerifyEmailInput accepts either a token, or an email and a six-digit code.
type VerifyEmailInput struct {
	Email string `json:"email,omitempty"`
	Code  string `json:"code,omitempty"`
	Token string `json:"token,omitempty"`
}

type MessageResponse struct {
	Message string `json:"message"`
}

type RegistrationResponse struct {
	Email   string `json:"email"`
	Message string `json:"message"`
}

type EmailVerification struct {
	UserID    int64
	ID        string
	TokenHash []byte
	CodeHash  []byte
	ExpiresAt time.Time
}

type VerificationThrottledError struct {
	RetryAfter time.Duration
}

func (e *VerificationThrottledError) Error() string {
	return "wait before requesting another verification email"
}

// VerificationEmail is used only for delivery, never as an API response.
type VerificationEmail struct {
	To           string
	FullName     string
	Code         string
	URL          string
	ExpiresAt    time.Time
	ValidMinutes int
}
