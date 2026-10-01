// Package objects reads the images people attach to what they tell an
// agent from the photo bucket (s3.*), the bucket the backend writes them
// to. Credentials follow the backend's rule (apps/control-plane/src/storage.ts):
// explicit keys when set (MinIO, versitygw), both of them; otherwise the EC2
// instance role through IMDSv2 — never the AWS environment chain. A custom
// endpoint is addressed path-style.
package objects

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Store reads objects. An interface so tests can stand in.
type Store interface {
	// Get returns an object's bytes, at most max of them; ErrNotFound when
	// there is no such object.
	Get(ctx context.Context, key string, max int64) ([]byte, error)
}

// ErrNotFound: the bucket has no object under the key.
var ErrNotFound = errors.New("no such object")

// ErrUnconfigured: no bucket is configured, so nothing was ever stored.
var ErrUnconfigured = errors.New("image storage is not configured (s3.bucket)")

// Config is the s3.* settings.
type Config struct {
	Bucket, Endpoint, Region, AccessKey, SecretKey string
}

// S3 is a Store over an S3-compatible bucket.
type S3 struct {
	bucket string
	client *s3.Client
}

// New returns a Store for cfg, or nil when no bucket is configured.
func New(cfg Config) (*S3, error) {
	if cfg.Bucket == "" {
		return nil, nil
	}
	if (cfg.AccessKey == "") != (cfg.SecretKey == "") {
		return nil, errors.New("s3.access_key and s3.secret_key are set together or not at all")
	}
	var creds aws.CredentialsProvider
	if cfg.AccessKey != "" {
		creds = credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")
	} else {
		creds = aws.NewCredentialsCache(ec2rolecreds.New())
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	client := s3.New(s3.Options{Region: region, Credentials: creds}, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		}
	})
	return &S3{bucket: cfg.Bucket, client: client}, nil
}

// Get reads an object whole, refusing one larger than max.
func (s *S3) Get(ctx context.Context, key string, max int64) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		var missing *types.NoSuchKey
		if errors.As(err, &missing) {
			return nil, ErrNotFound
		}
		// The SDK's error names the operation and status, never credentials.
		return nil, fmt.Errorf("reading an attachment from storage: %w", err)
	}
	defer out.Body.Close()
	b, err := io.ReadAll(io.LimitReader(out.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("reading an attachment from storage: %w", err)
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("an attachment in storage is larger than %d bytes", max)
	}
	return b, nil
}
