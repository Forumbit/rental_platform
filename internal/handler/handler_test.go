package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forumbit/rental_platform/internal/handler"
	"github.com/forumbit/rental_platform/internal/model"
	"github.com/forumbit/rental_platform/internal/service"
)

const validListingJSON = `{"title":"Дом","description":"Дом у леса","property_type":"house","price_minor":10000,"city":"Истра"}`

func TestListingRequestValidation(t *testing.T) {
	router := unitTestRouter(t)
	withField := func(field string) string {
		return strings.TrimSuffix(validListingJSON, "}") + "," + field + "}"
	}

	tests := []struct {
		name        string
		method      string
		target      string
		body        string
		contentType string
		status      int
	}{
		{name: "invalid id", method: "GET", target: "/listings/no-id", status: 400},
		{name: "zero id", method: "PUT", target: "/listings/0", body: validListingJSON, status: 400},
		{name: "overflowing id", method: "DELETE", target: "/listings/9223372036854775808", status: 400},
		{name: "invalid limit", method: "GET", target: "/listings?limit=abc", status: 400},
		{name: "empty limit", method: "GET", target: "/listings?limit=", status: 400},
		{name: "zero limit", method: "GET", target: "/listings?limit=0", status: 400},
		{name: "large limit", method: "GET", target: "/listings?limit=101", status: 400},
		{name: "zero cursor", method: "GET", target: "/listings?cursor=0", status: 400},
		{name: "negative cursor", method: "GET", target: "/listings?cursor=-1", status: 400},
		{name: "invalid cursor", method: "GET", target: "/listings?cursor=abc", status: 400},
		{name: "own listings invalid cursor", method: "GET", target: "/me/listings?cursor=abc", status: 400},
		{name: "own listings large limit", method: "GET", target: "/me/listings?limit=101", status: 400},
		{name: "own listings unknown status", method: "GET", target: "/me/listings?status=deleted", status: 400},
		{name: "own listing invalid id", method: "GET", target: "/me/listings/no-id", status: 400},
		{name: "empty body", method: "POST", target: "/listings", status: 400},
		{name: "malformed JSON", method: "POST", target: "/listings", body: `{"title":`, status: 400},
		{name: "null body", method: "POST", target: "/listings", body: `null`, status: 400},
		{name: "array body", method: "POST", target: "/listings", body: `[]`, status: 400},
		{name: "unknown field", method: "POST", target: "/listings", body: withField(`"unknown":true`), status: 400},
		{name: "server managed field", method: "POST", target: "/listings", body: withField(`"id":1`), status: 400},
		{name: "multiple objects", method: "POST", target: "/listings", body: validListingJSON + validListingJSON, status: 400},
		{name: "trailing garbage", method: "PUT", target: "/listings/1", body: validListingJSON + "garbage", status: 400},
		{name: "wrong field type", method: "POST", target: "/listings", body: withField(`"bedrooms":"three"`), status: 400},
		{name: "server managed owner", method: "POST", target: "/listings", body: withField(`"owner_id":1`), status: 400},
		{name: "server managed published_at", method: "PUT", target: "/listings/1", body: withField(`"published_at":"2026-01-01T00:00:00Z"`), status: 400},
		{name: "smallint overflow", method: "POST", target: "/listings", body: withField(`"bedrooms":32768`), status: 400},
		{name: "area rounds to zero", method: "POST", target: "/listings", body: withField(`"area_sqm":0.001`), status: 400},
		{name: "numeric overflow", method: "POST", target: "/listings", body: withField(`"area_sqm":1000000`), status: 400},
		{name: "postgres null character", method: "POST", target: "/listings", body: withField(`"address":"street\u0000"`), status: 400},
		{name: "missing required fields", method: "POST", target: "/listings", body: `{"title":"Дом"}`, status: 400},
		{name: "partial PUT", method: "PUT", target: "/listings/1", body: `{"price_minor":20000}`, status: 400},
		{name: "wrong content type", method: "POST", target: "/listings", body: validListingJSON, contentType: "text/plain", status: 415},
		{name: "oversized body", method: "POST", target: "/listings", body: `{"description":"` + strings.Repeat("x", 1<<20) + `"}`, status: 413},
		{name: "oversized trailing whitespace", method: "POST", target: "/listings", body: validListingJSON + strings.Repeat(" ", 1<<20), status: 413},
		{name: "unknown route", method: "GET", target: "/unknown", status: 404},
		{name: "unsupported method", method: "PATCH", target: "/listings/1", body: validListingJSON, status: 405},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer test-access-token")
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.status, response.Body.String())
			}
			if !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("unexpected Content-Type: %s", response.Header().Get("Content-Type"))
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Error == "" {
				t.Fatalf("expected a JSON error, got %s", response.Body.String())
			}
		})
	}
}

func TestInternalErrorResponse(t *testing.T) {
	router := unitTestRouter(t)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/listings/1", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if strings.TrimSpace(response.Body.String()) != `{"error":"internal server error"}` {
		t.Fatalf("unexpected error response: %s", response.Body.String())
	}
}

