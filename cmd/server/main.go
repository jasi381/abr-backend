// Command server runs the ABR Lab backend: uploads, FFmpeg → HLS, and serving.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jasi381/abr-backend/internal/api"
	"github.com/jasi381/abr-backend/internal/config"
	"github.com/jasi381/abr-backend/internal/storage"
	"github.com/jasi381/abr-backend/internal/storage/s3store"
	"github.com/jasi381/abr-backend/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(filepath.Join(cfg.DataDir, "videos.json"))
	if err != nil {
		return err
	}
	blob, err := openBlob(ctx, cfg)
	if err != nil {
		return err
	}

	srv := api.New(cfg, st, blob, nil, log)
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No Read/WriteTimeout: uploads and progress streams are long-lived.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "storage", cfg.Storage)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

func openBlob(ctx context.Context, cfg config.Config) (storage.Blob, error) {
	if cfg.Storage == "local" {
		return storage.NewLocal(cfg.LocalStorageDir)
	}
	return s3store.New(ctx, s3store.Options{
		Bucket:    cfg.S3Bucket,
		Region:    cfg.S3Region,
		Endpoint:  cfg.S3Endpoint,
		PathStyle: cfg.S3PathStyle,
	})
}
