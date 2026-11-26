package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/forumbit/rental_platform/internal/model"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
)

const userColumns = `id, email, password_hash, full_name, role, created_at, updated_at,
	email_verified_at IS NOT NULL, email_verified_at`

type UserRepository struct {
	db *sql.DB
}

func NewUserRepository(db *sql.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	user, err := scanUser(r.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE LOWER(email) = LOWER($1)`, email,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return user, nil
}

// CreateIfNotExists never replaces an existing user's password or profile.
func (r *UserRepository) CreateIfNotExists(ctx context.Context, email, passwordHash, fullName string) (*model.User, bool, error) {
	user, err := scanUser(r.db.QueryRowContext(ctx, `
		INSERT INTO users (email, password_hash, full_name, email_verified_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (LOWER(email)) DO NOTHING
		RETURNING `+userColumns, email, passwordHash, fullName,
	))
	if errors.Is(err, sql.ErrNoRows) {
		user, err = r.GetByEmail(ctx, email)
		return user, false, err
	}
	if err != nil {
		return nil, false, fmt.Errorf("create user: %w", err)
	}
	return user, true, nil
}

func (r *UserRepository) CreateUnverified(ctx context.Context, email, passwordHash, fullName string) (*model.User, error) {
	user, err := scanUser(r.db.QueryRowContext(ctx, `
		INSERT INTO users (email, password_hash, full_name)
		VALUES ($1, $2, $3) RETURNING `+userColumns, email, passwordHash, fullName,
	))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation && pgErr.ConstraintName == "users_email_unique_idx" {
		return nil, model.ErrEmailTaken
	}
	if err != nil {
		return nil, fmt.Errorf("create unverified user: %w", err)
	}
	return user, nil
}

func scanUser(row rowScanner) (*model.User, error) {
	var user model.User
	if err := row.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.FullName,
		&user.Role, &user.CreatedAt, &user.UpdatedAt, &user.EmailVerified, &user.EmailVerifiedAt); err != nil {
		return nil, err
	}
	return &user, nil
}
