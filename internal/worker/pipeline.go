package worker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/jasi381/abr-backend/internal/events"
	"github.com/jasi381/abr-backend/internal/media"
	"github.com/jasi381/abr-backend/internal/store"
)

// process takes one video from "queued" to finished HLS output.
func (w *Worker) process(ctx context.Context, id string) error {
	v, err := w.store.Get(id)
	if err != nil {
		return err
	}
	work := w.cfg.WorkDir(id)
	if err := os.RemoveAll(work); err != nil { // leftovers from an interrupted run
		return err
	}
	defer os.RemoveAll(work) // chunks live in storage once published; on failure we retry from scratch
	hlsDir := filepath.Join(work, "hls")
	if err := os.MkdirAll(hlsDir, 0o755); err != nil {
		return err
	}

	// 1. Get the source onto local disk.
	src, err := w.localSource(ctx, v, work)
	if err != nil {
		return fmt.Errorf("fetch source: %w", err)
	}

	// 2. Inspect it and plan the ladder.
	if err := w.setStatus(id, store.StatusProbing); err != nil {
		return err
	}
	info, err := media.Probe(ctx, w.cfg.FFprobe, src)
	if err != nil {
		return err
	}
	plan := media.Plan(info, w.cfg.KeyframeSeconds)
	if err := w.advance(id, events.Step, func(v *store.Video) {
		v.Source = &store.SourceInfo{
			Width: info.Width, Height: info.Height, FPS: info.FPS,
			VideoCodec: info.VideoCodec, HasAudio: info.HasAudio, Duration: info.Duration,
		}
		v.Renditions = nil
		v.Progress = map[string]int{}
		for _, r := range plan {
			v.Renditions = append(v.Renditions, store.Rendition{Name: r.Name})
			v.Progress[r.Name] = 0
		}
		v.Status = store.StatusTranscoding
	}); err != nil {
		return err
	}

	// 3. Encode every rendition in parallel.
	if err := w.transcodeAll(ctx, id, src, info, plan, hlsDir); err != nil {
		return err
	}
	thumbAt := math.Min(1, info.Duration/2)
	if err := media.Thumbnail(ctx, w.cfg.FFmpeg, src, filepath.Join(hlsDir, "thumb.jpg"), thumbAt); err != nil {
		w.log.Warn("thumbnail skipped", "id", id, "err", err) // nice to have, not fatal
	}

	// 4. Measure the chunks and write master.m3u8.
	if err := w.setStatus(id, store.StatusPackaging); err != nil {
		return err
	}
	renditions, err := w.writeMaster(ctx, hlsDir, info, plan)
	if err != nil {
		return err
	}
	if err := w.advance(id, events.Step, func(v *store.Video) {
		v.Renditions = renditions
		v.Status = store.StatusPublishing
	}); err != nil {
		return err
	}

	// 5. Upload chunks, playlists and thumbnail to storage.
	masterKey, thumbKey, err := w.publish(ctx, id, hlsDir)
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	if err := w.advance(id, events.Ready, func(v *store.Video) {
		v.MasterKey = masterKey
		v.ThumbKey = thumbKey
		v.Status = store.StatusReady
		v.Error = ""
	}); err != nil {
		return err
	}

	// Done: local copies are no longer needed.
	os.Remove(w.cfg.SpoolPath(id, filepath.Ext(v.SourceKey)))
	return nil
}

// localSource prefers the spooled upload; after a restart on another machine
// (or once the spool was cleaned) it downloads the original from storage.
func (w *Worker) localSource(ctx context.Context, v store.Video, work string) (string, error) {
	ext := filepath.Ext(v.SourceKey)
	spool := w.cfg.SpoolPath(v.ID, ext)
	if _, err := os.Stat(spool); err == nil {
		return spool, nil
	}
	dst := filepath.Join(work, "source"+ext)
	if err := w.blob.Download(ctx, v.SourceKey, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// transcodeAll runs one ffmpeg per rendition at the same time. If one fails,
// the others are stopped.
func (w *Worker) transcodeAll(ctx context.Context, id, src string, info media.ProbeResult, plan []media.Rendition, hlsDir string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, len(plan))
	for _, r := range plan {
		wg.Add(1)
		go func(r media.Rendition) {
			defer wg.Done()
			err := media.Transcode(ctx, media.TranscodeOptions{
				FFmpeg:          w.cfg.FFmpeg,
				Input:           src,
				OutDir:          filepath.Join(hlsDir, r.Name),
				Rendition:       r,
				HasAudio:        info.HasAudio,
				Duration:        info.Duration,
				Preset:          w.cfg.Preset,
				SegmentSeconds:  w.cfg.SegmentSeconds,
				KeyframeSeconds: w.cfg.KeyframeSeconds,
			}, func(pct int) { w.setProgress(id, r.Name, pct) })
			if err != nil {
				errs <- err
				cancel()
			}
		}(r)
	}
	wg.Wait()
	close(errs)
	var first error
	for err := range errs {
		if first == nil || errors.Is(first, context.Canceled) {
			first = err // report the real failure, not the cancellations it caused
		}
	}
	if first == nil {
		return ctx.Err()
	}
	return first
}

// writeMaster measures each rendition's chunks, reads the exact output size
// from its first chunk, and writes master.m3u8.
func (w *Worker) writeMaster(ctx context.Context, hlsDir string, info media.ProbeResult, plan []media.Rendition) ([]store.Rendition, error) {
	var variants []media.Variant
	var out []store.Rendition
	for _, r := range plan {
		dir := filepath.Join(hlsDir, r.Name)
		bw, err := media.MeasureBandwidth(dir)
		if err != nil {
			return nil, fmt.Errorf("measure %s: %w", r.Name, err)
		}
		seg, err := media.Probe(ctx, w.cfg.FFprobe, filepath.Join(dir, bw.FirstSegment))
		if err != nil {
			return nil, fmt.Errorf("probe %s output: %w", r.Name, err)
		}
		variants = append(variants, media.Variant{
			URI:          r.Name + "/index.m3u8",
			Bandwidth:    bw.Peak,
			AvgBandwidth: bw.Average,
			Width:        seg.Width,
			Height:       seg.Height,
			FPS:          r.FPS,
			Codecs:       r.Codecs(info.HasAudio),
		})
		out = append(out, store.Rendition{
			Name: r.Name, Width: seg.Width, Height: seg.Height,
			Bandwidth: bw.Peak, AvgBandwidth: bw.Average,
		})
	}
	if err := os.WriteFile(filepath.Join(hlsDir, "master.m3u8"), media.MasterPlaylist(variants), 0o644); err != nil {
		return nil, err
	}
	return out, nil
}
