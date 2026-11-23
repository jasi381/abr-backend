// Package config loads server settings from environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds every knob the server reads at startup.
type Config struct {
	Addr    string // HTTP listen address, e.g. ":8080"
	DataDir string // local scratch space: video index, upload spool, ffmpeg work dirs

	// Storage backend: "s3" (default) or "local" (files under LocalStorageDir, handy offline).
	Storage         string
	LocalStorageDir string

	S3Bucket    string
	S3Region    string
	S3Endpoint  string // optional, for MinIO / LocalStack (e.g. http://localhost:9000)
	S3PathStyle bool   // MinIO needs path-style URLs

	// PublicHLSBaseURL, when set, is where players fetch HLS files from directly
	// (a CloudFront domain or a public bucket URL). When empty, the API proxies
	// them from storage under /hls/... on its own host.
	PublicHLSBaseURL string

	MaxUploadBytes int64
	Workers        int // videos processed at the same time

	FFmpeg          string
	FFprobe         string
	Preset          string // x264 preset: veryfast is a good speed/size trade for a POC
	SegmentSeconds  int    // length of each HLS chunk
	KeyframeSeconds int    // keyframe interval; must divide SegmentSeconds
}

// Load reads the environment and validates the result.
func Load() (Config, error) {
	c := Config{
		Addr:             env("ADDR", ":8080"),
		DataDir:          env("DATA_DIR", "./data"),
		Storage:          strings.ToLower(env("STORAGE", "s3")),
		LocalStorageDir:  env("LOCAL_STORAGE_DIR", ""),
		S3Bucket:         env("S3_BUCKET", ""),
		S3Region:         env("S3_REGION", env("AWS_REGION", "ap-south-1")),
		S3Endpoint:       env("S3_ENDPOINT", ""),
		S3PathStyle:      envBool("S3_PATH_STYLE", false),
		PublicHLSBaseURL: strings.TrimRight(env("PUBLIC_HLS_BASE_URL", ""), "/"),
		MaxUploadBytes:   int64(envInt("MAX_UPLOAD_MB", 2048)) << 20,
		Workers:          envInt("WORKERS", 1),
		FFmpeg:           env("FFMPEG_PATH", "ffmpeg"),
		FFprobe:          env("FFPROBE_PATH", "ffprobe"),
		Preset:           env("X264_PRESET", "veryfast"),
		SegmentSeconds:   envInt("SEGMENT_SECONDS", 6),
		KeyframeSeconds:  envInt("KEYFRAME_SECONDS", 2),
	}
	if c.LocalStorageDir == "" {
		c.LocalStorageDir = filepath.Join(c.DataDir, "storage")
	}
	return c, c.validate()
}

func (c Config) validate() error {
	switch c.Storage {
	case "s3":
		if c.S3Bucket == "" {
			return fmt.Errorf("S3_BUCKET is required when STORAGE=s3")
		}
	case "local":
	default:
		return fmt.Errorf("STORAGE must be s3 or local, got %q", c.Storage)
	}
	if c.Workers < 1 {
		return fmt.Errorf("WORKERS must be at least 1")
	}
	if c.KeyframeSeconds < 1 || c.SegmentSeconds < 1 || c.SegmentSeconds%c.KeyframeSeconds != 0 {
		return fmt.Errorf("SEGMENT_SECONDS (%d) must be a multiple of KEYFRAME_SECONDS (%d)", c.SegmentSeconds, c.KeyframeSeconds)
	}
	return nil
}

// SpoolPath is where an upload is kept on local disk until processing finishes.
func (c Config) SpoolPath(id, ext string) string {
	return filepath.Join(c.DataDir, "uploads", id+ext)
}

// WorkDir is the scratch folder ffmpeg writes one video's output into.
func (c Config) WorkDir(id string) string {
	return filepath.Join(c.DataDir, "work", id)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(env(key, "")); err == nil {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, err := strconv.ParseBool(env(key, "")); err == nil {
		return v
	}
	return def
}
