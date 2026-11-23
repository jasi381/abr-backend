// Package api is the HTTP layer: REST endpoints for the Android app.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/jasi381/abr-backend/internal/config"
	"github.com/jasi381/abr-backend/internal/storage"
	"github.com/jasi381/abr-backend/internal/store"
)

// Jobs is the slice of the worker the API needs.
type Jobs interface {
	Enqueue(id string)
	Cancel(id string)
}

// Server wires handlers to their dependencies.
type Server struct {
	cfg   config.Config
	store *store.Store
	blob  storage.Blob
	jobs  Jobs
	log   *slog.Logger
}

// New builds the server. jobs may be nil (uploads are then stored but not processed).
func New(cfg config.Config, st *store.Store, blob storage.Blob, jobs Jobs, log *slog.Logger) *Server {
	return &Server{cfg: cfg, store: st, blob: blob, jobs: jobs, log: log}
}

// Handler returns the routed, logged http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /videos", s.handleCreateVideo)
	mux.HandleFunc("GET /videos", s.handleListVideos)
	mux.HandleFunc("GET /videos/{id}", s.handleGetVideo)
	mux.HandleFunc("DELETE /videos/{id}", s.handleDeleteVideo)
	return s.logRequests(mux)
}

var idPattern = regexp.MustCompile(`^[a-f0-9]{16}$`)

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusRecorder captures the status code for logging. Unwrap lets
// http.ResponseController reach the real writer (needed for SSE flushing).
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"ms", time.Since(start).Milliseconds())
	})
}
