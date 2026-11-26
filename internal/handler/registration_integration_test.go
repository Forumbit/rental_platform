package handler_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forumbit/rental_platform/internal/auth"
	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/internal/model"
	"github.com/forumbit/rental_platform/internal/repository"
)

func TestRegisterAndVerifyByCode(t *testing.T) {
	db := listingsTestDB(t)
	mailer := &captureMailer{}
	router, tokens := registrationTestRouter(t, db, mailer)
	input := model.RegisterInput{Email: " Anna@Yandex.Ru ", Password: testLoginPassword, FullName: " Анна Иванова "}
	created := request(t, router, "POST", "/auth/register", jsonBody(t, input), 201)
	response := responseBody[model.RegistrationResponse](t, created)
	if response.Email != "anna@yandex.ru" || strings.Contains(created.Body.String(), "token") || strings.Contains(created.Body.String(), "code") {
		t.Fatalf("unexpected registration response: %s", created.Body.String())
	}
	if created.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("registration responses must not be cached")
	}
	delivery := mailer.last(t)
	if delivery.To != response.Email || delivery.FullName != "Анна Иванова" || len(delivery.Code) != 6 || delivery.ValidMinutes != 15 {
		t.Fatalf("incorrect verification email: %+v", delivery)
	}
	user, err := repository.NewUserRepository(db).GetByEmail(t.Context(), response.Email)
	if err != nil {
		t.Fatal(err)
	}
	if user.EmailVerified || user.EmailVerifiedAt != nil || user.Role != "user" || !auth.CheckPassword(user.PasswordHash, input.Password) {
		t.Fatal("registered user must have a hashed password and an unverified address")
	}
	verification, err := repository.NewVerificationRepository(db).GetByEmail(t.Context(), response.Email)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewVerificationManager(verificationTestConfig().Secret, verificationTestConfig().TTL)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(verification.TokenHash, auth.HashVerificationToken(emailToken(t, delivery))) ||
		!bytes.Equal(verification.CodeHash, manager.HashCode(user.ID, verification.ID, delivery.Code)) {
		t.Fatal("verification secrets must be stored as challenge-specific hashes")
	}
	request(t, router, "POST", "/auth/register", jsonBody(t, input), 409)
	if mailer.count() != 1 {
		t.Fatal("duplicate registration must not send another email")
	}
	request(t, router, "POST", "/auth/login", jsonBody(t, model.LoginInput{Email: response.Email, Password: input.Password}), 403)
	request(t, router, "POST", "/auth/login", jsonBody(t, model.LoginInput{Email: response.Email, Password: "wrong-password"}), 401)
	// Even a directly inserted session cannot authenticate an unverified user.
	unverifiedPair := createTestSession(t, db, tokens, user.ID)
	request(t, router, "GET", "/auth/me", "", 401, unverifiedPair.AccessToken)
	request(t, router, "POST", "/auth/refresh", jsonBody(t, model.RefreshInput{RefreshToken: unverifiedPair.RefreshToken}), 401)

	request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Email: "ANNA@YANDEX.RU", Code: delivery.Code}), 200)
	verified, err := repository.NewUserRepository(db).GetByEmail(t.Context(), response.Email)
	if err != nil || !verified.EmailVerified || verified.EmailVerifiedAt == nil || verified.PasswordHash != user.PasswordHash {
		t.Fatalf("verification did not preserve and activate the user: %+v, %v", verified, err)
	}
	request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Email: response.Email, Code: delivery.Code}), 400)
	request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Token: emailToken(t, delivery)}), 400)
	pair := loginForTest(t, router, response.Email)
	me := responseBody[model.User](t, request(t, router, "GET", "/auth/me", "", 200, pair.AccessToken))
	if !me.EmailVerified || me.EmailVerifiedAt == nil || me.PasswordHash != "" {
		t.Fatal("current user must expose verification status but never the password hash")
	}
}

