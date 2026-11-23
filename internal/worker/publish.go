package worker

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/jasi381/abr-backend/internal/storage"
	"github.com/jasi381/abr-backend/internal/store"
)

const uploadConcurrency = 8

// publish uploads everything under hlsDir to hls/{id}/... and returns the
// master and thumbnail keys. master.m3u8 goes up last, so a player can never
// read a master that points at chunks that are not there yet.
func (w *Worker) publish(ctx context.Context, id, hlsDir string) (masterKey, thumbKey string, err error) {
	prefix := storage.HLSPrefix(id)
	var files []string
	err = filepath.WalkDir(hlsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(hlsDir, p)
		if err != nil {
			return err
		}
		if rel != "master.m3u8" {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, uploadConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for _, rel := range files {
		wg.Add(1)
		go func(rel string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			key := prefix + rel
			if err := w.blob.PutFile(ctx, key, filepath.Join(hlsDir, filepath.FromSlash(rel)),
				storage.ContentTypeFor(key), storage.CacheControlFor(key)); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				cancel()
			}
		}(rel)
	}
	wg.Wait()
	if firstErr != nil {
		return "", "", firstErr
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}

	masterKey = prefix + "master.m3u8"
	if err := w.blob.PutFile(ctx, masterKey, filepath.Join(hlsDir, "master.m3u8"),
		storage.ContentTypeFor(masterKey), storage.CacheControlFor(masterKey)); err != nil {
		return "", "", err
	}
	if _, err := os.Stat(filepath.Join(hlsDir, "thumb.jpg")); err == nil {
		thumbKey = prefix + "thumb.jpg"
	}
	return masterKey, thumbKey, nil
}

// Recover puts videos that were mid-pipeline when the server stopped back in
// the queue. Their work folders are rebuilt from scratch.
func (w *Worker) Recover() int {
	n := 0
	for _, v := range w.store.List() {
		if !v.Status.Active() {
			continue
		}
		if _, err := w.store.Update(v.ID, func(v *store.Video) {
			v.Status = store.StatusQueued
			v.Progress = nil
		}); err != nil && !errors.Is(err, store.ErrNotFound) {
			w.log.Error("recover", "id", v.ID, "err", err)
			continue
		}
		w.Enqueue(v.ID)
		n++
	}
	return n
}
