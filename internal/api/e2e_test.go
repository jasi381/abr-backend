package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasi381/abr-backend/internal/api"
	"github.com/jasi381/abr-backend/internal/config"
	"github.com/jasi381/abr-backend/internal/events"
	"github.com/jasi381/abr-backend/internal/storage"
	"github.com/jasi381/abr-backend/internal/store"
	"github.com/jasi381/abr-backend/internal/worker"
)

// TestUploadToPlayableHLS runs the whole backend with local storage and real
// ffmpeg: upload → live events → ready → fetch master, playlist, chunk → delete.
func TestUploadToPlayableHLS(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	dir := t.TempDir()
	sample := filepath.Join(dir, "clip.mp4")
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "8", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", sample)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("make sample: %v\n%s", err, out)
	}

	cfg := config.Config{
		DataDir: filepath.Join(dir, "data"), Storage: "local", LocalStorageDir: filepath.Join(dir, "bucket"),
		MaxUploadBytes: 1 << 30, Workers: 1, FFmpeg: "ffmpeg", FFprobe: "ffprobe",
		Preset: "ultrafast", SegmentSeconds: 4, KeyframeSeconds: 2,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(cfg.DataDir, "videos.json"))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := storage.NewLocal(cfg.LocalStorageDir)
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wk := worker.New(cfg, st, blob, hub, log)
	wk.Start(ctx)
	srv := api.New(cfg, st, blob, wk, hub, log)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer srv.CloseStreams()

	// Upload.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("title", "Test clip")
	fw, _ := mw.CreateFormFile("file", "clip.mp4")
	f, _ := os.Open(sample)
	_, _ = io.Copy(fw, f)
	f.Close()
	mw.Close()
	resp, err := http.Post(ts.URL+"/videos", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	var created struct{ ID, Status, Title string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || created.ID == "" || created.Title != "Test clip" {
		t.Fatalf("upload: status %d, body %+v", resp.StatusCode, created)
	}

	// Follow the event stream until it ends.
	evResp, err := http.Get(ts.URL + "/videos/" + created.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	if ct := evResp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("events content type = %q", ct)
	}
	seen := map[string]int{}
	var last string
	var final []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(evResp.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if name, ok := strings.CutPrefix(line, "event: "); ok {
				seen[name]++
				last = name
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok && (last == "ready" || last == "failed") {
				final = []byte(data)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("timed out waiting for the video to finish")
	}
	evResp.Body.Close()
	if last != "ready" {
		t.Fatalf("stream ended with %q: %s (events %v)", last, final, seen)
	}
	if seen["snapshot"] != 1 || seen["step"] == 0 || seen["progress"] == 0 {
		t.Errorf("expected snapshot, step and progress events, got %v", seen)
	}

	var ready struct {
		Status       string
		MasterURL    string `json:"masterUrl"`
		ThumbnailURL string `json:"thumbnailUrl"`
		Renditions   []store.Rendition
	}
	if err := json.Unmarshal(final, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Status != "ready" || len(ready.Renditions) != 2 || !strings.HasPrefix(ready.MasterURL, ts.URL+"/hls/") {
		t.Fatalf("ready payload = %+v", ready)
	}

	get := func(url string) (int, string, string) {
		r, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, r.Header.Get("Content-Type"), string(b)
	}

	code, ct, master := get(ready.MasterURL)
	if code != 200 || ct != "application/vnd.apple.mpegurl" ||
		!strings.Contains(master, "720p/index.m3u8") || !strings.Contains(master, "480p/index.m3u8") {
		t.Fatalf("master: %d %s\n%s", code, ct, master)
	}
	base := strings.TrimSuffix(ready.MasterURL, "master.m3u8")
	code, _, media := get(base + "480p/index.m3u8")
	if code != 200 || !strings.Contains(media, "seg_0000.ts") || !strings.Contains(media, "#EXT-X-ENDLIST") {
		t.Fatalf("media playlist: %d\n%s", code, media)
	}
	if code, ct, seg := get(base + "480p/seg_0000.ts"); code != 200 || ct != "video/mp2t" || len(seg) == 0 {
		t.Fatalf("chunk: %d %s %d bytes", code, ct, len(seg))
	}
	if code, ct, _ := get(ready.ThumbnailURL); code != 200 || ct != "image/jpeg" {
		t.Fatalf("thumbnail: %d %s", code, ct)
	}

	// Delete removes everything.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/videos/"+created.ID, nil)
	dr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	dr.Body.Close()
	if dr.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d", dr.StatusCode)
	}
	if code, _, _ := get(ready.MasterURL); code != http.StatusNotFound {
		t.Errorf("master after delete: %d", code)
	}
	if code, _, _ := get(ts.URL + "/videos/" + created.ID); code != http.StatusNotFound {
		t.Errorf("video after delete: %d", code)
	}
}

func TestUploadRejectsWrongType(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, Storage: "local", LocalStorageDir: filepath.Join(dir, "b"), MaxUploadBytes: 1 << 20, Workers: 1}
	st, _ := store.Open(filepath.Join(dir, "videos.json"))
	blob, _ := storage.NewLocal(cfg.LocalStorageDir)
	srv := api.New(cfg, st, blob, nil, events.NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "notes.txt")
	_, _ = fw.Write([]byte("hello"))
	mw.Close()
	resp, err := http.Post(ts.URL+"/videos", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", resp.StatusCode)
	}
}
