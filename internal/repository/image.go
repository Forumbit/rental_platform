package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/forumbit/rental_platform/internal/model"
)

const imageColumns = `id, listing_id, storage_key, content_type, size_bytes, width, height, position, created_at`

type ImageRepository struct {
	db *sql.DB
}

func NewImageRepository(db *sql.DB) *ImageRepository {
	return &ImageRepository{db: db}
}

func (r *ImageRepository) Count(ctx context.Context, listingID int64) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM listing_images WHERE listing_id = $1`, listingID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count listing images: %w", err)
	}
	return count, nil
}

// Create appends the image to the owner's listing. The listing row is locked,
// so concurrent uploads cannot exceed maxPerListing.
func (r *ImageRepository) Create(ctx context.Context, ownerID int64, image model.ListingImage, maxPerListing int) (*model.ListingImage, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin image creation: %w", err)
	}
	defer tx.Rollback()

	var locked int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM listings WHERE id = $1 AND owner_id = $2 FOR UPDATE`,
		image.ListingID, ownerID).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrListingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock listing for image: %w", err)
	}
	var count, nextPosition int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(position) + 1, 0)
		FROM listing_images WHERE listing_id = $1`, image.ListingID).Scan(&count, &nextPosition)
	if err != nil {
		return nil, fmt.Errorf("count listing images: %w", err)
	}
	if count >= maxPerListing {
		return nil, &model.ImageLimitError{Max: maxPerListing}
	}
	created, err := scanImage(tx.QueryRowContext(ctx, `
		INSERT INTO listing_images (listing_id, storage_key, content_type, size_bytes, width, height, position)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+imageColumns,
		image.ListingID, image.StorageKey, image.ContentType, image.SizeBytes, image.Width, image.Height, nextPosition,
	))
	if err != nil {
		return nil, fmt.Errorf("create listing image: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit listing image: %w", err)
	}
	return created, nil
}

// Reorder sets positions 0..n-1 in the order of imageIDs, which must contain
// every image of the owner's listing exactly once. The listing row is locked,
// the same lock Create takes, so an upload cannot slip in between.
func (r *ImageRepository) Reorder(ctx context.Context, listingID, ownerID int64, imageIDs []int64) ([]model.ListingImage, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin image reorder: %w", err)
	}
	defer tx.Rollback()

	var locked int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM listings WHERE id = $1 AND owner_id = $2 FOR UPDATE`,
		listingID, ownerID).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrListingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock listing for reorder: %w", err)
	}
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM listing_images WHERE listing_id = $1`,
		listingID).Scan(&total); err != nil {
		return nil, fmt.Errorf("count listing images: %w", err)
	}
	// The caller guarantees unique IDs, so matching counts mean equal sets.
	result, err := tx.ExecContext(ctx, `
		UPDATE listing_images i SET position = o.ordinality - 1
		FROM UNNEST($2::bigint[]) WITH ORDINALITY AS o(id, ordinality)
		WHERE i.id = o.id AND i.listing_id = $1`, listingID, imageIDs)
	if err != nil {
		return nil, fmt.Errorf("reorder listing images: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reorder listing images: %w", err)
	}
	if int(updated) != len(imageIDs) || total != len(imageIDs) {
		return nil, model.ErrImageOrderMismatch
	}

	rows, err := tx.QueryContext(ctx, `SELECT `+imageColumns+` FROM listing_images
		WHERE listing_id = $1 ORDER BY position, id`, listingID)
	if err != nil {
		return nil, fmt.Errorf("list reordered images: %w", err)
	}
	images := make([]model.ListingImage, 0, len(imageIDs))
	for rows.Next() {
		image, err := scanImage(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan listing image: %w", err)
		}
		images = append(images, *image)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate listing images: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit image reorder: %w", err)
	}
	return images, nil
}

// ListByListings returns images grouped by listing ID, each group ordered by position.
func (r *ImageRepository) ListByListings(ctx context.Context, listingIDs []int64) (map[int64][]model.ListingImage, error) {
	result := make(map[int64][]model.ListingImage, len(listingIDs))
	if len(listingIDs) == 0 {
		return result, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+imageColumns+` FROM listing_images
		WHERE listing_id = ANY($1) ORDER BY listing_id, position, id`, listingIDs)
	if err != nil {
		return nil, fmt.Errorf("list listing images: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		image, err := scanImage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan listing image: %w", err)
		}
		result[image.ListingID] = append(result[image.ListingID], *image)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate listing images: %w", err)
	}
	return result, nil
}

// Delete removes the image of the owner's listing and returns its storage key.
func (r *ImageRepository) Delete(ctx context.Context, listingID, imageID, ownerID int64) (string, error) {
	var key string
	err := r.db.QueryRowContext(ctx, `
		DELETE FROM listing_images i USING listings l
		WHERE i.id = $1 AND i.listing_id = $2 AND l.id = i.listing_id AND l.owner_id = $3
		RETURNING i.storage_key`, imageID, listingID, ownerID,
	).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", model.ErrImageNotFound
	}
	if err != nil {
		return "", fmt.Errorf("delete listing image: %w", err)
	}
	return key, nil
}

func (r *ImageRepository) StorageKeys(ctx context.Context, listingID int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT storage_key FROM listing_images WHERE listing_id = $1`, listingID)
	if err != nil {
		return nil, fmt.Errorf("list image keys: %w", err)
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan image key: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate image keys: %w", err)
	}
	return keys, nil
}

func scanImage(row rowScanner) (*model.ListingImage, error) {
	var image model.ListingImage
	if err := row.Scan(&image.ID, &image.ListingID, &image.StorageKey, &image.ContentType,
		&image.SizeBytes, &image.Width, &image.Height, &image.Position, &image.CreatedAt); err != nil {
		return nil, err
	}
	return &image, nil
}
