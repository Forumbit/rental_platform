package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/forumbit/rental_platform/internal/model"
)

type AuthService interface {
	Login(ctx context.Context, input model.LoginInput) (*model.TokenPair, error)
	Refresh(ctx context.Context, refreshToken string) (*model.TokenPair, error)
	Logout(ctx context.Context, refreshToken string) error
	Authenticate(ctx context.Context, accessToken string) (*model.User, error)
}

type authUserKey struct{}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var input model.LoginInput
	if !decodeJSON(w, r, &input) {
		return
	}
	pair, err := h.auth.Login(r.Context(), input)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pair)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var input model.RefreshInput
	if !decodeJSON(w, r, &input) {
		return
	}
	pair, err := h.auth.Refresh(r.Context(), input.RefreshToken)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pair)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	var input model.RefreshInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := h.auth.Logout(r.Context(), input.RefreshToken); err != nil {
		h.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(authUserKey{}).(*model.User)
	if !ok || user == nil {
		h.handleError(w, r, model.ErrInvalidToken)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (h *Handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			h.handleError(w, r, model.ErrInvalidToken)
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			h.handleError(w, r, model.ErrInvalidToken)
			return
		}
		user, err := h.auth.Authenticate(r.Context(), parts[1])
		if err != nil {
			h.handleError(w, r, err)
			return
		}
		if user == nil || user.ID <= 0 {
			h.handleError(w, r, model.ErrInvalidToken)
			return
		}
		ctx := context.WithValue(r.Context(), authUserKey{}, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestUserID(r *http.Request) int64 {
	user, ok := r.Context().Value(authUserKey{}).(*model.User)
	if !ok || user == nil {
		return 0
	}
	return user.ID
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}
