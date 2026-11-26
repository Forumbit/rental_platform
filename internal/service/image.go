package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register decoders for image.DecodeConfig
	_ "image/png"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/internal/model"
	_ "golang.org/x/image/webp"
)

type ImageRepository interface {
	Count(ctx context.Context, listingID int64) (int, error)
	Create(ctx context.Context, ownerID int64, image model.ListingImage, maxPerListing int) (*model.ListingImage, error)
	ListByListings(ctx context.Context, listingIDs []int64) (map[int64][]model.ListingImage, error)
	Delete(ctx context.Context, listingID, imageID, ownerID int64) (string, error)
	Reorder(ctx context.Context, listingID, ownerID int64, imageIDs []int64) ([]model.ListingImage, error)
	StorageKeys(ctx context.Context, listingID int64) ([]string, error)
}

type ImageStorage interface {
	Put(ctx context.Context, key, contentType string, data []byte) error
	Get(ctx context.Context, key string) (*model.StoredObject, error)
	Delete(ctx context.Context, key string) error
}

// imageFormats lists accepted types, keyed by the type sniffed from the file content.
var imageFormats = map[string]struct{ decoder, ext string }{
	"image/jpeg": {"jpeg", "jpg"},
	"image/png":  {"png", "png"},
	"image/webp": {"webp", "webp"},
}

var contentTypeByExt = map[string]string{"jpg": "image/jpeg", "png": "image/png", "webp": "image/webp"}

var storageKeyPattern = regexp.MustCompile(`^[0-9a-f]{32}\.(jpg|png|webp)$`)

const imageURLPrefix = "/images/"

type ImageService struct {
	listings ListingRepository
	images   ImageRepository
	storage  ImageStorage
	cfg      config.ImagesConfig
}

func NewImageService(listings ListingRepository, images ImageRepository, storage ImageStorage, cfg config.ImagesConfig) (*ImageService, error) {
	if cfg.MaxSizeBytes < 1024 || cfg.MaxSizeBytes > 50<<20 {
		return nil, fmt.Errorf("images.max_size_bytes must be between 1 KiB and 50 MiB")
	}
	if cfg.MaxPerListing < 1 || cfg.MaxPerListing > 100 {
		return nil, fmt.Errorf("images.max_per_listing must be between 1 and 100")
	}
	if cfg.MaxDimension < 1 || cfg.MaxDimension > 20000 {
		return nil, fmt.Errorf("images.max_dimension must be between 1 and 20000")
	}
	return &ImageService{listings: listings, images: images, storage: storage, cfg: cfg}, nil
}

func (s *ImageService) MaxSizeBytes() int64 {
	return s.cfg.MaxSizeBytes
}

// Upload validates the file, stores it and appends it to the owner's listing.
func (s *ImageService) Upload(ctx context.Context, userID, listingID int64, data []byte) (*model.ListingImage, error) {
	if err := validateID(listingID); err != nil {
		return nil, err
	}
	if int64(len(data)) > s.cfg.MaxSizeBytes {
		return nil, model.ErrImageTooLarge
	}
	image, err := s.inspect(data)
	if err != nil {
		return nil, err
	}
	if err := requireOwner(ctx, s.listings, listingID, userID); err != nil {
		return nil, err
	}
	// Cheap pre-check before uploading; Create re-checks under a lock.
	count, err := s.images.Count(ctx, listingID)
	if err != nil {
		return nil, err
	}
	if count >= s.cfg.MaxPerListing {
		return nil, &model.ImageLimitError{Max: s.cfg.MaxPerListing}
	}

	id, err := randomHex()
	if err != nil {
		return nil, err
	}
	image.ListingID = listingID
	image.StorageKey = id + "." + imageFormats[image.ContentType].ext
	if err := s.storage.Put(ctx, image.StorageKey, image.ContentType, data); err != nil {
		return nil, err
	}
	created, err := s.images.Create(ctx, userID, image, s.cfg.MaxPerListing)
	if err != nil {
		s.deleteObjects(ctx, image.StorageKey)
		return nil, err
	}
	setImageURL(created)
	return created, nil
}

