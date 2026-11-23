//go:build !nos3

// Package s3store implements storage.Blob on Amazon S3 (or any S3-compatible
// service such as MinIO) using the AWS SDK for Go v2.
package s3store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/jasi381/abr-backend/internal/storage"
)

// Options configures the S3 client.
type Options struct {
	Bucket    string
	Region    string
	Endpoint  string // optional custom endpoint (MinIO, LocalStack)
	PathStyle bool
}

// Store talks to one bucket. Credentials come from the default AWS chain:
// env vars, ~/.aws/credentials, or an IAM role on EC2/ECS.
type Store struct {
	client *s3.Client
	bucket string
}

// New builds the client. It does not make a network call.
func New(ctx context.Context, o Options) (*Store, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(o.Region))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	client := s3.NewFromConfig(cfg, func(so *s3.Options) {
		if o.Endpoint != "" {
			so.BaseEndpoint = aws.String(o.Endpoint)
		}
		so.UsePathStyle = o.PathStyle
	})
	return &Store{client: client, bucket: o.Bucket}, nil
}

// PutFile uploads with a single PutObject. Fine up to 5 GB, which covers the
// upload cap; chunks and playlists are tiny.
func (s *Store) PutFile(ctx context.Context, key, localPath, contentType, cacheControl string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          f,
		ContentLength: aws.Int64(st.Size()),
		ContentType:   aws.String(contentType),
		CacheControl:  aws.String(cacheControl),
	})
	if err != nil {
		return fmt.Errorf("put s3://%s/%s: %w", s.bucket, key, err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, storage.ObjectInfo{}, storage.ErrNotFound
		}
		return nil, storage.ObjectInfo{}, fmt.Errorf("get s3://%s/%s: %w", s.bucket, key, err)
	}
	info := storage.ObjectInfo{
		Size:        aws.ToInt64(out.ContentLength),
		ContentType: aws.ToString(out.ContentType),
	}
	if info.ContentType == "" {
		info.ContentType = storage.ContentTypeFor(key)
	}
	return out.Body, info, nil
}

func (s *Store) Download(ctx context.Context, key, localPath string) error {
	body, _, err := s.Get(ctx, key)
	if err != nil {
		return err
	}
	defer body.Close()
	tmp := localPath + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("download s3://%s/%s: %w", s.bucket, key, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, localPath)
}

// DeletePrefix lists every key under prefix and deletes them 1,000 at a time
// (the DeleteObjects limit).
func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list s3://%s/%s: %w", s.bucket, prefix, err)
		}
		if len(page.Contents) == 0 {
			continue
		}
		ids := make([]types.ObjectIdentifier, 0, len(page.Contents))
		for _, obj := range page.Contents {
			ids = append(ids, types.ObjectIdentifier{Key: obj.Key})
		}
		out, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("delete under s3://%s/%s: %w", s.bucket, prefix, err)
		}
		if len(out.Errors) > 0 {
			e := out.Errors[0]
			return fmt.Errorf("delete s3://%s/%s: %s", s.bucket, aws.ToString(e.Key), aws.ToString(e.Message))
		}
	}
	return nil
}

func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	return false
}
