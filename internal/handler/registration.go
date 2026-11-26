package handler

import (
	"context"
	_ "embed"
	"net/http"

	"github.com/forumbit/rental_platform/internal/model"
)

type RegistrationService interface {
	Register(ctx context.Context, input model.RegisterInput) (*model.RegistrationResponse, error)
	Resend(ctx context.Context, email string) error
	Verify(ctx context.Context, input model.VerifyEmailInput) error
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var input model.RegisterInput
	if !decodeJSON(w, r, &input) {
		return
	}
	response, err := h.registration.Register(r.Context(), input)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) resendVerification(w http.ResponseWriter, r *http.Request) {
	var input model.EmailInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := h.registration.Resend(r.Context(), input.Email); err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, model.MessageResponse{Message: "If this email needs verification, a new message has been sent."})
}

func (h *Handler) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var input model.VerifyEmailInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := h.registration.Verify(r.Context(), input); err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, model.MessageResponse{Message: "Email verified. You can now log in."})
}

//go:embed pages/verify-email.html
var verifyPage []byte

func verificationPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(verifyPage)
}
