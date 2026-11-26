package storage_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/internal/model"
	"github.com/forumbit/rental_platform/internal/storage"
)

// TestS3Storage runs against a real S3-compatible server, e.g. the SeaweedFS
// container from docker-compose.yml.
func TestS3Storage(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set TEST_S3_ENDPOINT, TEST_S3_ACCESS_KEY and TEST_S3_SECRET_KEY to run S3 integration tests")
	}
	cfg := config.StorageConfig{
		Endpoint: endpoint,
		// A fixed name: in SeaweedFS every bucket reserves its own volumes.
		Bucket:    "storage-test",
		AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TEST_S3_SECRET_KEY"),
	}
	s3, err := storage.OpenS3(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// A second open must accept the existing bucket.
	if _, err := storage.OpenS3(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}

	key := fmt.Sprintf("%032x.png", time.Now().UnixNano())
	data := []byte("\x89PNG\r\n\x1a\nnot-a-real-image")
	if err := s3.Put(t.Context(), key, "image/png", data); err != nil {
		t.Fatal(err)
	}
	object, err := s3.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(object.Body)
	object.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(data) || object.ContentType != "image/png" || object.Size != int64(len(data)) {
		t.Fatalf("got %q %s %d", body, object.ContentType, object.Size)
	}

	if err := s3.Delete(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := s3.Get(t.Context(), key); !errors.Is(err, model.ErrImageNotFound) {
		t.Fatalf("Get after Delete: %v, want ErrImageNotFound", err)
	}
	if err := s3.Delete(t.Context(), key); err != nil {
		t.Fatalf("deleting a missing object must succeed: %v", err)
	}

	wrong := cfg
	wrong.SecretKey = "wrong-secret-key"
	if _, err := storage.OpenS3(t.Context(), wrong); err == nil {
		t.Fatal("OpenS3 accepted wrong credentials")
	}
}
