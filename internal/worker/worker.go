// Package worker runs the processing pipeline in the background: one video
// per worker goroutine, with the renditions of that video encoded in parallel.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/jasi381/abr-backend/internal/config"
	"github.com/jasi381/abr-backend/internal/storage"
	"github.com/jasi381/abr-backend/internal/store"
)

// Worker owns the job queue.
type Worker struct {
	cfg   config.Config
	store *store.Store
	blob  storage.Blob
	log   *slog.Logger

	queue chan string
	ctx   context.Context

	mu      sync.Mutex
	running map[string]context.CancelFunc
	wg      sync.WaitGroup
}

// New creates a worker. Call Start before Enqueue.
func New(cfg config.Config, st *store.Store, blob storage.Blob, log *slog.Logger) *Worker {
	return &Worker{
		cfg:     cfg,
		store:   st,
		blob:    blob,
		log:     log,
		queue:   make(chan string, 256),
		running: map[string]context.CancelFunc{},
	}
}

// Start launches cfg.Workers goroutines. They stop when ctx is cancelled;
// a job cut short that way stays in its current status and is picked up
// again on the next start.
func (w *Worker) Start(ctx context.Context) {
	w.ctx = ctx
	for i := 0; i < w.cfg.Workers; i++ {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-w.queue:
					w.run(ctx, id)
				}
			}
		}()
	}
}

// Enqueue schedules a video. It never blocks the caller.
func (w *Worker) Enqueue(id string) {
	select {
	case w.queue <- id:
	default:
		go func() {
			select {
			case w.queue <- id:
			case <-w.ctx.Done():
			}
		}()
	}
}

// Cancel stops a running job (used when its video is deleted). The ffmpeg
// processes are killed through their context.
func (w *Worker) Cancel(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if cancel, ok := w.running[id]; ok {
		cancel()
	}
}

// Wait blocks until all worker goroutines have returned.
func (w *Worker) Wait() { w.wg.Wait() }

func (w *Worker) run(parent context.Context, id string) {
	ctx, cancel := context.WithCancel(parent)
	w.mu.Lock()
	w.running[id] = cancel
	w.mu.Unlock()
	defer func() {
		cancel()
		w.mu.Lock()
		delete(w.running, id)
		w.mu.Unlock()
	}()

	log := w.log.With("id", id)
	log.Info("job started")
	err := w.process(ctx, id)
	switch {
	case err == nil:
		log.Info("job finished")
	case errors.Is(err, store.ErrNotFound) || ctx.Err() != nil:
		log.Info("job stopped", "reason", err) // deleted, or server shutting down
	default:
		log.Error("job failed", "err", err)
		w.fail(id, err)
	}
}

func (w *Worker) fail(id string, err error) {
	_, _ = w.store.Update(id, func(v *store.Video) {
		v.Status = store.StatusFailed
		v.Error = err.Error()
	})
}

func (w *Worker) setStatus(id string, s store.Status) error {
	_, err := w.store.Update(id, func(v *store.Video) { v.Status = s })
	return err
}

func (w *Worker) setProgress(id, rendition string, pct int) {
	_, _ = w.store.Update(id, func(v *store.Video) {
		if v.Progress == nil {
			v.Progress = map[string]int{}
		}
		v.Progress[rendition] = pct
	})
}