func unitTestRouter(t *testing.T) http.Handler {
	t.Helper()
	images, err := service.NewImageService(failingRepository{}, nil, newMemoryStorage(), imagesTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	listings := service.NewListingService(failingRepository{}, nil, nil)
	return handler.New(listings, images, testAuthService{}, testRegistrationService{}).Routes()
}

type failingRepository struct{}

var errDatabase = errors.New("database connection failed: private connection details")

func (failingRepository) List(context.Context, model.ListingFilter) ([]model.Listing, error) {
	return nil, errDatabase
}

func (failingRepository) GetByID(context.Context, int64) (*model.Listing, error) {
	return nil, errDatabase
}

func (failingRepository) Create(context.Context, int64, model.ListingInput) (*model.Listing, error) {
	return nil, errDatabase
}

func (failingRepository) Update(context.Context, int64, int64, model.ListingInput) (*model.Listing, error) {
	return nil, errDatabase
}

func (failingRepository) Delete(context.Context, int64, int64) error {
	return errDatabase
}

func TestAuthenticationRequired(t *testing.T) {
	router := unitTestRouter(t)
	for _, tt := range []struct {
		method  string
		path    string
		headers []string
	}{
		{"POST", "/listings", nil},
		{"PUT", "/listings/1", nil},
		{"DELETE", "/listings/1", nil},
		{"POST", "/listings/1/images", nil},
		{"DELETE", "/listings/1/images/1", nil},
		{"GET", "/me/listings", nil},
		{"GET", "/me/listings/1", nil},
		{"GET", "/auth/me", nil},
		{"GET", "/auth/me", []string{"Basic test-access-token"}},
		{"GET", "/auth/me", []string{"Bearer"}},
		{"GET", "/auth/me", []string{"Bearer invalid-token"}},
		{"GET", "/auth/me", []string{"Bearer test-access-token extra"}},
		{"GET", "/auth/me", []string{"Bearer test-access-token", "Bearer test-access-token"}},
	} {
		req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(validListingJSON))
		for _, value := range tt.headers {
			req.Header.Add("Authorization", value)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("%s %s: expected Bearer challenge, got %d %s", tt.method, tt.path, response.Code, response.Body.String())
		}
	}
}

type testAuthService struct{}

func (testAuthService) Login(context.Context, model.LoginInput) (*model.TokenPair, error) {
	return nil, model.ErrInvalidCredentials
}

func (testAuthService) Refresh(context.Context, string) (*model.TokenPair, error) {
	return nil, model.ErrInvalidToken
}

func (testAuthService) Logout(context.Context, string) error {
	return model.ErrInvalidToken
}

func (testAuthService) Authenticate(_ context.Context, token string) (*model.User, error) {
	if token != "test-access-token" {
		return nil, model.ErrInvalidToken
	}
	return &model.User{ID: 1}, nil
}

type testRegistrationService struct{}

func (testRegistrationService) Register(context.Context, model.RegisterInput) (*model.RegistrationResponse, error) {
	return nil, model.ErrMailUnavailable
}

func (testRegistrationService) Resend(context.Context, string) error {
	return model.ErrMailUnavailable
}

func (testRegistrationService) Verify(context.Context, model.VerifyEmailInput) error {
	return model.ErrInvalidVerification
}

func TestCORS(t *testing.T) {
	images, err := service.NewImageService(failingRepository{}, nil, newMemoryStorage(), imagesTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	router := handler.New(service.NewListingService(failingRepository{}, nil, nil), images, testAuthService{}, testRegistrationService{}).
		WithCORS([]string{"https://your-project.site", "http://localhost:*"}).Routes()

	for _, tt := range []struct {
		origin  string
		allowed bool
	}{
		{"https://your-project.site", true},
		{"http://localhost:5173", true},
		{"https://evil.example", false},
	} {
		preflight := httptest.NewRequest(http.MethodOptions, "/listings/1/images", nil)
		preflight.Header.Set("Origin", tt.origin)
		preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
		preflight.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, preflight)
		got := response.Header().Get("Access-Control-Allow-Origin")
		if tt.allowed && (got != tt.origin || !strings.Contains(strings.ToLower(response.Header().Get("Access-Control-Allow-Headers")), "authorization")) {
			t.Fatalf("%s: preflight headers = %v", tt.origin, response.Header())
		}
		if !tt.allowed && got != "" {
			t.Fatalf("%s must not be allowed, got %q", tt.origin, got)
		}

		// Simple requests, including image downloads, carry the header too.
		get := httptest.NewRequest(http.MethodGet, "/images/0123456789abcdef0123456789abcdef.png", nil)
		get.Header.Set("Origin", tt.origin)
		response = httptest.NewRecorder()
		router.ServeHTTP(response, get)
		if got := response.Header().Get("Access-Control-Allow-Origin"); (got == tt.origin) != tt.allowed {
			t.Fatalf("%s: GET Access-Control-Allow-Origin = %q", tt.origin, got)
		}
	}
}
