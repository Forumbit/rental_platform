package handler_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/internal/model"
)

// A 1x1 lossless WebP image.
const tinyWebP = "UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA=="

func imagesTestConfig() config.ImagesConfig {
	return config.ImagesConfig{MaxSizeBytes: 64 << 10, MaxPerListing: 3, MaxDimension: 1000}
}

func TestImageUploadValidation(t *testing.T) {
	router := unitTestRouter(t)
	cfg := imagesTestConfig()
	largePNG := encodePNG(t, 1001, 10)
	tests := []struct {
		name        string
		target      string
		body        []byte
		contentType string
		status      int
	}{
		{"invalid listing id", "/listings/abc/images", nil, "", 400},
		{"JSON instead of multipart", "/listings/1/images", []byte(`{}`), "application/json", 415},
		{"broken multipart", "/listings/1/images", []byte("garbage"), "multipart/form-data; boundary=x", 400},
		{"no file", "/listings/1/images", nil, "", 400},
		{"wrong field name", "/listings/1/images", multipartFile(t, "photo", encodePNG(t, 2, 2)), "", 400},
		{"two files", "/listings/1/images", multipartFiles(t, "image", encodePNG(t, 2, 2), encodePNG(t, 2, 2)), "", 400},
		{"empty file", "/listings/1/images", multipartFile(t, "image", []byte{}), "", 400},
		{"too large", "/listings/1/images", multipartFile(t, "image", bytes.Repeat([]byte{0xff}, int(cfg.MaxSizeBytes)+1)), "", 413},
		{"too large body", "/listings/1/images", multipartFile(t, "image", bytes.Repeat([]byte{0xff}, int(cfg.MaxSizeBytes)*3)), "", 413},
		{"text file", "/listings/1/images", multipartFile(t, "image", []byte("hello, I am an image")), "", 415},
		{"GIF", "/listings/1/images", multipartFile(t, "image", encodeGIF(t)), "", 415},
		{"PNG signature with garbage", "/listings/1/images", multipartFile(t, "image", []byte("\x89PNG\r\n\x1a\ngarbage")), "", 400},
		{"too wide", "/listings/1/images", multipartFile(t, "image", largePNG), "", 400},
		{"invalid image id", "/listings/1/images/abc", nil, "", 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := http.MethodPost
			if strings.Contains(tt.target, "/images/") {
				method = http.MethodDelete
			}
			req := httptest.NewRequest(method, tt.target, bytes.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer test-access-token")
			switch {
			case tt.contentType != "":
				req.Header.Set("Content-Type", tt.contentType)
			case tt.body != nil:
				req.Header.Set("Content-Type", multipartContentType)
			default:
				body := multipartEmpty(t)
				req = httptest.NewRequest(method, tt.target, bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer test-access-token")
				req.Header.Set("Content-Type", multipartContentType)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.status, response.Body.String())
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

func TestServeImageRejectsInvalidKeys(t *testing.T) {
	router := unitTestRouter(t)
	for _, key := range []string{
		"missing.png",
		"0123456789abcdef0123456789abcdef.gif",
		"0123456789ABCDEF0123456789ABCDEF.png",
		"..%2F..%2Fetc%2Fpasswd",
		"0123456789abcdef0123456789abcdef.png", // valid format, but absent
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/images/"+key, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", key, response.Code)
		}
	}
}

const multipartBoundary = "test-boundary-7MA4YWxkTrZu0gW"

var multipartContentType = "multipart/form-data; boundary=" + multipartBoundary

func multipartFile(t *testing.T, field string, data []byte) []byte {
	return multipartFiles(t, field, data)
}

func multipartFiles(t *testing.T, field string, files ...[]byte) []byte {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary(multipartBoundary); err != nil {
		t.Fatal(err)
	}
	for _, data := range files {
		part, err := writer.CreateFormFile(field, "photo.bin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func multipartEmpty(t *testing.T) []byte {
	return multipartFiles(t, "image")
}

func encodePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeGIF(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black}), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeWebP(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(tinyWebP)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// memoryStorage is an in-memory ImageStorage for tests.
type memoryStorage struct {
	mu      sync.Mutex
	objects map[string]memoryObject
	failPut bool
}

type memoryObject struct {
	data        []byte
	contentType string
}

func newMemoryStorage() *memoryStorage {
	return &memoryStorage{objects: make(map[string]memoryObject)}
}

func (s *memoryStorage) Put(_ context.Context, key, contentType string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failPut {
		return errDatabase
	}
	s.objects[key] = memoryObject{data: bytes.Clone(data), contentType: contentType}
	return nil
}

func (s *memoryStorage) Get(_ context.Context, key string) (*model.StoredObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	object, ok := s.objects[key]
	if !ok {
		return nil, model.ErrImageNotFound
	}
	return &model.StoredObject{
		Body: io.NopCloser(bytes.NewReader(object.data)), ContentType: object.contentType, Size: int64(len(object.data)),
	}, nil
}

func (s *memoryStorage) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func (s *memoryStorage) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		keys = append(keys, key)
	}
	return keys
}
