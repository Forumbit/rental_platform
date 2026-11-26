package model

import "time"

type User struct {
	ID              int64      `json:"id"`
	Email           string     `json:"email"`
	PasswordHash    string     `json:"-"`
	FullName        string     `json:"full_name"`
	Role            string     `json:"role"`
	EmailVerified   bool       `json:"email_verified"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}
