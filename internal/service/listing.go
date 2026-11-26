package service

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/forumbit/rental_platform/internal/model"
)

const (
	DefaultListingsLimit = 20
	MaxListingsLimit     = 100
)

type ListingRepository interface {
	List(ctx context.Context, filter model.ListingFilter) ([]model.Listing, error)
	GetByID(ctx context.Context, id int64) (*model.Listing, error)
	Create(ctx context.Context, ownerID int64, input model.ListingInput) (*model.Listing, error)
	Update(ctx context.Context, id, ownerID int64, input model.ListingInput) (*model.Listing, error)
	Delete(ctx context.Context, id, ownerID int64) error
}

type ListingService struct {
	repository ListingRepository
}

func NewListingService(repository ListingRepository) *ListingService {
	return &ListingService{repository: repository}
}

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + " " + e.Message
}

// ListPublished returns the public feed: only published listings.
func (s *ListingService) ListPublished(ctx context.Context, limit int, cursor int64) (*model.ListingPage, error) {
	return s.list(ctx, model.ListingFilter{Status: model.ListingStatusPublished, Cursor: cursor, Limit: limit})
}

// GetPublished hides drafts and archived listings as if they did not exist.
func (s *ListingService) GetPublished(ctx context.Context, id int64) (*model.Listing, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	listing, err := s.repository.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if listing.Status != model.ListingStatusPublished {
		return nil, model.ErrListingNotFound
	}
	return listing, nil
}

// ListOwn returns the user's listings in any status; status optionally filters them.
func (s *ListingService) ListOwn(ctx context.Context, userID int64, status string, limit int, cursor int64) (*model.ListingPage, error) {
	if userID <= 0 {
		return nil, model.ErrInvalidToken
	}
	if status != "" {
		if err := validateStatus(status); err != nil {
			return nil, err
		}
	}
	return s.list(ctx, model.ListingFilter{OwnerID: userID, Status: status, Cursor: cursor, Limit: limit})
}

// GetOwn returns the user's listing in any status. Other users' listings are
// reported as missing so their drafts are not revealed.
func (s *ListingService) GetOwn(ctx context.Context, userID, id int64) (*model.Listing, error) {
	if userID <= 0 {
		return nil, model.ErrInvalidToken
	}
	if err := validateID(id); err != nil {
		return nil, err
	}
	listing, err := s.repository.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if listing.OwnerID != userID {
		return nil, model.ErrListingNotFound
	}
	return listing, nil
}

func (s *ListingService) Create(ctx context.Context, userID int64, input model.ListingInput) (*model.Listing, error) {
	if userID <= 0 {
		return nil, model.ErrInvalidToken
	}
	input, err := prepareListing(input)
	if err != nil {
		return nil, err
	}
	return s.repository.Create(ctx, userID, input)
}

// Update replaces all writable fields; omitted optional fields become NULL or defaults.
func (s *ListingService) Update(ctx context.Context, userID, id int64, input model.ListingInput) (*model.Listing, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	input, err := prepareListing(input)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwner(ctx, id, userID); err != nil {
		return nil, err
	}
	return s.repository.Update(ctx, id, userID, input)
}

func (s *ListingService) Delete(ctx context.Context, userID, id int64) error {
	if err := validateID(id); err != nil {
		return err
	}
	if err := s.requireOwner(ctx, id, userID); err != nil {
		return err
	}
	return s.repository.Delete(ctx, id, userID)
}

// list fetches one extra row to learn whether another page exists.
func (s *ListingService) list(ctx context.Context, filter model.ListingFilter) (*model.ListingPage, error) {
	if filter.Limit < 1 || filter.Limit > MaxListingsLimit {
		return nil, &ValidationError{"limit", fmt.Sprintf("must be between 1 and %d", MaxListingsLimit)}
	}
	if filter.Cursor < 0 {
		return nil, &ValidationError{"cursor", "must be a positive integer"}
	}
	limit := filter.Limit
	filter.Limit++
	listings, err := s.repository.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	page := &model.ListingPage{Items: listings}
	if len(listings) > limit {
		page.Items = listings[:limit]
		next := page.Items[limit-1].ID
		page.NextCursor = &next
	}
	return page, nil
}

