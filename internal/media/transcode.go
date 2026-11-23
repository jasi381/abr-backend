package media

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// TranscodeOptions describes one ffmpeg run that produces one rendition.
type TranscodeOptions struct {
	FFmpeg          string
	Input           string
	OutDir          string // receives index.m3u8 and seg_0000.ts, seg_0001.ts, ...
	Rendition       Rendition
	HasAudio        bool
	Duration        float64 // source duration, for progress percent
	Preset          string
	SegmentSeconds  int
	KeyframeSeconds int
}

// TranscodeArgs builds the ffmpeg command line. Kept separate so tests can
// check the flags that matter without running ffmpeg.
//
// The keyframe flags are what make quality switching clean: every rendition
// gets a keyframe at exactly the same timestamps (every KeyframeSeconds), and
// scene-cut keyframes are off, so chunk N of 1080p and chunk N of 480p cover
// the same moment and each starts with a full picture.
func TranscodeArgs(o TranscodeOptions) []string {
	r := o.Rendition
	args := []string{
		"-hide_banner", "-nostdin", "-y", "-loglevel", "error",
		"-i", o.Input,
		"-map", "0:v:0",
	}
	if o.HasAudio {
		args = append(args, "-map", "0:a:0")
	}
	args = append(args,
		"-vf", r.VideoFilter(),
		"-c:v", "libx264", "-preset", o.Preset, "-profile:v", "main", "-level", r.Level,
		"-b:v", kbps(r.VideoKbps), "-maxrate", kbps(r.MaxKbps), "-bufsize", kbps(r.BufKbps),
		"-g", strconv.Itoa(r.GOP), "-keyint_min", strconv.Itoa(r.GOP), "-sc_threshold", "0",
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", o.KeyframeSeconds),
	)
	if o.HasAudio {
		args = append(args, "-c:a", "aac", "-b:a", kbps(r.AudioKbps), "-ac", "2", "-ar", "48000")
	}
	args = append(args,
		"-f", "hls",
		"-hls_time", strconv.Itoa(o.SegmentSeconds),
		"-hls_playlist_type", "vod",
		"-hls_flags", "independent_segments",
		"-hls_segment_type", "mpegts",
		"-start_number", "0",
		"-hls_segment_filename", filepath.Join(o.OutDir, "seg_%04d.ts"),
		"-progress", "pipe:1", "-nostats",
		filepath.Join(o.OutDir, "index.m3u8"),
	)
	return args
}

func kbps(n int) string { return strconv.Itoa(n) + "k" }

// Transcode runs ffmpeg for one rendition, reporting whole-percent progress.
func Transcode(ctx context.Context, o TranscodeOptions, onProgress func(percent int)) error {
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, o.FFmpeg, TranscodeArgs(o)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &tailBuffer{max: 2048}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	ParseProgress(stdout, o.Duration, onProgress)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg %s: %v: %s", o.Rendition.Name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ParseProgress reads ffmpeg's -progress output (key=value lines) and calls fn
// each time the whole percentage goes up. out_time_ms is in microseconds
// despite its name, same as out_time_us.
func ParseProgress(r io.Reader, duration float64, fn func(int)) {
	last := -1
	emit := func(p int) {
		if p > 100 {
			p = 100
		}
		if p > last && fn != nil {
			last = p
			fn(p)
		}
	}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch key {
		case "out_time_us", "out_time_ms":
			us, err := strconv.ParseInt(val, 10, 64)
			if err != nil || us < 0 || duration <= 0 {
				continue
			}
			emit(int(float64(us) / 1e6 / duration * 100))
		case "progress":
			if val == "end" {
				emit(100)
			}
		}
	}
	_, _ = io.Copy(io.Discard, r) // drain so ffmpeg never blocks on a full pipe
}

// Thumbnail grabs one frame at second `at` as a 360px-tall JPEG.
func Thumbnail(ctx context.Context, ffmpeg, input, out string, at float64) error {
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-nostdin", "-y", "-loglevel", "error",
		"-ss", strconv.FormatFloat(at, 'f', 2, 64), "-i", input,
		"-frames:v", "1", "-vf", "scale=-2:360", "-q:v", "3", out)
	stderr := &tailBuffer{max: 1024}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("thumbnail: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// tailBuffer keeps the last max bytes written: enough of ffmpeg's stderr
// to explain a failure without holding the whole log.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
