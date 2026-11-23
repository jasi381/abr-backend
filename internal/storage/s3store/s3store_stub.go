//go:build nos3

// Build with -tags nos3 to compile without the AWS SDK (STORAGE=local only).
package s3store

import (
	"context"
	"errors"
	"io"

	"github.com/jasi381/abr-backend/internal/storage"
)

type Options struct {
	Bucket    string
	Region    string
	Endpoint  string
	PathStyle bool
}

type Store struct{}

func New(context.Context, Options) (*Store, error) {
	return nil, errors.New("built with -tags nos3: S3 support is not compiled in; use STORAGE=local")
}

func (*Store) PutFile(context.Context, string, string, string, string) error {
	return errors.New("s3 disabled")
}
func (*Store) Get(context.Context, string) (io.ReadCloser, storage.ObjectInfo, error) {
	return nil, storage.ObjectInfo{}, errors.New("s3 disabled")
}
func (*Store) Download(context.Context, string, string) error { return errors.New("s3 disabled") }
func (*Store) DeletePrefix(context.Context, string) error     { return errors.New("s3 disabled") }