func (s *ListingService) requireOwner(ctx context.Context, id, userID int64) error {
	if userID <= 0 {
		return model.ErrInvalidToken
	}
	listing, err := s.repository.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if listing.OwnerID != userID {
		return model.ErrForbidden
	}
	return nil
}

func validateID(id int64) error {
	if id <= 0 {
		return &ValidationError{"id", "must be a positive integer"}
	}
	return nil
}

func prepareListing(input model.ListingInput) (model.ListingInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	input.PropertyType = strings.TrimSpace(input.PropertyType)
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	input.PricePeriod = strings.TrimSpace(input.PricePeriod)
	input.Country = strings.TrimSpace(input.Country)
	input.City = strings.TrimSpace(input.City)
	input.Status = strings.TrimSpace(input.Status)

	if input.Currency == "" {
		input.Currency = "RUB"
	}
	if input.PricePeriod == "" {
		input.PricePeriod = "day"
	}
	if input.Country == "" {
		input.Country = "Россия"
	}
	if input.Status == "" {
		input.Status = "draft"
	}

	return input, validateListing(input)
}

func validateListing(input model.ListingInput) error {
	for _, field := range []struct {
		name  string
		value string
		max   int
	}{
		{"title", input.Title, 200},
		{"description", input.Description, 0},
		{"country", input.Country, 100},
		{"city", input.City, 150},
	} {
		if field.value == "" {
			return &ValidationError{field.name, "is required"}
		}
		if field.max > 0 && utf8.RuneCountInString(field.value) > field.max {
			return &ValidationError{field.name, fmt.Sprintf("must not exceed %d characters", field.max)}
		}
	}
	if input.Region != nil && utf8.RuneCountInString(*input.Region) > 150 {
		return &ValidationError{"region", "must not exceed 150 characters"}
	}
	// PostgreSQL text values cannot contain the zero byte, even when JSON is valid.
	for _, value := range []string{input.Title, input.Description, input.Country, input.City} {
		if strings.ContainsRune(value, '\x00') {
			return &ValidationError{"text fields", "must not contain null characters"}
		}
	}
	for _, value := range []*string{input.Region, input.Address} {
		if value != nil && strings.ContainsRune(*value, '\x00') {
			return &ValidationError{"text fields", "must not contain null characters"}
		}
	}

	switch input.PropertyType {
	case "house", "cottage", "dacha", "apartment":
	default:
		return &ValidationError{"property_type", "must be house, cottage, dacha or apartment"}
	}
	if input.PriceMinor <= 0 {
		return &ValidationError{"price_minor", "must be positive"}
	}
	if len(input.Currency) != 3 || strings.IndexFunc(input.Currency, func(r rune) bool {
		return r < 'A' || r > 'Z'
	}) != -1 {
		return &ValidationError{"currency", "must be a three-letter currency code"}
	}
	switch input.PricePeriod {
	case "day", "month":
	default:
		return &ValidationError{"price_period", "must be day or month"}
	}
	if input.AreaSqm != nil && (math.IsNaN(*input.AreaSqm) || math.IsInf(*input.AreaSqm, 0) ||
		*input.AreaSqm < 0.01 || *input.AreaSqm > 999999.99) {
		return &ValidationError{"area_sqm", "must be between 0.01 and 999999.99"}
	}
	for _, field := range []struct {
		name  string
		value *int
		min   int
	}{
		{"bedrooms", input.Bedrooms, 0},
		{"bathrooms", input.Bathrooms, 0},
		{"max_guests", input.MaxGuests, 1},
	} {
		if field.value != nil && (*field.value < field.min || *field.value > 32767) {
			return &ValidationError{field.name, fmt.Sprintf("must be between %d and 32767", field.min)}
		}
	}
	return validateStatus(input.Status)
}

func validateStatus(status string) error {
	switch status {
	case "draft", model.ListingStatusPublished, "archived":
		return nil
	default:
		return &ValidationError{"status", "must be draft, published or archived"}
	}
}
