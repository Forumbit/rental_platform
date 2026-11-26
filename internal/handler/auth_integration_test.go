package handler_test

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forumbit/rental_platform/internal/auth"
	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/internal/handler"
	"github.com/forumbit/rental_platform/internal/model"
	"github.com/forumbit/rental_platform/internal/repository"
	"github.com/forumbit/rental_platform/internal/service"
	"github.com/golang-jwt/jwt/v5"
)

const testLoginPassword = "correct-password-for-tests"

func TestAuthenticationFlow(t *testing.T) {
	db := listingsTestDB(t)
	users := service.NewUserService(repository.NewUserRepository(db))
	user, created, err := users.EnsureUser(t.Context(), " Owner@Rental.Test ", testLoginPassword, "Rental Owner")
	if err != nil || !created {
		t.Fatalf("seed user: created=%v, err=%v", created, err)
	}
	if user.Email != "owner@rental.test" || user.Role != "user" || !auth.CheckPassword(user.PasswordHash, testLoginPassword) {
		t.Fatal("seed user has incorrect email, role or password hash")
	}
	existing, created, err := users.EnsureUser(t.Context(), "OWNER@RENTAL.TEST", "different-password", "Different Name")
	if err != nil || created || existing.ID != user.ID || existing.PasswordHash != user.PasswordHash || existing.FullName != user.FullName {
		t.Fatal("repeated seeding must preserve the existing account")
	}
	router, tokens := authTestRouter(t, db)

	wrongPassword := request(t, router, "POST", "/auth/login", jsonBody(t, model.LoginInput{
		Email: user.Email, Password: "wrong-password",
	}), 401)
	unknownAccount := request(t, router, "POST", "/auth/login", jsonBody(t, model.LoginInput{
		Email: "unknown@rental.test", Password: testLoginPassword,
	}), 401)
	if wrongPassword.Body.String() != unknownAccount.Body.String() {
		t.Fatal("login errors disclose whether an account exists")
	}
	for _, body := range []string{
		`{}`, `{"email":"not-an-email","password":"password"}`, `{"email":"owner@rental.test"}`,
		jsonBody(t, model.LoginInput{Email: user.Email, Password: strings.Repeat("x", 73)}),
	} {
		request(t, router, "POST", "/auth/login", body, 400)
	}
	request(t, router, "POST", "/auth/refresh", `{}`, 400)
	request(t, router, "POST", "/auth/logout", `{}`, 400)
	request(t, router, "POST", "/auth/refresh", `{"refresh_token":"not-a-token"}`, 401)
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM refresh_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed login created a session: count=%d, err=%v", count, err)
	}

	loginResponse := request(t, router, "POST", "/auth/login", jsonBody(t, model.LoginInput{
		Email: " OWNER@RENTAL.TEST ", Password: testLoginPassword,
	}), 200)
	if loginResponse.Header().Get("Cache-Control") != "no-store" || loginResponse.Header().Get("Pragma") != "no-cache" {
		t.Fatal("token responses must disable caching")
	}
	pair := responseBody[model.TokenPair](t, loginResponse)
	identity, err := tokens.ParseRefresh(pair.RefreshToken)
	if err != nil || identity.UserID != user.ID {
		t.Fatalf("invalid refresh token identity: %+v, %v", identity, err)
	}
	assertStoredRefresh(t, db, identity.SessionID, pair)
	meResponse := request(t, router, "GET", "/auth/me", "", 200, pair.AccessToken)
	me := responseBody[model.User](t, meResponse)
	if me.ID != user.ID || me.Email != user.Email || me.Role != "user" || strings.Contains(meResponse.Body.String(), "password") {
		t.Fatalf("unexpected current user: %s", meResponse.Body.String())
	}
	request(t, router, "GET", "/auth/me", "", 401, pair.RefreshToken)
	request(t, router, "POST", "/auth/refresh", jsonBody(t, model.RefreshInput{RefreshToken: pair.AccessToken}), 401)

	// Refresh works independently of an expired access token.
	cfg := authTestConfig()
	expiredAccess, err := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{
		SessionID: identity.SessionID, TokenUse: "access",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: strconv.FormatInt(user.ID, 10), ID: strings.Repeat("a", 32),
			Issuer: cfg.Issuer, Audience: jwt.ClaimStrings{cfg.Audience},
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
	}).SignedString([]byte(cfg.AccessSecret))
	if err != nil {
		t.Fatal(err)
	}
	request(t, router, "GET", "/auth/me", "", 401, expiredAccess)
	rotated := responseBody[model.TokenPair](t, request(t, router, "POST", "/auth/refresh",
		jsonBody(t, model.RefreshInput{RefreshToken: pair.RefreshToken}), 200, expiredAccess))
	if rotated.RefreshToken == pair.RefreshToken || rotated.AccessToken == pair.AccessToken {
		t.Fatal("refresh did not rotate both tokens")
	}
	assertStoredRefresh(t, db, identity.SessionID, rotated)
	request(t, router, "GET", "/auth/me", "", 200, rotated.AccessToken)

	// Replaying a used refresh token revokes the entire session, including access.
	request(t, router, "POST", "/auth/refresh", jsonBody(t, model.RefreshInput{RefreshToken: pair.RefreshToken}), 401)
	request(t, router, "POST", "/auth/refresh", jsonBody(t, model.RefreshInput{RefreshToken: rotated.RefreshToken}), 401)
	request(t, router, "GET", "/auth/me", "", 401, rotated.AccessToken)
	request(t, router, "GET", "/auth/me", "", 401, pair.AccessToken)

	firstSession := loginForTest(t, router, user.Email)
	secondSession := loginForTest(t, router, user.Email)
	logoutBody := jsonBody(t, model.RefreshInput{RefreshToken: firstSession.RefreshToken})
	logout := request(t, router, "POST", "/auth/logout", logoutBody, 204)
	if logout.Body.Len() != 0 {
		t.Fatal("logout must return an empty body")
	}
	request(t, router, "POST", "/auth/logout", logoutBody, 204)
	request(t, router, "GET", "/auth/me", "", 401, firstSession.AccessToken)
	request(t, router, "POST", "/auth/refresh", logoutBody, 401)
	request(t, router, "POST", "/listings", validListingJSON, 401, firstSession.AccessToken)
	request(t, router, "GET", "/auth/me", "", 200, secondSession.AccessToken)
	request(t, router, "POST", "/auth/refresh", jsonBody(t, model.RefreshInput{RefreshToken: secondSession.RefreshToken}), 200)
}

