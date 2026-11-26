package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/forumbit/rental_platform/internal/model"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
)

const listingColumns = `id, owner_id, title, description, property_type,
	price_minor, currency, price_period, country, region, city, address,
	area_sqm, bedrooms, bathrooms, max_guests, status, published_at,
	created_at, updated_at`

type ListingRepository struct {
	db *sql.DB
}

func NewListingRepository(db *sql.DB) *ListingRepository {
	return &ListingRepository{db: db}
}

// List returns listings matching the filter, newest first. Conditions are
// added only when set, so the planner can use the partial and composite indexes.
func (r *ListingRepository) List(ctx context.Context, filter model.ListingFilter) ([]model.Listing, error) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 4)
	addCondition := func(sql string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(sql, len(args)))
	}
	if filter.OwnerID > 0 {
		addCondition("owner_id = $%d", filter.OwnerID)
	}
	if filter.Status != "" {
		addCondition("status = $%d", filter.Status)
	}
	if filter.Cursor > 0 {
		addCondition("id < $%d", filter.Cursor)
	}
	query := `SELECT ` + listingColumns + ` FROM listings`
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, " AND ")
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list listings: %w", err)
	}
	defer rows.Close()

	listings := make([]model.Listing, 0)
	for rows.Next() {
		listing, err := scanListing(rows)
		if err != nil {
			return nil, fmt.Errorf("scan listing: %w", err)
		}
		listings = append(listings, *listing)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate listings: %w", err)
	}

	return listings, nil
}

func (r *ListingRepository) GetByID(ctx context.Context, id int64) (*model.Listing, error) {
	listing, err := scanListing(r.db.QueryRowContext(ctx,
		`SELECT `+listingColumns+` FROM listings WHERE id = $1`, id,
	))
	if err != nil {
		return nil, fmt.Errorf("get listing %d: %w", id, mapListingError(err))
	}
	return listing, nil
}

// Create sets published_at to the current time when the listing is published.
func (r *ListingRepository) Create(ctx context.Context, ownerID int64, input model.ListingInput) (*model.Listing, error) {
	listing, err := scanListing(r.db.QueryRowContext(ctx, `
		INSERT INTO listings (
			owner_id, title, description, property_type, price_minor, currency,
			price_period, country, region, city, address, area_sqm, bedrooms,
			bathrooms, max_guests, status, published_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
			CASE WHEN $17 THEN NOW() END
		) RETURNING `+listingColumns,
		append(listingArgs(ownerID, input), input.Status == model.ListingStatusPublished)...,
	))
	if err != nil {
		return nil, fmt.Errorf("create listing: %w", mapListingError(err))
	}
	return listing, nil
}

// Update keeps published_at while the listing stays published, sets it when
// the listing becomes published and clears it otherwise.
func (r *ListingRepository) Update(ctx context.Context, id, ownerID int64, input model.ListingInput) (*model.Listing, error) {
	args := append(listingArgs(ownerID, input), id, input.Status == model.ListingStatusPublished)
	listing, err := scanListing(r.db.QueryRowContext(ctx, `
		UPDATE listings SET
			title = $2, description = $3, property_type = $4,
			price_minor = $5, currency = $6, price_period = $7, country = $8,
			region = $9, city = $10, address = $11, area_sqm = $12,
			bedrooms = $13, bathrooms = $14, max_guests = $15, status = $16,
			published_at = CASE WHEN $18
				THEN CASE WHEN status = 'published' THEN published_at ELSE NOW() END
			END,
			updated_at = NOW()
		WHERE id = $17 AND owner_id = $1
		RETURNING `+listingColumns,
		args...,
	))
	if err != nil {
		return nil, fmt.Errorf("update listing %d: %w", id, mapListingError(err))
	}
	return listing, nil
}

func (r *ListingRepository) Delete(ctx context.Context, id, ownerID int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM listings WHERE id = $1 AND owner_id = $2`, id, ownerID)
	if err != nil {
		return fmt.Errorf("delete listing %d: %w", id, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete listing %d: %w", id, err)
	}
	if count == 0 {
		return model.ErrListingNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanListing(row rowScanner) (*model.Listing, error) {
	var listing model.Listing
	if err := row.Scan(
		&listing.ID, &listing.OwnerID, &listing.Title, &listing.Description,
		&listing.PropertyType, &listing.PriceMinor, &listing.Currency,
		&listing.PricePeriod, &listing.Country, &listing.Region, &listing.City,
		&listing.Address, &listing.AreaSqm, &listing.Bedrooms, &listing.Bathrooms,
		&listing.MaxGuests, &listing.Status, &listing.PublishedAt,
		&listing.CreatedAt, &listing.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &listing, nil
}

func listingArgs(ownerID int64, input model.ListingInput) []any {
	return []any{
		ownerID, input.Title, input.Description, input.PropertyType,
		input.PriceMinor, input.Currency, input.PricePeriod, input.Country,
		input.Region, input.City, input.Address, input.AreaSqm, input.Bedrooms,
		input.Bathrooms, input.MaxGuests, input.Status,
	}
}

func mapListingError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return model.ErrListingNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation &&
		pgErr.ConstraintName == "listings_owner_id_fkey" {
		return model.ErrOwnerNotFound
	}
	return err
}
