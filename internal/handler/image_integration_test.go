package handler_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/forumbit/rental_platform/internal/model"
)

func TestListingImages(t *testing.T) {
	db := listingsTestDB(t)
	storage := newMemoryStorage()
	router, tokens := testRouter(t, db, &captureMailer{}, storage, imagesTestConfig())
	owner := seedAuthTestUser(t, db)
	ownerToken := createTestSession(t, db, tokens, owner.ID).AccessToken
	var otherID int64
	if err := db.QueryRowContext(t.Context(), `
		INSERT INTO users (email, password_hash, full_name, email_verified_at)
		VALUES ('other@example.test', 'unused-test-hash', 'Other', NOW()) RETURNING id`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	otherToken := createTestSession(t, db, tokens, otherID).AccessToken

	draft := `{"title":"Дом","description":"Дом у леса","property_type":"house","price_minor":10000,"city":"Истра"}`
	listing := responseBody[model.Listing](t, request(t, router, "POST", "/listings", draft, 201, ownerToken))
	if listing.Images == nil || len(listing.Images) != 0 {
		t.Fatalf("new listing images = %#v, want []", listing.Images)
	}
	imagesURL := fmt.Sprintf("/listings/%d/images", listing.ID)

	pngData := encodePNG(t, 40, 30)
	first := responseBody[model.ListingImage](t, upload(t, router, imagesURL, pngData, 201, ownerToken))
	if first.ContentType != "image/png" || first.Width != 40 || first.Height != 30 ||
		first.SizeBytes != int64(len(pngData)) || first.Position != 0 || !strings.HasPrefix(first.URL, "/images/") ||
		!strings.HasSuffix(first.URL, ".png") || first.ID <= 0 {
		t.Fatalf("unexpected image: %+v", first)
	}
	second := responseBody[model.ListingImage](t, upload(t, router, imagesURL, encodeJPEG(t, 8, 6), 201, ownerToken))
	third := responseBody[model.ListingImage](t, upload(t, router, imagesURL, decodeWebP(t), 201, ownerToken))
	if second.ContentType != "image/jpeg" || second.Position != 1 || third.ContentType != "image/webp" ||
		third.Width != 1 || third.Height != 1 || third.Position != 2 {
		t.Fatalf("unexpected images: %+v %+v", second, third)
	}
	// The limit is 3 per listing.
	upload(t, router, imagesURL, pngData, 409, ownerToken)
	if len(storage.keys()) != 3 {
		t.Fatalf("storage has %d objects, want 3", len(storage.keys()))
	}

	// Files are served with their type and long-lived caching.
	served := request(t, router, "GET", first.URL, "", 200)
	if !bytes.Equal(served.Body.Bytes(), pngData) || served.Header().Get("Content-Type") != "image/png" ||
		served.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(served.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("unexpected served image: %v", served.Header())
	}

	// Draft images are visible to the owner; the public API hides the draft.
	listingURL := fmt.Sprintf("/listings/%d", listing.ID)
	request(t, router, "GET", listingURL, "", 404)
	own := responseBody[model.Listing](t, request(t, router, "GET", "/me"+listingURL, "", 200, ownerToken))
	assertImageIDs(t, own.Images, first.ID, second.ID, third.ID)

	published := strings.Replace(draft, `"city":"Истра"`, `"city":"Истра","status":"published"`, 1)
	updated := responseBody[model.Listing](t, request(t, router, "PUT", listingURL, published, 200, ownerToken))
	assertImageIDs(t, updated.Images, first.ID, second.ID, third.ID)
	public := responseBody[model.Listing](t, request(t, router, "GET", listingURL, "", 200))
	assertImageIDs(t, public.Images, first.ID, second.ID, third.ID)
	page := responseBody[model.ListingPage](t, request(t, router, "GET", "/listings", "", 200))
	if len(page.Items) != 1 {
		t.Fatalf("public page has %d listings, want 1", len(page.Items))
	}
	assertImageIDs(t, page.Items[0].Images, first.ID, second.ID, third.ID)
	if page.Items[0].Images[0].URL != first.URL {
		t.Fatalf("image URL = %q, want %q", page.Items[0].Images[0].URL, first.URL)
	}

	// Only the owner can change images.
	upload(t, router, imagesURL, pngData, 403, otherToken)
	request(t, router, "DELETE", fmt.Sprintf("%s/%d", imagesURL, first.ID), "", 403, otherToken)
	upload(t, router, "/listings/999999/images", pngData, 404, ownerToken)

	// Deleting frees a slot; new images go to the end.
	request(t, router, "DELETE", fmt.Sprintf("%s/%d", imagesURL, second.ID), "", 204, ownerToken)
	request(t, router, "DELETE", fmt.Sprintf("%s/%d", imagesURL, second.ID), "", 404, ownerToken)
	request(t, router, "GET", second.URL, "", 404)
	fourth := responseBody[model.ListingImage](t, upload(t, router, imagesURL, pngData, 201, ownerToken))
	if fourth.Position != 3 {
		t.Fatalf("position = %d, want 3", fourth.Position)
	}
	// An image of another listing cannot be deleted through this one.
	otherListing := responseBody[model.Listing](t, request(t, router, "POST", "/listings", draft, 201, ownerToken))
	request(t, router, "DELETE", fmt.Sprintf("/listings/%d/images/%d", otherListing.ID, first.ID), "", 404, ownerToken)

	// A failed upload to storage leaves no database row.
	storage.mu.Lock()
	storage.failPut = true
	storage.mu.Unlock()
	upload(t, router, fmt.Sprintf("/listings/%d/images", otherListing.ID), pngData, 500, ownerToken)
	storage.mu.Lock()
	storage.failPut = false
	storage.mu.Unlock()
	own = responseBody[model.Listing](t, request(t, router, "GET", fmt.Sprintf("/me/listings/%d", otherListing.ID), "", 200, ownerToken))
	if len(own.Images) != 0 {
		t.Fatalf("failed upload created images: %+v", own.Images)
	}

	// Deleting the listing deletes its files.
	request(t, router, "DELETE", listingURL, "", 204, ownerToken)
	if keys := storage.keys(); len(keys) != 0 {
		t.Fatalf("storage still has %v", keys)
	}
	request(t, router, "GET", first.URL, "", 404)
}

func TestConcurrentImageUploadsRespectLimit(t *testing.T) {
	db := listingsTestDB(t)
	storage := newMemoryStorage()
	router, tokens := testRouter(t, db, &captureMailer{}, storage, imagesTestConfig())
	owner := seedAuthTestUser(t, db)
	token := createTestSession(t, db, tokens, owner.ID).AccessToken
	listing := responseBody[model.Listing](t, request(t, router, "POST", "/listings",
		`{"title":"Дом","description":"Дом","property_type":"house","price_minor":1,"city":"Истра"}`, 201, token))

	const attempts = 8
	statuses := make([]int, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/listings/%d/images", listing.ID),
				bytes.NewReader(multipartFile(t, "image", encodePNG(t, 4, 4))))
			req.Header.Set("Content-Type", multipartContentType)
			req.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			statuses[i] = response.Code
		})
	}
	wg.Wait()
	created := 0
	for _, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Fatalf("unexpected status %d in %v", status, statuses)
		}
	}
	if created != imagesTestConfig().MaxPerListing {
		t.Fatalf("created %d images, want %d (statuses %v)", created, imagesTestConfig().MaxPerListing, statuses)
	}
	// Rejected uploads must not leave files behind.
	if len(storage.keys()) != created {
		t.Fatalf("storage has %d objects, want %d", len(storage.keys()), created)
	}
}

func upload(t *testing.T, router http.Handler, target string, data []byte, status int, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, bytes.NewReader(multipartFile(t, "image", data)))
	req.Header.Set("Content-Type", multipartContentType)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != status {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("POST %s: status = %d, want %d; body = %s", target, response.Code, status, body)
	}
	return response
}

func assertImageIDs(t *testing.T, images []model.ListingImage, ids ...int64) {
	t.Helper()
	got := make([]int64, 0, len(images))
	for _, image := range images {
		got = append(got, image.ID)
	}
	if !slices.Equal(got, ids) {
		t.Fatalf("image ids = %v, want %v", got, ids)
	}
}
