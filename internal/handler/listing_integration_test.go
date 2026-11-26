package handler_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/forumbit/rental_platform/internal/model"
	"github.com/forumbit/rental_platform/internal/repository"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestListingsCRUD(t *testing.T) {
	db := listingsTestDB(t)
	var ownerID, otherOwnerID int64
	for i, id := range []*int64{&ownerID, &otherOwnerID} {
		err := db.QueryRowContext(t.Context(), `
			INSERT INTO users (email, password_hash, full_name, email_verified_at)
			VALUES ($1, 'unused-test-hash', 'Test Owner', NOW()) RETURNING id`,
			fmt.Sprintf("owner%d@example.test", i),
		).Scan(id)
		if err != nil {
			t.Fatal(err)
		}
	}
	router, tokens := authTestRouter(t, db)
	ownerToken := createTestSession(t, db, tokens, ownerID).AccessToken
	otherOwnerToken := createTestSession(t, db, tokens, otherOwnerID).AccessToken

	empty := request(t, router, "GET", "/listings", "", 200)
	if strings.TrimSpace(empty.Body.String()) != `{"items":[],"next_cursor":null}` {
		t.Fatalf("empty list = %s", empty.Body.String())
	}

	input := model.ListingInput{
		Title: "Уютный загородный дом", Description: "Дом с террасой и видом на лес",
		PropertyType: "cottage", PriceMinor: 750000, Currency: "RUB", PricePeriod: "day",
		Country: "Россия", Region: ptr("Московская область"), City: "Истра",
		Address: ptr("ул. Лесная, д. 15"), AreaSqm: ptr(120.50), Bedrooms: ptr(0),
		Bathrooms: ptr(2), MaxGuests: ptr(6), Status: "published",
	}
	createdResponse := request(t, router, "POST", "/listings", jsonBody(t, input), 201, ownerToken)
	created := responseBody[model.Listing](t, createdResponse)
	assertListing(t, created, ownerID, input)
	if created.ID <= 0 || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("missing generated fields: %+v", created)
	}
	if created.PublishedAt == nil {
		t.Fatal("published listing has no published_at")
	}
	listingURL := fmt.Sprintf("/listings/%d", created.ID)
	ownURL := "/me" + listingURL
	if createdResponse.Header().Get("Location") != listingURL {
		t.Fatalf("Location = %q, want %q", createdResponse.Header().Get("Location"), listingURL)
	}
	fetched := responseBody[model.Listing](t, request(t, router, "GET", listingURL, "", 200))
	assertListing(t, fetched, ownerID, input)
	if fetched.ID != created.ID || !fetched.CreatedAt.Equal(created.CreatedAt) || !fetched.PublishedAt.Equal(*created.PublishedAt) {
		t.Fatalf("GET returned different generated fields: %+v", fetched)
	}

	// Multibyte titles use PostgreSQL's character limit; money remains exact above 2^53.
	minimal := map[string]any{
		"title": strings.Repeat("я", 200), "description": "Квартира",
		"property_type": "apartment", "price_minor": int64(9007199254740993), "city": "Москва",
	}
	minimalJSON := jsonBody(t, minimal)
	secondResponse := request(t, router, "POST", "/listings", minimalJSON, 201, ownerToken)
	second := responseBody[model.Listing](t, secondResponse)
	defaults := model.ListingInput{
		Title: strings.Repeat("я", 200), Description: "Квартира",
		PropertyType: "apartment", PriceMinor: 9007199254740993, City: "Москва",
		Currency: "RUB", PricePeriod: "day", Country: "Россия", Status: "draft",
	}
	assertListing(t, second, ownerID, defaults)
	for _, field := range []string{"region", "address", "area_sqm", "bedrooms", "bathrooms", "max_guests", "published_at"} {
		if !strings.Contains(secondResponse.Body.String(), `"`+field+`":null`) {
			t.Errorf("expected %s to be null: %s", field, secondResponse.Body.String())
		}
	}
	minimal["title"] = strings.Repeat("я", 201)
	request(t, router, "POST", "/listings", jsonBody(t, minimal), 400, ownerToken)

	// Drafts are hidden from the public API and from other users, but visible to the author.
	secondURL := fmt.Sprintf("/listings/%d", second.ID)
	request(t, router, "GET", secondURL, "", 404)
	request(t, router, "GET", "/me"+secondURL, "", 404, otherOwnerToken)
	ownDraft := responseBody[model.Listing](t, request(t, router, "GET", "/me"+secondURL, "", 200, ownerToken))
	assertListing(t, ownDraft, ownerID, defaults)
	request(t, router, "GET", ownURL, "", 200, ownerToken)
	request(t, router, "GET", "/me/listings/999999", "", 404, ownerToken)

	third := responseBody[model.Listing](t, request(t, router, "POST", "/listings", jsonBody(t, input), 201, ownerToken))
	foreign := responseBody[model.Listing](t, request(t, router, "POST", "/listings", jsonBody(t, input), 201, otherOwnerToken))

	for _, page := range []struct {
		target string
		token  string
		ids    []int64
		next   *int64
	}{
		{"/listings", "", []int64{foreign.ID, third.ID, created.ID}, nil},
		{"/listings?limit=2", "", []int64{foreign.ID, third.ID}, &third.ID},
		{fmt.Sprintf("/listings?limit=2&cursor=%d", third.ID), "", []int64{created.ID}, nil},
		{fmt.Sprintf("/listings?cursor=%d", created.ID), "", []int64{}, nil},
		{"/me/listings", ownerToken, []int64{third.ID, second.ID, created.ID}, nil},
		{"/me/listings?limit=1", ownerToken, []int64{third.ID}, &third.ID},
		{fmt.Sprintf("/me/listings?limit=1&cursor=%d", third.ID), ownerToken, []int64{second.ID}, &second.ID},
		{"/me/listings?status=draft", ownerToken, []int64{second.ID}, nil},
		{"/me/listings?status=published", ownerToken, []int64{third.ID, created.ID}, nil},
		{"/me/listings?status=archived", ownerToken, []int64{}, nil},
		{"/me/listings", otherOwnerToken, []int64{foreign.ID}, nil},
	} {
		var tokens []string
		if page.token != "" {
			tokens = append(tokens, page.token)
		}
		body := responseBody[model.ListingPage](t, request(t, router, "GET", page.target, "", 200, tokens...))
		ids := make([]int64, 0, len(body.Items))
		for _, listing := range body.Items {
			ids = append(ids, listing.ID)
		}
		if !reflect.DeepEqual(ids, page.ids) || !reflect.DeepEqual(body.NextCursor, page.next) {
			t.Fatalf("page %q: ids = %v next = %v, want %v next = %v", page.target, ids, body.NextCursor, page.ids, page.next)
		}
	}

	updatedInput := model.ListingInput{
		Title: "Дом у озера", Description: "Обновлённое описание",
		PropertyType: "house", PriceMinor: 850000, Currency: "USD", PricePeriod: "month",
		Country: "Беларусь", Region: ptr("Минская область"), City: "Минск",
		Address: ptr("ул. Озёрная, д. 1"), AreaSqm: ptr(150.25), Bedrooms: ptr(3),
		Bathrooms: ptr(1), MaxGuests: ptr(8), Status: "published",
	}
	// Make timestamp advancement deterministic without sleeps.
	if _, err := db.ExecContext(t.Context(), `UPDATE listings SET updated_at = '2000-01-01T00:00:00Z' WHERE id = $1`, created.ID); err != nil {
		t.Fatal(err)
	}
	updated := responseBody[model.Listing](t, request(t, router, "PUT", listingURL, jsonBody(t, updatedInput), 200, ownerToken))
	assertListing(t, updated, ownerID, updatedInput)
	if updated.ID != created.ID || !updated.CreatedAt.Equal(created.CreatedAt) || updated.UpdatedAt.Before(created.UpdatedAt) {
		t.Fatalf("incorrect generated fields after PUT: %+v", updated)
	}
	// Staying published keeps the original publication time.
	if updated.PublishedAt == nil || !updated.PublishedAt.Equal(*created.PublishedAt) {
		t.Fatalf("published_at = %v, want %v", updated.PublishedAt, created.PublishedAt)
	}
	fetched = responseBody[model.Listing](t, request(t, router, "GET", listingURL, "", 200))
	assertListing(t, fetched, ownerID, updatedInput)

	request(t, router, "PUT", listingURL, minimalJSON, 403, otherOwnerToken)
	request(t, router, "DELETE", listingURL, "", 403, otherOwnerToken)
	// The SQL predicates enforce ownership even if a caller bypasses the service.
	listingsRepo := repository.NewListingRepository(db)
	if _, err := listingsRepo.Update(t.Context(), created.ID, otherOwnerID, updatedInput); !errors.Is(err, model.ErrListingNotFound) {
		t.Fatalf("repository allowed a foreign update: %v", err)
	}
	if err := listingsRepo.Delete(t.Context(), created.ID, otherOwnerID); !errors.Is(err, model.ErrListingNotFound) {
		t.Fatalf("repository allowed a foreign deletion: %v", err)
	}
	fetched = responseBody[model.Listing](t, request(t, router, "GET", listingURL, "", 200))
	assertListing(t, fetched, ownerID, updatedInput)

	// Archiving clears published_at and hides the listing; republishing sets a new time.
	archivedInput := updatedInput
	archivedInput.Status = "archived"
	archived := responseBody[model.Listing](t, request(t, router, "PUT", listingURL, jsonBody(t, archivedInput), 200, ownerToken))
	if archived.PublishedAt != nil {
		t.Fatalf("archived listing has published_at = %v", archived.PublishedAt)
	}
	request(t, router, "GET", listingURL, "", 404)
	request(t, router, "GET", ownURL, "", 200, ownerToken)
	republished := responseBody[model.Listing](t, request(t, router, "PUT", listingURL, jsonBody(t, updatedInput), 200, ownerToken))
	if republished.PublishedAt == nil {
		t.Fatal("republished listing has no published_at")
	}

	// PUT must clear old nullable values and restore defaults for omitted fields.
	replaced := responseBody[model.Listing](t, request(t, router, "PUT", listingURL, minimalJSON, 200, ownerToken))
	assertListing(t, replaced, ownerID, defaults)
	if !replaced.CreatedAt.Equal(created.CreatedAt) || replaced.PublishedAt != nil {
		t.Fatalf("PUT changed created_at or kept published_at: %+v", replaced)
	}

	deleted := request(t, router, "DELETE", listingURL, "", 204, ownerToken)
	if deleted.Body.Len() != 0 {
		t.Fatalf("DELETE returned a body: %s", deleted.Body.String())
	}
	request(t, router, "GET", ownURL, "", 404, ownerToken)
	request(t, router, "PUT", listingURL, minimalJSON, 404, ownerToken)
	request(t, router, "DELETE", listingURL, "", 404, ownerToken)
	for _, id := range []int64{second.ID, third.ID} {
		request(t, router, "DELETE", fmt.Sprintf("/listings/%d", id), "", 204, ownerToken)
	}
	request(t, router, "DELETE", fmt.Sprintf("/listings/%d", foreign.ID), "", 204, otherOwnerToken)
	empty = request(t, router, "GET", "/listings", "", 200)
	if strings.TrimSpace(empty.Body.String()) != `{"items":[],"next_cursor":null}` {
		t.Fatalf("list after deletions = %s", empty.Body.String())
	}
}

