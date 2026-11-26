package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type CleanupRepository struct {
	db *sql.DB
}

func NewCleanupRepository(db *sql.DB) *CleanupRepository {
	return &CleanupRepository{db: db}
}

// DeleteExpired removes refresh sessions and email verifications that expired
// or were revoked more than retention ago. Such rows can no longer authenticate
// anyone, so deleting them does not change API behavior.
func (r *CleanupRepository) DeleteExpired(ctx context.Context, retention time.Duration) (sessions, verifications int64, err error) {
	seconds := retention.Seconds()
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM refresh_sessions
		WHERE expires_at < NOW() - make_interval(secs => $1)
			OR revoked_at < NOW() - make_interval(secs => $1)`, seconds)
	if err != nil {
		return 0, 0, fmt.Errorf("delete expired refresh sessions: %w", err)
	}
	if sessions, err = result.RowsAffected(); err != nil {
		return 0, 0, fmt.Errorf("count deleted refresh sessions: %w", err)
	}
	result, err = r.db.ExecContext(ctx, `
		DELETE FROM email_verifications
		WHERE expires_at < NOW() - make_interval(secs => $1)`, seconds)
	if err != nil {
		return sessions, 0, fmt.Errorf("delete expired email verifications: %w", err)
	}
	if verifications, err = result.RowsAffected(); err != nil {
		return sessions, 0, fmt.Errorf("count deleted email verifications: %w", err)
	}
	return sessions, verifications, nil
}
