package media

import (
	"fmt"
	"math"
)

// Rung is one entry of the bitrate ladder.
type Rung struct {
	Name      string
	Short     int // short side in pixels
	VideoKbps int // target average video bitrate
	MaxKbps   int // VBV max rate: caps spikes so chunk sizes stay predictable
	BufKbps   int // VBV buffer size
	AudioKbps int
}

// Ladder is the default 3-step ladder, highest first.
var Ladder = []Rung{
	{Name: "1080p", Short: 1080, VideoKbps: 5000, MaxKbps: 5350, BufKbps: 7500, AudioKbps: 128},
	{Name: "720p", Short: 720, VideoKbps: 2800, MaxKbps: 2996, BufKbps: 4200, AudioKbps: 128},
	{Name: "480p", Short: 480, VideoKbps: 1400, MaxKbps: 1498, BufKbps: 2100, AudioKbps: 96},
}

// Rendition is a rung fitted to a specific source.
type Rendition struct {
	Rung
	Landscape bool
	FPS       float64 // output frame rate
	CapFPS    bool    // source was above 60 fps and gets capped
	GOP       int     // frames between keyframes
	Level     string  // H.264 level, e.g. "4.0"
}

// Plan picks the renditions for a source: every rung at or below the
// source's short side (never upscale). A source smaller than the lowest rung
// gets one rendition at its own size.
func Plan(src ProbeResult, keyframeSeconds int) []Rendition {
	fps := src.FPS
	capFPS := false
	if fps <= 0 {
		fps = 30
	}
	if fps > 60 {
		fps, capFPS = 60, true
	}
	gop := int(math.Round(fps * float64(keyframeSeconds)))
	if gop < 1 {
		gop = 1
	}

	short := src.ShortSide()
	var out []Rendition
	for _, r := range Ladder {
		if r.Short <= short {
			out = append(out, Rendition{Rung: r})
		}
	}
	if len(out) == 0 {
		lowest := Ladder[len(Ladder)-1]
		lowest.Short = short &^ 1 // even, as H.264 requires
		lowest.Name = fmt.Sprintf("%dp", lowest.Short)
		out = append(out, Rendition{Rung: lowest})
	}
	for i := range out {
		out[i].Landscape = src.Landscape()
		out[i].FPS = fps
		out[i].CapFPS = capFPS
		out[i].GOP = gop
		out[i].Level = levelFor(out[i].Short, fps)
	}
	return out
}

func levelFor(short int, fps float64) string {
	high := fps > 30
	switch {
	case short <= 480 && !high:
		return "3.0"
	case short <= 480, short <= 720 && !high:
		return "3.1"
	case short <= 720:
		return "3.2"
	case !high:
		return "4.0"
	default:
		return "4.2"
	}
}

var levelHex = map[string]string{"3.0": "1e", "3.1": "1f", "3.2": "20", "4.0": "28", "4.2": "2a"}

// Codecs is the RFC 6381 string for the master playlist's CODECS attribute:
// H.264 Main profile at this level, plus AAC-LC when there is audio.
func (r Rendition) Codecs(hasAudio bool) string {
	c := "avc1.4d40" + levelHex[r.Level]
	if hasAudio {
		c += ",mp4a.40.2"
	}
	return c
}

// VideoFilter scales the short side to the rung's size, keeping the aspect
// ratio (-2 = "whatever keeps the ratio, rounded to even").
func (r Rendition) VideoFilter() string {
	f := fmt.Sprintf("scale=-2:%d", r.Short)
	if !r.Landscape {
		f = fmt.Sprintf("scale=%d:-2", r.Short)
	}
	if r.CapFPS {
		f += ",fps=60"
	}
	// 8-bit 4:2:0 for H.264 Main; iPhone HDR/10-bit HEVC sources need this.
	return f + ",format=yuv420p"
}
