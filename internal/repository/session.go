package repository

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"

	"github.com/forumbit/rental_platform/internal/model"
)

type SessionRepository struct {
	db *sql.DB
}

func NewSessionRepository(db *sql.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

func (r *SessionRepository) Create(ctx context.Context, session model.RefreshSession) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO refresh_sessions (id, user_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)`,
		session.ID, session.UserID, session.TokenHash, session.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("create refresh session: %w", err)
	}
	return nil
}

// Rotate serializes refresh requests for one session. Reusing an old signed
// token revokes the session, including the pair issued by a competing request.
func (r *SessionRepository) Rotate(ctx context.Context, previousHash []byte, next model.RefreshSession) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin refresh rotation: %w", err)
	}
	defer tx.Rollback()

	var storedHash []byte
	var active bool
	err = tx.QueryRowContext(ctx, `
		SELECT token_hash, revoked_at IS NULL AND expires_at > NOW()
			AND EXISTS (SELECT 1 FROM users WHERE users.id = refresh_sessions.user_id AND email_verified_at IS NOT NULL)
		FROM refresh_sessions WHERE id = $1 AND user_id = $2 FOR UPDATE`,
		next.ID, next.UserID,
	).Scan(&storedHash, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ErrInvalidToken
	}
	if err != nil {
		return fmt.Errorf("lock refresh session: %w", err)
	}
	if !active {
		return model.ErrInvalidToken
	}
	if subtle.ConstantTimeCompare(storedHash, previousHash) != 1 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE refresh_sessions SET revoked_at = NOW(), updated_at = NOW() WHERE id = $1`, next.ID,
		); err != nil {
			return fmt.Errorf("revoke reused refresh session: %w", err)
		}
		// Commit revocation before returning the authentication error.
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit refresh revocation: %w", err)
		}
		return model.ErrInvalidToken
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE refresh_sessions SET token_hash = $1, expires_at = $2, updated_at = NOW()
		WHERE id = $3`, next.TokenHash, next.ExpiresAt, next.ID,
	); err != nil {
		return fmt.Errorf("rotate refresh session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit refresh rotation: %w", err)
	}
	return nil
}

func (r *SessionRepository) Revoke(ctx context.Context, sessionID string, userID int64) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE refresh_sessions SET revoked_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, sessionID, userID,
	)
	if err != nil {
		return fmt.Errorf("revoke refresh session: %w", err)
	}
	return nil
}

func (r *SessionRepository) GetUser(ctx context.Context, sessionID string, userID int64) (*model.User, error) {
	var user model.User
	err := r.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.full_name, u.role, u.created_at, u.updated_at,
			u.email_verified_at IS NOT NULL, u.email_verified_at
		FROM users u JOIN refresh_sessions s ON s.user_id = u.id
		WHERE s.id = $1 AND s.user_id = $2 AND s.revoked_at IS NULL AND s.expires_at > NOW()
			AND u.email_verified_at IS NOT NULL`,
		sessionID, userID,
	).Scan(&user.ID, &user.Email, &user.FullName, &user.Role, &user.CreatedAt, &user.UpdatedAt,
		&user.EmailVerified, &user.EmailVerifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrInvalidToken
	}
	if err != nil {
		return nil, fmt.Errorf("get session user: %w", err)
	}
	return &user, nil
}
