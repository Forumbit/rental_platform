package storage

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/forumbit/rental_platform/internal/config"
	"github.com/forumbit/rental_platform/internal/model"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 stores listing images in any S3-compatible service: SeaweedFS locally,
// or a cloud provider such as Cloudflare R2 or Yandex Object Storage.
type S3 struct {
	client *minio.Client
	bucket string
}

// OpenS3 connects to the storage and creates the bucket when it is missing.
// The bucket stays private: images are served through the API.
func OpenS3(ctx context.Context, cfg config.StorageConfig) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY and S3_SECRET_KEY must be set")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("check S3 bucket %q: %w", cfg.Bucket, err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			return nil, fmt.Errorf("create S3 bucket %q: %w", cfg.Bucket, err)
		}
	}
	return &S3{client: client, bucket: cfg.Bucket}, nil
}

func (s *S3) Put(ctx context.Context, key, contentType string, data []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("upload image %s: %w", key, err)
	}
	return nil
}

func (s *S3) Get(ctx context.Context, key string) (*model.StoredObject, error) {
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get image %s: %w", key, err)
	}
	// GetObject is lazy; Stat performs the request and reports a missing key.
	info, err := object.Stat()
	if err != nil {
		object.Close()
		if isNotFound(err) {
			return nil, model.ErrImageNotFound
		}
		return nil, fmt.Errorf("stat image %s: %w", key, err)
	}
	return &model.StoredObject{Body: object, ContentType: info.ContentType, Size: info.Size}, nil
}

// Delete succeeds when the object is already absent.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil && !isNotFound(err) {
		return fmt.Errorf("delete image %s: %w", key, err)
	}
	return nil
}

func isNotFound(err error) bool {
	response := minio.ToErrorResponse(err)
	return response.Code == "NoSuchKey" || response.StatusCode == http.StatusNotFound
}