func TestVerificationResendAndMagicLink(t *testing.T) {
	db := listingsTestDB(t)
	mailer := &captureMailer{}
	router, _ := registrationTestRouter(t, db, mailer)
	email := "user@mail.ru"
	request(t, router, "POST", "/auth/register", jsonBody(t, model.RegisterInput{Email: email, Password: testLoginPassword, FullName: "User"}), 201)
	first := mailer.last(t)
	// Loading the landing page does not consume the challenge.
	landing := request(t, router, "GET", "/auth/verify-email", "", 200)
	if !strings.Contains(landing.Body.String(), "location.hash") || landing.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("magic link landing page must use a non-leaking URL fragment")
	}
	user, err := repository.NewUserRepository(db).GetByEmail(t.Context(), email)
	if err != nil || user.EmailVerified {
		t.Fatal("GET must not verify an email")
	}
	resendBody := jsonBody(t, model.EmailInput{Email: email})
	throttled := request(t, router, "POST", "/auth/verification/resend", resendBody, 429)
	seconds, err := strconv.Atoi(throttled.Header().Get("Retry-After"))
	if err != nil || seconds < 1 || seconds > 60 || mailer.count() != 1 {
		t.Fatal("resend cooldown did not prevent repeated delivery")
	}
	allowResend(t, db, email)
	request(t, router, "POST", "/auth/verification/resend", resendBody, 202)
	second := mailer.last(t)
	if emailToken(t, first) == emailToken(t, second) || mailer.count() != 2 {
		t.Fatal("resend must rotate the magic link")
	}
	request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Token: emailToken(t, first)}), 400)
	request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Token: emailToken(t, second)}), 200)
	request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Email: email, Code: second.Code}), 400)
	request(t, router, "POST", "/auth/verification/resend", resendBody, 202)
	request(t, router, "POST", "/auth/verification/resend", `{"email":"unknown@yandex.ru"}`, 202)
	if mailer.count() != 2 {
		t.Fatal("verified or unknown accounts must not receive verification emails")
	}
	loginForTest(t, router, email)
}

func TestVerificationExpirationAndAttemptLimit(t *testing.T) {
	db := listingsTestDB(t)
	mailer := &captureMailer{}
	router, _ := registrationTestRouter(t, db, mailer)
	email := "attempts@yandex.ru"
	request(t, router, "POST", "/auth/register", jsonBody(t, model.RegisterInput{Email: email, Password: testLoginPassword, FullName: "User"}), 201)
	delivery := mailer.last(t)
	wrongCode := "000000"
	if wrongCode == delivery.Code {
		wrongCode = "000001"
	}
	for range 5 {
		request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Email: email, Code: wrongCode}), 400)
	}
	request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Email: email, Code: delivery.Code}), 400)
	request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Token: emailToken(t, delivery)}), 400)
	var attempts int
	if err := db.QueryRowContext(t.Context(), `SELECT attempts FROM email_verifications`).Scan(&attempts); err != nil || attempts != 5 {
		t.Fatalf("recorded attempts = %d, err = %v", attempts, err)
	}
	allowResend(t, db, email)
	request(t, router, "POST", "/auth/verification/resend", jsonBody(t, model.EmailInput{Email: email}), 202)
	delivery = mailer.last(t)
	if _, err := db.ExecContext(t.Context(), `UPDATE email_verifications SET expires_at = NOW() - INTERVAL '1 second'`); err != nil {
		t.Fatal(err)
	}
	request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Email: email, Code: delivery.Code}), 400)
	request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Token: emailToken(t, delivery)}), 400)
	allowResend(t, db, email)
	request(t, router, "POST", "/auth/verification/resend", jsonBody(t, model.EmailInput{Email: email}), 202)
	delivery = mailer.last(t)
	request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Email: email, Code: delivery.Code}), 200)
}

func TestVerificationIsConsumedAtomically(t *testing.T) {
	db := listingsTestDB(t)
	mailer := &captureMailer{}
	router, _ := registrationTestRouter(t, db, mailer)
	email := "concurrent@yandex.ru"
	request(t, router, "POST", "/auth/register", jsonBody(t, model.RegisterInput{Email: email, Password: testLoginPassword, FullName: "User"}), 201)
	delivery := mailer.last(t)
	codeBody := jsonBody(t, model.VerifyEmailInput{Email: email, Code: delivery.Code})
	tokenBody := jsonBody(t, model.VerifyEmailInput{Token: emailToken(t, delivery)})
	const requests = 4
	start := make(chan struct{})
	responses := make(chan int, requests)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			body := codeBody
			if i%2 == 0 {
				body = tokenBody
			}
			req := httptest.NewRequest("POST", "/auth/verify-email", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			responses <- response.Code
		}()
	}
	close(start)
	wg.Wait()
	close(responses)
	successes := 0
	for status := range responses {
		if status == 200 {
			successes++
		} else if status != 400 {
			t.Fatalf("concurrent verification returned %d", status)
		}
	}
	if successes != 1 {
		t.Fatalf("successful verifications = %d, want 1", successes)
	}
}

