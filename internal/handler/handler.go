package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/forumbit/rental_platform/docs"
	"github.com/forumbit/rental_platform/internal/model"
	"github.com/forumbit/rental_platform/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const maxRequestBodySize = 1 << 20 // 1 MiB

type ListingService interface {
	ListPublished(ctx context.Context, limit int, cursor int64) (*model.ListingPage, error)
	GetPublished(ctx context.Context, id int64) (*model.Listing, error)
	ListOwn(ctx context.Context, userID int64, status string, limit int, cursor int64) (*model.ListingPage, error)
	GetOwn(ctx context.Context, userID, id int64) (*model.Listing, error)
	Create(ctx context.Context, userID int64, input model.ListingInput) (*model.Listing, error)
	Update(ctx context.Context, userID, id int64, input model.ListingInput) (*model.Listing, error)
	Delete(ctx context.Context, userID, id int64) error
}

type Handler struct {
	listings     ListingService
	images       ImageService
	auth         AuthService
	registration RegistrationService
}

func New(listings ListingService, images ImageService, auth AuthService, registration RegistrationService) *Handler {
	return &Handler{listings: listings, images: images, auth: auth, registration: registration}
}

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Logger, middleware.Recoverer)
	docs.Mount(r)

	r.Route("/auth", func(r chi.Router) {
		r.Use(noStore)
		r.Post("/register", h.register)
		r.Post("/verification/resend", h.resendVerification)
		r.Post("/verify-email", h.verifyEmail)
		r.Get("/verify-email", verificationPage)
		r.Post("/login", h.login)
		r.Post("/refresh", h.refresh)
		r.Post("/logout", h.logout)
		r.With(h.authenticate).Get("/me", h.me)
	})

	r.Get("/listings", h.listListings)
	r.Get("/listings/{id}", h.getListing)
	r.With(h.authenticate).Post("/listings", h.createListing)
	r.With(h.authenticate).Put("/listings/{id}", h.updateListing)
	r.With(h.authenticate).Delete("/listings/{id}", h.deleteListing)
	r.With(h.authenticate).Post("/listings/{id}/images", h.uploadImage)
	r.With(h.authenticate).Put("/listings/{id}/images/order", h.reorderImages)
	r.With(h.authenticate).Delete("/listings/{id}/images/{imageID}", h.deleteImage)
	r.Get("/images/{key}", h.serveImage)

	// The author's own listings in any status: drafts, published and archived.
	r.Route("/me/listings", func(r chi.Router) {
		r.Use(h.authenticate, noStore)
		r.Get("/", h.listOwnListings)
		r.Get("/{id}", h.getOwnListing)
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	return r
}

func (h *Handler) listListings(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := pageParams(r)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	page, err := h.listings.ListPublished(r.Context(), limit, cursor)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) getListing(w http.ResponseWriter, r *http.Request) {
	id, err := listingID(r)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	listing, err := h.listings.GetPublished(r.Context(), id)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (h *Handler) listOwnListings(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := pageParams(r)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	page, err := h.listings.ListOwn(r.Context(), requestUserID(r), r.URL.Query().Get("status"), limit, cursor)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) getOwnListing(w http.ResponseWriter, r *http.Request) {
	id, err := listingID(r)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	listing, err := h.listings.GetOwn(r.Context(), requestUserID(r), id)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (h *Handler) createListing(w http.ResponseWriter, r *http.Request) {
	var input model.ListingInput
	if !decodeJSON(w, r, &input) {
		return
	}
	listing, err := h.listings.Create(r.Context(), requestUserID(r), input)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	w.Header().Set("Location", "/listings/"+strconv.FormatInt(listing.ID, 10))
	writeJSON(w, http.StatusCreated, listing)
}

func (h *Handler) updateListing(w http.ResponseWriter, r *http.Request) {
	id, err := listingID(r)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	var input model.ListingInput
	if !decodeJSON(w, r, &input) {
		return
	}
	listing, err := h.listings.Update(r.Context(), requestUserID(r), id, input)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (h *Handler) deleteListing(w http.ResponseWriter, r *http.Request) {
	id, err := listingID(r)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	if err := h.listings.Delete(r.Context(), requestUserID(r), id); err != nil {
		h.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func listingID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, &service.ValidationError{Field: "id", Message: "must be a positive integer"}
	}
	return id, nil
}

// pageParams reads keyset pagination: limit and the next_cursor of the previous page.
func pageParams(r *http.Request) (limit int, cursor int64, err error) {
	query := r.URL.Query()
	limit = service.DefaultListingsLimit
	if query.Has("limit") {
		if limit, err = strconv.Atoi(query.Get("limit")); err != nil {
			return 0, 0, &service.ValidationError{Field: "limit", Message: "must be an integer"}
		}
	}
	if query.Has("cursor") {
		cursor, err = strconv.ParseInt(query.Get("cursor"), 10, 64)
		if err != nil || cursor <= 0 {
			return 0, 0, &service.ValidationError{Field: "cursor", Message: "must be a positive integer"}
		}
	}
	return limit, cursor, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || mediaType != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return false
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeDecodeError(w, err)
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		var sizeError *http.MaxBytesError
		if errors.As(err, &sizeError) {
			writeDecodeError(w, err)
		} else {
			writeError(w, http.StatusBadRequest, "request body must contain a single JSON object")
		}
		return false
	}
	return true
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var sizeError *http.MaxBytesError
	if errors.As(err, &sizeError) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body must not exceed 1 MiB")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
}

func (h *Handler) handleError(w http.ResponseWriter, r *http.Request, err error) {
	var validationError *service.ValidationError
	var throttled *model.VerificationThrottledError
	var imageLimit *model.ImageLimitError
	switch {
	case errors.As(err, &validationError):
		writeError(w, http.StatusBadRequest, validationError.Error())
	case errors.Is(err, model.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid email or password")
	case errors.Is(err, model.ErrEmailTaken):
		writeError(w, http.StatusConflict, "email is already registered; request a verification email if needed")
	case errors.Is(err, model.ErrEmailNotVerified):
		writeError(w, http.StatusForbidden, "verify your email before logging in")
	case errors.Is(err, model.ErrInvalidVerification):
		writeError(w, http.StatusBadRequest, "verification is invalid, expired or already used; request a new email")
	case errors.Is(err, model.ErrMailUnavailable):
		writeError(w, http.StatusServiceUnavailable, "email delivery is unavailable; try requesting a verification email later")
	case errors.As(err, &throttled):
		seconds := int64((throttled.RetryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		writeError(w, http.StatusTooManyRequests, throttled.Error())
	// The owner is the authenticated user, so a missing owner means the account was deleted.
	case errors.Is(err, model.ErrInvalidToken), errors.Is(err, model.ErrOwnerNotFound):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "invalid or expired token")
	case errors.Is(err, model.ErrForbidden):
		writeError(w, http.StatusForbidden, "you can only modify your own listings")
	case errors.Is(err, model.ErrListingNotFound):
		writeError(w, http.StatusNotFound, "listing not found")
	case errors.Is(err, model.ErrImageNotFound):
		writeError(w, http.StatusNotFound, "image not found")
	case errors.Is(err, model.ErrImageTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge,
			"image must not exceed "+strconv.FormatInt(h.images.MaxSizeBytes(), 10)+" bytes")
	case errors.Is(err, model.ErrUnsupportedImage):
		writeError(w, http.StatusUnsupportedMediaType, "image must be JPEG, PNG or WebP")
	case errors.Is(err, model.ErrInvalidImage):
		writeError(w, http.StatusBadRequest, "image file is damaged or not a valid image")
	case errors.Is(err, model.ErrImageOrderMismatch):
		writeError(w, http.StatusBadRequest, "image_ids must list every image of the listing exactly once")
	case errors.As(err, &imageLimit):
		writeError(w, http.StatusConflict, imageLimit.Error())
	default:
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		log.Printf("encode response: %v", err)
		status = http.StatusInternalServerError
		body = []byte(`{"error":"internal server error"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(append(body, '\n')); err != nil {
		log.Printf("write response: %v", err)
	}
}