func TestCleanupDeletesOnlyStaleAuthData(t *testing.T) {
	db := listingsTestDB(t)
	user := seedAuthTestUser(t, db)
	for _, session := range []struct {
		id      string
		expires string
		revoked any
	}{
		{strings.Repeat("a", 32), "NOW() + INTERVAL '1 day'", nil},                             // active
		{strings.Repeat("b", 32), "NOW() - INTERVAL '2 days'", nil},                            // long expired
		{strings.Repeat("c", 32), "NOW() - INTERVAL '1 hour'", nil},                            // within retention
		{strings.Repeat("d", 32), "NOW() + INTERVAL '1 day'", time.Now().Add(-48 * time.Hour)}, // long revoked
		{strings.Repeat("e", 32), "NOW() + INTERVAL '1 day'", time.Now().Add(-time.Hour)},      // recently revoked
	} {
		hash := sha256.Sum256([]byte(session.id))
		if _, err := db.ExecContext(t.Context(), `
			INSERT INTO refresh_sessions (id, user_id, token_hash, expires_at, revoked_at)
			VALUES ($1, $2, $3, `+session.expires+`, $4)`, session.id, user.ID, hash[:], session.revoked); err != nil {
			t.Fatal(err)
		}
	}
	unverified, err := repository.NewUserRepository(db).CreateUnverified(t.Context(), "stale@example.test", "unused-test-hash", "Stale")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("verification"))
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO email_verifications (user_id, id, token_hash, code_hash, expires_at)
		VALUES ($1, $2, $3, $3, NOW() - INTERVAL '2 days')`, unverified.ID, strings.Repeat("f", 32), hash[:]); err != nil {
		t.Fatal(err)
	}

	sessions, verifications, err := repository.NewCleanupRepository(db).DeleteExpired(t.Context(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if sessions != 2 || verifications != 1 {
		t.Fatalf("deleted %d sessions and %d verifications, want 2 and 1", sessions, verifications)
	}
	var remaining []string
	rows, err := db.QueryContext(t.Context(), `SELECT LEFT(id, 1) FROM refresh_sessions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		remaining = append(remaining, id)
	}
	if !reflect.DeepEqual(remaining, []string{"a", "c", "e"}) {
		t.Fatalf("remaining sessions = %v, want [a c e]", remaining)
	}
}

func listingsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { admin.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	schema := fmt.Sprintf("listings_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})

	cfg = cfg.Copy()
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { db.Close() })
	// Glob returns names sorted, which matches the numeric migration order.
	names, err := filepath.Glob(filepath.Join("..", "..", "migrations", "postgres-migs", "*.up.sql"))
	if err != nil || len(names) == 0 {
		t.Fatalf("find migrations: %v", err)
	}
	for _, name := range names {
		migration, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return db
}

func request(t *testing.T, router http.Handler, method, target, body string, status int, accessToken ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if len(accessToken) > 0 {
		req.Header.Set("Authorization", "Bearer "+accessToken[0])
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != status {
		t.Fatalf("%s %s: status = %d, want %d; body = %s", method, target, response.Code, status, response.Body.String())
	}
	return response
}

func jsonBody(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func responseBody[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var body T
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, response.Body.String())
	}
	return body
}

func ptr[T any](value T) *T {
	return &value
}

func assertListing(t *testing.T, got model.Listing, ownerID int64, want model.ListingInput) {
	t.Helper()
	if got.OwnerID != ownerID {
		t.Fatalf("owner_id = %d, want %d", got.OwnerID, ownerID)
	}
	if !reflect.DeepEqual(got.ListingInput, want) {
		t.Fatalf("listing input = %+v, want %+v", got.ListingInput, want)
	}
}
