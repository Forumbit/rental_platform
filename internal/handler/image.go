package handler

import (
	"context"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"

	"github.com/forumbit/rental_platform/internal/model"
	"github.com/forumbit/rental_platform/internal/service"
	"github.com/go-chi/chi/v5"
)

// multipartOverhead allows for boundaries and part headers around the file.
const multipartOverhead = 64 << 10

const imageFormField = "image"

type ImageService interface {
	MaxSizeBytes() int64
	Upload(ctx context.Context, userID, listingID int64, data []byte) (*model.ListingImage, error)
	Delete(ctx context.Context, userID, listingID, imageID int64) error
	Reorder(ctx context.Context, userID, listingID int64, imageIDs []int64) ([]model.ListingImage, error)
	Open(ctx context.Context, key string) (*model.StoredObject, error)
}

// uploadImage accepts multipart/form-data with exactly one file field named "image".
func (h *Handler) uploadImage(w http.ResponseWriter, r *http.Request) {
	listingID, err := listingID(r)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be multipart/form-data")
		return
	}
	maxSize := h.images.MaxSizeBytes()
	r.Body = http.MaxBytesReader(w, r.Body, maxSize+multipartOverhead)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart body")
		return
	}

	var data []byte
	found := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			h.writeUploadError(w, r, err)
			return
		}
		if part.FormName() != imageFormField || found {
			part.Close()
			writeError(w, http.StatusBadRequest, "body must contain exactly one file field named image")
			return
		}
		data, err = io.ReadAll(io.LimitReader(part, maxSize+1))
		part.Close()
		if err != nil {
			h.writeUploadError(w, r, err)
			return
		}
		if int64(len(data)) > maxSize {
			h.handleError(w, r, model.ErrImageTooLarge)
			return
		}
		found = true
	}
	if !found {
		writeError(w, http.StatusBadRequest, "body must contain exactly one file field named image")
		return
	}

	image, err := h.images.Upload(r.Context(), requestUserID(r), listingID, data)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, image)
}

func (h *Handler) writeUploadError(w http.ResponseWriter, r *http.Request, err error) {
	var sizeError *http.MaxBytesError
	if errors.As(err, &sizeError) {
		h.handleError(w, r, model.ErrImageTooLarge)
		return
	}
	writeError(w, http.StatusBadRequest, "invalid multipart body")
}

func (h *Handler) deleteImage(w http.ResponseWriter, r *http.Request) {
	listingID, err := listingID(r)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	imageID, err := strconv.ParseInt(chi.URLParam(r, "imageID"), 10, 64)
	if err != nil || imageID <= 0 {
		h.handleError(w, r, &service.ValidationError{Field: "image_id", Message: "must be a positive integer"})
		return
	}
	if err := h.images.Delete(r.Context(), requestUserID(r), listingID, imageID); err != nil {
		h.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reorderImages accepts the full list of image IDs in the new order.
func (h *Handler) reorderImages(w http.ResponseWriter, r *http.Request) {
	listingID, err := listingID(r)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	var input model.ImageOrderInput
	if !decodeJSON(w, r, &input) {
		return
	}
	images, err := h.images.Reorder(r.Context(), requestUserID(r), listingID, input.ImageIDs)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, images)
}

// serveImage streams a file from storage. Keys never change, so the response
// can be cached for a year.
func (h *Handler) serveImage(w http.ResponseWriter, r *http.Request) {
	object, err := h.images.Open(r.Context(), chi.URLParam(r, "key"))
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	defer object.Body.Close()
	header := w.Header()
	header.Set("Content-Type", object.ContentType)
	header.Set("Content-Length", strconv.FormatInt(object.Size, 10))
	header.Set("Cache-Control", "public, max-age=31536000, immutable")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, object.Body); err != nil {
		log.Printf("stream image: %v", err)
	}
}
