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
	"github.com/jasi381/abr-backend/internal/events"
	"github.com/jasi381/abr-backend/internal/storage"
	"github.com/jasi381/abr-backend/internal/storage/s3store"
	"github.com/jasi381/abr-backend/internal/store"
	"github.com/jasi381/abr-backend/internal/worker"
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
	hub := events.NewHub()

	// Workers get their own context so in-flight HTTP requests can finish
	// before ffmpeg is stopped.
	workCtx, stopWork := context.WithCancel(context.Background())
	defer stopWork()
	wk := worker.New(cfg, st, blob, hub, log)
	wk.Start(workCtx)
	if n := wk.Recover(); n > 0 {
		log.Info("re-queued unfinished videos", "count", n)
	}

	srv := api.New(cfg, st, blob, wk, hub, log)
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No Read/WriteTimeout: uploads and progress streams are long-lived.
	}
	httpSrv.RegisterOnShutdown(srv.CloseStreams)

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "storage", cfg.Storage, "workers", cfg.Workers)
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = httpSrv.Shutdown(shutdownCtx)
	stopWork() // kills running ffmpeg; those videos resume on next start
	wk.Wait()
	return err
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
