// Package media wraps ffprobe and ffmpeg: inspect a source, plan the quality
// ladder, transcode each rendition into HLS chunks, and write the master playlist.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// ProbeResult is what the pipeline needs to know about a file.
type ProbeResult struct {
	Width      int // as displayed (rotation applied)
	Height     int
	Rotation   int
	FPS        float64
	VideoCodec string
	HasAudio   bool
	Duration   float64 // seconds
}

// ShortSide is the smaller dimension: "1080p" means a 1080-pixel short side,
// for landscape (1920×1080) and portrait (1080×1920, i.e. reels) alike.
func (p ProbeResult) ShortSide() int {
	if p.Width < p.Height {
		return p.Width
	}
	return p.Height
}

// Landscape reports whether the video is wider than tall.
func (p ProbeResult) Landscape() bool { return p.Width >= p.Height }

type ffprobeOutput struct {
	Streams []struct {
		CodecType    string            `json:"codec_type"`
		CodecName    string            `json:"codec_name"`
		Width        int               `json:"width"`
		Height       int               `json:"height"`
		AvgFrameRate string            `json:"avg_frame_rate"`
		RFrameRate   string            `json:"r_frame_rate"`
		Duration     string            `json:"duration"`
		Tags         map[string]string `json:"tags"`
		SideData     []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// Probe runs ffprobe on path.
func Probe(ctx context.Context, ffprobe, path string) (ProbeResult, error) {
	cmd := exec.CommandContext(ctx, ffprobe,
		"-v", "error", "-print_format", "json", "-show_streams", "-show_format", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return ProbeResult{}, fmt.Errorf("ffprobe: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseProbe(stdout.Bytes())
}

func parseProbe(b []byte) (ProbeResult, error) {
	var out ffprobeOutput
	if err := json.Unmarshal(b, &out); err != nil {
		return ProbeResult{}, fmt.Errorf("parse ffprobe output: %w", err)
	}
	var r ProbeResult
	foundVideo := false
	var streamDuration float64
	for _, s := range out.Streams {
		switch s.CodecType {
		case "video":
			if foundVideo || s.Width == 0 || s.Height == 0 {
				continue // first real video stream only (skips cover art with 0 size)
			}
			foundVideo = true
			r.Width, r.Height, r.VideoCodec = s.Width, s.Height, s.CodecName
			r.FPS = parseRate(s.AvgFrameRate)
			if r.FPS == 0 {
				r.FPS = parseRate(s.RFrameRate)
			}
			r.Rotation = rotationOf(s.Tags, s.SideData)
			streamDuration, _ = strconv.ParseFloat(s.Duration, 64)
		case "audio":
			r.HasAudio = true
		}
	}
	if !foundVideo {
		return ProbeResult{}, fmt.Errorf("no video stream found")
	}
	if abs(r.Rotation)%180 == 90 {
		r.Width, r.Height = r.Height, r.Width // phone portrait clips are stored sideways
	}
	r.Duration, _ = strconv.ParseFloat(out.Format.Duration, 64)
	if r.Duration <= 0 {
		r.Duration = streamDuration
	}
	if r.Duration <= 0 {
		return ProbeResult{}, fmt.Errorf("could not read the video duration")
	}
	return r, nil
}

// parseRate turns "30000/1001" into 29.97.
func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return math.Round(n/d*1000) / 1000
}

func rotationOf(tags map[string]string, side []struct {
	Rotation float64 `json:"rotation"`
}) int {
	for _, sd := range side {
		if sd.Rotation != 0 {
			return int(math.Round(sd.Rotation))
		}
	}
	if v, err := strconv.Atoi(tags["rotate"]); err == nil {
		return v
	}
	return 0
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