func TestRegistrationDeliveryFailureCanBeRetried(t *testing.T) {
	db := listingsTestDB(t)
	mailer := &captureMailer{disabled: true}
	router, _ := registrationTestRouter(t, db, mailer)
	email := "delivery@yandex.ru"
	body := jsonBody(t, model.RegisterInput{Email: email, Password: testLoginPassword, FullName: "User"})
	request(t, router, "POST", "/auth/register", body, 503)
	if _, err := repository.NewUserRepository(db).GetByEmail(t.Context(), email); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatal("disabled SMTP must not create an undeliverable account")
	}
	mailer.disabled = false
	mailer.err = errors.New("SMTP server temporarily unavailable")
	request(t, router, "POST", "/auth/register", body, 503)
	user, err := repository.NewUserRepository(db).GetByEmail(t.Context(), email)
	if err != nil || user.EmailVerified {
		t.Fatal("failed delivery must leave an unverified account available for resend")
	}
	if _, err := repository.NewVerificationRepository(db).GetByEmail(t.Context(), email); !errors.Is(err, model.ErrInvalidVerification) {
		t.Fatal("failed delivery must invalidate its challenge and release the cooldown")
	}
	mailer.err = nil
	request(t, router, "POST", "/auth/verification/resend", jsonBody(t, model.EmailInput{Email: email}), 202)
	delivery := mailer.last(t)
	request(t, router, "POST", "/auth/verify-email", jsonBody(t, model.VerifyEmailInput{Email: email, Code: delivery.Code}), 200)
	loginForTest(t, router, email)
}

func TestRegistrationAndVerificationValidation(t *testing.T) {
	db := listingsTestDB(t)
	mailer := &captureMailer{}
	router, _ := registrationTestRouter(t, db, mailer)
	for _, body := range []string{
		`{}`, `null`, `[]`,
		`{"email":"Name <user@yandex.ru>","password":"password123","full_name":"User"}`,
		`{"email":"user@yandex.ru","password":"short","full_name":"User"}`,
		jsonBody(t, model.RegisterInput{Email: "user@yandex.ru", Password: strings.Repeat("x", 73), FullName: "User"}),
		`{"email":"user@yandex.ru","password":"password123","full_name":" "}`,
		`{"email":"user@yandex.ru","password":"password123","full_name":"User","role":"admin"}`,
	} {
		request(t, router, "POST", "/auth/register", body, 400)
	}
	for _, body := range []string{
		`{}`, `{"email":"not-email","code":"123456"}`, `{"email":"user@yandex.ru","code":"12345"}`,
		`{"email":"user@yandex.ru","code":"abcdef"}`, `{"token":"bad"}`,
		`{"token":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","code":"123456"}`,
		`{"email":"user@yandex.ru","code":"123456","unknown":true}`,
	} {
		request(t, router, "POST", "/auth/verify-email", body, 400)
	}
	if mailer.count() != 0 {
		t.Fatal("invalid requests must not send emails")
	}
}

func verificationTestConfig() config.VerificationConfig {
	return config.VerificationConfig{
		Secret: strings.Repeat("v", 32), TTL: 15 * time.Minute, ResendInterval: time.Minute,
		MaxAttempts: 5, PublicBaseURL: "http://localhost:8080",
	}
}

type captureMailer struct {
	mu       sync.Mutex
	emails   []model.VerificationEmail
	disabled bool
	err      error
}

func (m *captureMailer) Available() bool {
	return !m.disabled
}

func (m *captureMailer) SendVerification(_ context.Context, email model.VerificationEmail) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.emails = append(m.emails, email)
	return nil
}

func (m *captureMailer) last(t *testing.T) model.VerificationEmail {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.emails) == 0 {
		t.Fatal("expected a verification email")
	}
	return m.emails[len(m.emails)-1]
}

func (m *captureMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.emails)
}

func emailToken(t *testing.T, email model.VerificationEmail) string {
	t.Helper()
	link, err := url.Parse(email.URL)
	if err != nil {
		t.Fatal(err)
	}
	if link.Path != "/auth/verify-email" || link.RawQuery != "" {
		t.Fatalf("unexpected verification URL: %s", email.URL)
	}
	fragment, err := url.ParseQuery(link.Fragment)
	if err != nil || len(fragment.Get("token")) != 64 {
		t.Fatalf("missing opaque verification token: %s", email.URL)
	}
	return fragment.Get("token")
}

func allowResend(t *testing.T, db *sql.DB, email string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `UPDATE email_verifications SET sent_at = NOW() - INTERVAL '2 minutes'
		WHERE user_id = (SELECT id FROM users WHERE LOWER(email) = LOWER($1))`, email); err != nil {
		t.Fatal(err)
	}
}
