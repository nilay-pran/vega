package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Store is the production ObjectStore backed by an S3-compatible service
// (DigitalOcean Spaces; MinIO in dev). It uses the minio Core API for explicit
// multipart control so a protocol chunk maps directly to a part.
type S3Store struct {
	core   *minio.Core
	bucket string
}

type S3Config struct {
	Endpoint  string // e.g. "nyc3.digitaloceanspaces.com"
	Region    string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

func NewS3Store(cfg S3Config) (*S3Store, error) {
	core, err := minio.NewCore(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: new client: %w", err)
	}
	return &S3Store{core: core, bucket: cfg.Bucket}, nil
}

func (s *S3Store) InitMultipart(ctx context.Context, key string) (string, error) {
	return s.core.NewMultipartUpload(ctx, s.bucket, key, minio.PutObjectOptions{})
}

func (s *S3Store) UploadPart(ctx context.Context, key, uploadID string, partNumber int, r io.Reader, size int64) (Part, error) {
	p, err := s.core.PutObjectPart(ctx, s.bucket, key, uploadID, partNumber, r, size, minio.PutObjectPartOptions{})
	if err != nil {
		return Part{}, err
	}
	return Part{PartNumber: partNumber, ETag: p.ETag}, nil
}

func (s *S3Store) CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error {
	complete := make([]minio.CompletePart, len(parts))
	for i, p := range parts {
		complete[i] = minio.CompletePart{PartNumber: p.PartNumber, ETag: p.ETag}
	}
	_, err := s.core.CompleteMultipartUpload(ctx, s.bucket, key, uploadID, complete, minio.PutObjectOptions{})
	return err
}

func (s *S3Store) AbortMultipart(ctx context.Context, key, uploadID string) error {
	return s.core.AbortMultipartUpload(ctx, s.bucket, key, uploadID)
}

// PresignUploadPart signs a PUT for one multipart part so the client can upload
// the bytes straight to S3/Spaces. partNumber and uploadID go into the signed
// query string, exactly as PutObjectPart would send them, so the resulting URL
// targets the same part within the same multipart upload. S3 returns the part's
// ETag in the response header, which the client threads back through
// CompleteMultipart.
func (s *S3Store) PresignUploadPart(ctx context.Context, key, uploadID string, partNumber int, expiry time.Duration) (PresignedPut, error) {
	q := url.Values{}
	q.Set("uploadId", uploadID)
	q.Set("partNumber", strconv.Itoa(partNumber))
	u, err := s.core.Presign(ctx, http.MethodPut, s.bucket, key, expiry, q)
	if err != nil {
		return PresignedPut{}, fmt.Errorf("s3: presign part: %w", err)
	}
	return PresignedPut{URL: u.String(), Method: http.MethodPut, Expires: time.Now().Add(expiry)}, nil
}

func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, _, _, err := s.core.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return rc, nil
}
