package api

import (
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/jasi381/abr-backend/internal/storage"
)

// GET /hls/{id}/{path...}
//
// Streams HLS files from storage. The URL path mirrors the storage key
// (hls/{id}/720p/seg_0003.ts), so the relative links inside the playlists just
// work. In production, set PUBLIC_HLS_BASE_URL to a CloudFront domain in front
// of the bucket and players skip this route entirely.
func (s *Server) handleHLS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(path.Clean("/"+r.PathValue("path")), "/")
	if rel == "" {
		http.NotFound(w, r)
		return
	}
	key := storage.HLSPrefix(id) + rel

	body, info, err := s.blob.Get(r.Context(), key)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.Error("hls get", "key", key, "err", err)
		writeError(w, http.StatusBadGateway, "could not read from storage")
		return
	}
	defer body.Close()

	ct := info.ContentType
	if ct == "" || ct == "application/octet-stream" || ct == "binary/octet-stream" {
		ct = storage.ContentTypeFor(key)
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", storage.CacheControlFor(key))
	h.Set("Access-Control-Allow-Origin", "*") // lets web players (hls.js) use it too
	if info.Size > 0 {
		h.Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, body); err != nil {
		s.log.Debug("hls copy interrupted", "key", key, "err", err) // player seeked away
	}
}

// hlsBase is where URLs to HLS files start: the CDN when configured,
// otherwise this server as the client reached it (so it works from an
// emulator at 10.0.2.2:8080 or a phone on the LAN without extra config).
func (s *Server) hlsBase(r *http.Request) string {
	if s.cfg.PublicHLSBaseURL != "" {
		return s.cfg.PublicHLSBaseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return scheme + "://" + host
}