func (s *ImageService) Delete(ctx context.Context, userID, listingID, imageID int64) error {
	if err := validateID(listingID); err != nil {
		return err
	}
	if imageID <= 0 {
		return &ValidationError{"image_id", "must be a positive integer"}
	}
	if err := requireOwner(ctx, s.listings, listingID, userID); err != nil {
		return err
	}
	key, err := s.images.Delete(ctx, listingID, imageID, userID)
	if err != nil {
		return err
	}
	s.deleteObjects(ctx, key)
	return nil
}

// Reorder sets the order of all images of the owner's listing; the first becomes the cover.
func (s *ImageService) Reorder(ctx context.Context, userID, listingID int64, imageIDs []int64) ([]model.ListingImage, error) {
	if err := validateID(listingID); err != nil {
		return nil, err
	}
	if len(imageIDs) == 0 {
		return nil, &ValidationError{"image_ids", "must contain at least one image id"}
	}
	if len(imageIDs) > s.cfg.MaxPerListing {
		return nil, &ValidationError{"image_ids", fmt.Sprintf("must not contain more than %d ids", s.cfg.MaxPerListing)}
	}
	seen := make(map[int64]struct{}, len(imageIDs))
	for _, id := range imageIDs {
		if id <= 0 {
			return nil, &ValidationError{"image_ids", "must contain positive integers"}
		}
		if _, ok := seen[id]; ok {
			return nil, &ValidationError{"image_ids", "must not contain duplicates"}
		}
		seen[id] = struct{}{}
	}
	if err := requireOwner(ctx, s.listings, listingID, userID); err != nil {
		return nil, err
	}
	images, err := s.images.Reorder(ctx, listingID, userID, imageIDs)
	if err != nil {
		return nil, err
	}
	for i := range images {
		setImageURL(&images[i])
	}
	return images, nil
}

// Open returns the stored file. Keys are random, so knowing the URL is what
// grants access; URLs of draft images are shown only to the owner.
func (s *ImageService) Open(ctx context.Context, key string) (*model.StoredObject, error) {
	if !storageKeyPattern.MatchString(key) {
		return nil, model.ErrImageNotFound
	}
	object, err := s.storage.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	// Trust the validated extension, not metadata stored alongside the object.
	object.ContentType = contentTypeByExt[key[strings.LastIndexByte(key, '.')+1:]]
	return object, nil
}

func (s *ImageService) inspect(data []byte) (model.ListingImage, error) {
	if len(data) == 0 {
		return model.ListingImage{}, &ValidationError{"image", "file is empty"}
	}
	contentType := http.DetectContentType(data)
	format, ok := imageFormats[contentType]
	if !ok {
		return model.ListingImage{}, model.ErrUnsupportedImage
	}
	// DecodeConfig reads only the header, so huge images are rejected cheaply.
	cfg, decoder, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || decoder != format.decoder || cfg.Width <= 0 || cfg.Height <= 0 {
		return model.ListingImage{}, model.ErrInvalidImage
	}
	if cfg.Width > s.cfg.MaxDimension || cfg.Height > s.cfg.MaxDimension {
		return model.ListingImage{}, &ValidationError{"image", fmt.Sprintf("width and height must not exceed %d pixels", s.cfg.MaxDimension)}
	}
	return model.ListingImage{
		ContentType: contentType, SizeBytes: int64(len(data)), Width: cfg.Width, Height: cfg.Height,
	}, nil
}

// deleteObjects is best-effort: a leftover object only wastes space.
func (s *ImageService) deleteObjects(ctx context.Context, keys ...string) {
	deleteStoredImages(ctx, s.storage, keys)
}

func deleteStoredImages(ctx context.Context, storage ImageStorage, keys []string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, key := range keys {
		if err := storage.Delete(ctx, key); err != nil && !errors.Is(err, model.ErrImageNotFound) {
			log.Printf("delete stored image %s: %v", key, err)
		}
	}
}

func setImageURL(image *model.ListingImage) {
	image.URL = imageURLPrefix + image.StorageKey
}

func randomHex() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate image key: %w", err)
	}
	return hex.EncodeToString(bytes[:]), nil
}
