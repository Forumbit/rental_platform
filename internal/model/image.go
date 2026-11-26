package model

import (
	"errors"
	"fmt"
	"io"
	"time"
)

var (
	ErrImageNotFound    = errors.New("image not found")
	ErrImageTooLarge    = errors.New("image is too large")
	ErrUnsupportedImage = errors.New("unsupported image format")
	ErrInvalidImage     = errors.New("invalid image")
	// ErrImageOrderMismatch means the new order does not list exactly the listing's images.
	ErrImageOrderMismatch = errors.New("image order must list every image of the listing exactly once")
)

// ImageOrderInput is the body of PUT /listings/{id}/images/order.
type ImageOrderInput struct {
	ImageIDs []int64 `json:"image_ids"`
}

// ImageLimitError is returned when a listing already has the maximum number of images.
type ImageLimitError struct {
	Max int
}

func (e *ImageLimitError) Error() string {
	return fmt.Sprintf("a listing can have at most %d images", e.Max)
}

// ListingImage describes an uploaded photo. The file itself lives in object
// storage under StorageKey and is served at URL.
type ListingImage struct {
	ID          int64     `json:"id"`
	ListingID   int64     `json:"-"`
	StorageKey  string    `json:"-"`
	URL         string    `json:"url"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	Position    int       `json:"position"`
	CreatedAt   time.Time `json:"created_at"`
}

// StoredObject is an object read from image storage. The caller closes Body.
type StoredObject struct {
	Body        io.ReadCloser
	ContentType string
	Size        int64
}
