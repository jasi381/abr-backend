// Package storage abstracts where uploaded sources and HLS output live:
// S3 in production, a local folder for offline development and tests.
package storage

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
)

// ErrNotFound is returned when a key does not exist.
var ErrNotFound = errors.New("object not found")

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Size        int64
	ContentType string
}

// Blob is the small slice of object storage the server needs.
type Blob interface {
	// PutFile uploads a local file to key.
	PutFile(ctx context.Context, key, localPath, contentType, cacheControl string) error
	// Get opens key for reading. The caller closes the reader.
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	// Download copies key into a local file.
	Download(ctx context.Context, key, localPath string) error
	// DeletePrefix removes every object whose key starts with prefix.
	DeletePrefix(ctx context.Context, prefix string) error
}

// Object key layout, in one place.
func SourceKey(id, ext string) string { return "raw/" + id + "/source" + ext }
func HLSPrefix(id string) string      { return "hls/" + id + "/" }
func RawPrefix(id string) string      { return "raw/" + id + "/" }

// ContentTypeFor picks the MIME type players expect for HLS files.
func ContentTypeFor(key string) string {
	switch strings.ToLower(path.Ext(key)) {
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".ts":
		return "video/mp2t"
	case ".m4s":
		return "video/iso.segment"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	default:
		return "application/octet-stream"
	}
}

// CacheControlFor: chunks never change once written, so a CDN can keep them
// for a year. Playlists get a short TTL in case a video is processed again.
func CacheControlFor(key string) string {
	switch strings.ToLower(path.Ext(key)) {
	case ".ts", ".m4s", ".jpg":
		return "public, max-age=31536000, immutable"
	case ".m3u8":
		return "public, max-age=300"
	default:
		return "private, max-age=0"
	}
}
