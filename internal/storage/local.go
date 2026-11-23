package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Local stores objects as files under a root folder. Keys map 1:1 to paths.
type Local struct {
	root string
}

// NewLocal creates the root folder if needed.
func NewLocal(root string) (*Local, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &Local{root: abs}, nil
}

// resolve turns a key into a path and refuses anything that escapes the root.
func (l *Local) resolve(key string) (string, error) {
	p := filepath.Join(l.root, filepath.FromSlash(key))
	if p != l.root && !strings.HasPrefix(p, l.root+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid key %q", key)
	}
	return p, nil
}

func (l *Local) PutFile(ctx context.Context, key, localPath, _, _ string) error {
	dst, err := l.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return copyFile(ctx, localPath, dst)
}

func (l *Local) Get(_ context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	p, err := l.resolve(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		f.Close()
		return nil, ObjectInfo{}, ErrNotFound
	}
	return f, ObjectInfo{Size: st.Size(), ContentType: ContentTypeFor(key)}, nil
}

func (l *Local) Download(ctx context.Context, key, localPath string) error {
	src, err := l.resolve(key)
	if err != nil {
		return err
	}
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	return copyFile(ctx, src, localPath)
}

func (l *Local) DeletePrefix(_ context.Context, prefix string) error {
	if !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("prefix %q must end with /", prefix)
	}
	p, err := l.resolve(prefix)
	if err != nil {
		return err
	}
	if p == l.root {
		return fmt.Errorf("refusing to delete the storage root")
	}
	return os.RemoveAll(p)
}

func copyFile(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