func TestConcurrentRefreshRevokesReusedSession(t *testing.T) {
	db := listingsTestDB(t)
	user := seedAuthTestUser(t, db)
	router, _ := authTestRouter(t, db)
	pair := loginForTest(t, router, user.Email)
	body := jsonBody(t, model.RefreshInput{RefreshToken: pair.RefreshToken})
	const requests = 4
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest("POST", "/auth/refresh", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			responses <- response
		}()
	}
	close(start)
	wg.Wait()
	close(responses)
	var winner model.TokenPair
	successes := 0
	for response := range responses {
		switch response.Code {
		case 200:
			successes++
			winner = responseBody[model.TokenPair](t, response)
		case 401:
		default:
			t.Fatalf("concurrent refresh returned %d: %s", response.Code, response.Body.String())
		}
	}
	if successes != 1 {
		t.Fatalf("successful rotations = %d, want 1", successes)
	}
	request(t, router, "GET", "/auth/me", "", 401, winner.AccessToken)
	request(t, router, "POST", "/auth/refresh", jsonBody(t, model.RefreshInput{RefreshToken: winner.RefreshToken}), 401)
}

func TestSessionExpirationDeletionAndRotationRollback(t *testing.T) {
	db := listingsTestDB(t)
	user := seedAuthTestUser(t, db)
	router, tokens := authTestRouter(t, db)
	first := createTestSession(t, db, tokens, user.ID)
	second := createTestSession(t, db, tokens, user.ID)
	identity, err := tokens.ParseRefresh(first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	// A constraint failure while rotating must leave the previous token usable.
	err = repository.NewSessionRepository(db).Rotate(t.Context(), auth.HashRefreshToken(first.RefreshToken), model.RefreshSession{
		ID: identity.SessionID, UserID: user.ID, TokenHash: auth.HashRefreshToken(second.RefreshToken), ExpiresAt: second.RefreshExpiresAt,
	})
	if err == nil {
		t.Fatal("expected a duplicate token hash constraint error")
	}
	assertStoredRefresh(t, db, identity.SessionID, *first)
	rotated := responseBody[model.TokenPair](t, request(t, router, "POST", "/auth/refresh",
		jsonBody(t, model.RefreshInput{RefreshToken: first.RefreshToken}), 200))
	if _, err := db.ExecContext(t.Context(), `UPDATE refresh_sessions SET expires_at = NOW() - INTERVAL '1 second' WHERE id = $1`, identity.SessionID); err != nil {
		t.Fatal(err)
	}
	request(t, router, "GET", "/auth/me", "", 401, rotated.AccessToken)
	request(t, router, "POST", "/auth/refresh", jsonBody(t, model.RefreshInput{RefreshToken: rotated.RefreshToken}), 401)
	if _, err := db.ExecContext(t.Context(), `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
		t.Fatal(err)
	}
	request(t, router, "GET", "/auth/me", "", 401, second.AccessToken)
	request(t, router, "POST", "/auth/refresh", jsonBody(t, model.RefreshInput{RefreshToken: second.RefreshToken}), 401)
}

func authTestConfig() config.AuthConfig {
	return config.AuthConfig{
		AccessSecret: strings.Repeat("a", 32), RefreshSecret: strings.Repeat("r", 32),
		AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour,
		Issuer: "rental-test", Audience: "rental-test-api",
	}
}

func authTestRouter(t *testing.T, db *sql.DB) (http.Handler, *auth.TokenManager) {
	return registrationTestRouter(t, db, &captureMailer{})
}

func registrationTestRouter(t *testing.T, db *sql.DB, mailer service.VerificationMailer) (http.Handler, *auth.TokenManager) {
	t.Helper()
	tokens, err := auth.NewTokenManager(authTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	authService := service.NewAuthService(repository.NewUserRepository(db), repository.NewSessionRepository(db), tokens)
	listings := service.NewListingService(repository.NewListingRepository(db))
	registration, err := service.NewRegistrationService(repository.NewUserRepository(db), repository.NewVerificationRepository(db), mailer, verificationTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	return handler.New(listings, authService, registration).Routes(), tokens
}

func seedAuthTestUser(t *testing.T, db *sql.DB) *model.User {
	t.Helper()
	user, _, err := service.NewUserService(repository.NewUserRepository(db)).EnsureUser(t.Context(),
		"owner@rental.test", testLoginPassword, "Rental Owner")
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func loginForTest(t *testing.T, router http.Handler, email string) model.TokenPair {
	t.Helper()
	return responseBody[model.TokenPair](t, request(t, router, "POST", "/auth/login",
		jsonBody(t, model.LoginInput{Email: email, Password: testLoginPassword}), 200))
}

func createTestSession(t *testing.T, db *sql.DB, tokens *auth.TokenManager, userID int64) *model.TokenPair {
	t.Helper()
	sessionID, err := auth.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tokens.Issue(userID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.NewSessionRepository(db).Create(t.Context(), model.RefreshSession{
		ID: sessionID, UserID: userID, TokenHash: auth.HashRefreshToken(pair.RefreshToken), ExpiresAt: pair.RefreshExpiresAt,
	}); err != nil {
		t.Fatal(err)
	}
	return pair
}

func assertStoredRefresh(t *testing.T, db *sql.DB, sessionID string, pair model.TokenPair) {
	t.Helper()
	var hash []byte
	var expiresAt time.Time
	if err := db.QueryRowContext(t.Context(), `SELECT token_hash, expires_at FROM refresh_sessions WHERE id = $1`, sessionID).Scan(&hash, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if len(hash) != 32 || !bytes.Equal(hash, auth.HashRefreshToken(pair.RefreshToken)) || !expiresAt.Equal(pair.RefreshExpiresAt) {
		t.Fatal("stored refresh hash or expiration is incorrect")
	}
}
