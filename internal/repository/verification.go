package repository

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/forumbit/rental_platform/internal/model"
)

type VerificationRepository struct {
	db *sql.DB
}

func NewVerificationRepository(db *sql.DB) *VerificationRepository {
	return &VerificationRepository{db: db}
}

// Replace locks the user first, the same lock order used by Consume.
func (r *VerificationRepository) Replace(ctx context.Context, verification model.EmailVerification, cooldown time.Duration) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin verification replacement: %w", err)
	}
	defer tx.Rollback()
	var verified bool
	err = tx.QueryRowContext(ctx, `SELECT email_verified_at IS NOT NULL FROM users WHERE id = $1 FOR UPDATE`, verification.UserID).Scan(&verified)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && verified) {
		return model.ErrInvalidVerification
	}
	if err != nil {
		return fmt.Errorf("lock verification user: %w", err)
	}
	var remaining float64
	err = tx.QueryRowContext(ctx, `
		SELECT EXTRACT(EPOCH FROM (sent_at + $2 * INTERVAL '1 second' - NOW()))
		FROM email_verifications WHERE user_id = $1`, verification.UserID, cooldown.Seconds(),
	).Scan(&remaining)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check verification cooldown: %w", err)
	}
	if err == nil && remaining > 0 {
		return &model.VerificationThrottledError{RetryAfter: time.Duration(remaining * float64(time.Second))}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO email_verifications (user_id, id, token_hash, code_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id) DO UPDATE SET id = EXCLUDED.id, token_hash = EXCLUDED.token_hash,
		code_hash = EXCLUDED.code_hash, expires_at = EXCLUDED.expires_at, attempts = 0, sent_at = NOW()`,
		verification.UserID, verification.ID, verification.TokenHash, verification.CodeHash, verification.ExpiresAt,
	); err != nil {
		return fmt.Errorf("replace email verification: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit verification replacement: %w", err)
	}
	return nil
}

func (r *VerificationRepository) GetByEmail(ctx context.Context, email string) (*model.EmailVerification, error) {
	return r.get(ctx, `SELECT v.user_id, v.id, v.token_hash, v.code_hash, v.expires_at
		FROM email_verifications v JOIN users u ON u.id = v.user_id
		WHERE LOWER(u.email) = LOWER($1) AND u.email_verified_at IS NULL`, email)
}

func (r *VerificationRepository) GetByTokenHash(ctx context.Context, hash []byte) (*model.EmailVerification, error) {
	return r.get(ctx, `SELECT user_id, id, token_hash, code_hash, expires_at
		FROM email_verifications WHERE token_hash = $1`, hash)
}

func (r *VerificationRepository) get(ctx context.Context, query string, argument any) (*model.EmailVerification, error) {
	var verification model.EmailVerification
	err := r.db.QueryRowContext(ctx, query, argument).Scan(&verification.UserID, &verification.ID,
		&verification.TokenHash, &verification.CodeHash, &verification.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrInvalidVerification
	}
	if err != nil {
		return nil, fmt.Errorf("get email verification: %w", err)
	}
	return &verification, nil
}

// Consume atomically verifies the address and deletes both verification methods.
// Incorrect code attempts are committed even though an error is returned.
func (r *VerificationRepository) Consume(ctx context.Context, userID int64, id string, hash []byte, byCode bool, maxAttempts int) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin email verification: %w", err)
	}
	defer tx.Rollback()
	var verified bool
	err = tx.QueryRowContext(ctx, `SELECT email_verified_at IS NOT NULL FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&verified)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && verified) {
		return model.ErrInvalidVerification
	}
	if err != nil {
		return fmt.Errorf("lock email verification user: %w", err)
	}
	var expected []byte
	var attempts int
	var active bool
	column := "token_hash"
	if byCode {
		column = "code_hash"
	}
	err = tx.QueryRowContext(ctx, `SELECT `+column+`, attempts, expires_at > NOW()
		FROM email_verifications WHERE user_id = $1 AND id = $2 FOR UPDATE`, userID, id,
	).Scan(&expected, &attempts, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ErrInvalidVerification
	}
	if err != nil {
		return fmt.Errorf("lock email verification: %w", err)
	}
	if !active || attempts >= maxAttempts {
		return model.ErrInvalidVerification
	}
	if subtle.ConstantTimeCompare(expected, hash) != 1 {
		if byCode {
			if _, err := tx.ExecContext(ctx, `UPDATE email_verifications SET attempts = attempts + 1 WHERE user_id = $1`, userID); err != nil {
				return fmt.Errorf("record verification attempt: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit verification attempt: %w", err)
			}
		}
		return model.ErrInvalidVerification
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET email_verified_at = NOW(), updated_at = NOW() WHERE id = $1`, userID); err != nil {
		return fmt.Errorf("verify user email: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM email_verifications WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("consume email verification: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit email verification: %w", err)
	}
	return nil
}

func (r *VerificationRepository) DeliveryFailed(ctx context.Context, userID int64, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM email_verifications WHERE user_id = $1 AND id = $2`, userID, id)
	if err != nil {
		return fmt.Errorf("invalidate undelivered verification: %w", err)
	}
	return nil
}
