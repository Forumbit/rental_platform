package model

import (
	"errors"
	"time"
)

var (
	ErrListingNotFound = errors.New("listing not found")
	ErrOwnerNotFound   = errors.New("owner not found")
)

const ListingStatusPublished = "published"

// ListingInput contains the fields accepted by POST /listings and PUT /listings/{id}.
// The owner and publication time are managed by the server.
// Pointer fields preserve the distinction between SQL NULL and zero values.
type ListingInput struct {
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	PropertyType string   `json:"property_type"`
	PriceMinor   int64    `json:"price_minor"`
	Currency     string   `json:"currency"`
	PricePeriod  string   `json:"price_period"`
	Country      string   `json:"country"`
	Region       *string  `json:"region"`
	City         string   `json:"city"`
	Address      *string  `json:"address"`
	AreaSqm      *float64 `json:"area_sqm"`
	Bedrooms     *int     `json:"bedrooms"`
	Bathrooms    *int     `json:"bathrooms"`
	MaxGuests    *int     `json:"max_guests"`
	Status       string   `json:"status"`
}

type Listing struct {
	ID      int64 `json:"id"`
	OwnerID int64 `json:"owner_id"`
	ListingInput
	// PublishedAt is set when the listing enters the published status and
	// cleared when it leaves it.
	PublishedAt *time.Time `json:"published_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// ListingFilter selects listings ordered by id DESC. Zero values mean "any".
type ListingFilter struct {
	OwnerID int64
	Status  string
	// Cursor returns only listings with id < Cursor.
	Cursor int64
	Limit  int
}

type ListingPage struct {
	Items      []Listing `json:"items"`
	NextCursor *int64    `json:"next_cursor"`
}
