// Package store keeps the list of videos and their processing state.
package store

import "time"

// Status is where a video is in the pipeline.
type Status string

const (
	StatusQueued      Status = "queued"      // uploaded, waiting for a worker
	StatusProbing     Status = "probing"     // ffprobe is reading the source
	StatusTranscoding Status = "transcoding" // ffmpeg is making the renditions
	StatusPackaging   Status = "packaging"   // measuring chunks, writing master.m3u8
	StatusPublishing  Status = "publishing"  // uploading HLS files to storage
	StatusReady       Status = "ready"
	StatusFailed      Status = "failed"
)

// TotalSteps is the number of steps the app shows on the processing screen.
const TotalSteps = 6

// Step maps a status to the step number and label shown in the app.
// Step 1 ("Uploaded") is done by the time a video exists.
func (s Status) Step() (int, string) {
	switch s {
	case StatusQueued:
		return 2, "Queued"
	case StatusProbing:
		return 3, "Inspecting source"
	case StatusTranscoding:
		return 4, "Transcoding"
	case StatusPackaging:
		return 5, "Writing playlists"
	case StatusPublishing:
		return 6, "Publishing"
	case StatusReady:
		return TotalSteps, "Ready"
	default:
		return 0, "Failed"
	}
}

// Active reports whether a video still needs work.
func (s Status) Active() bool {
	switch s {
	case StatusQueued, StatusProbing, StatusTranscoding, StatusPackaging, StatusPublishing:
		return true
	}
	return false
}

// SourceInfo is what ffprobe found in the uploaded file.
type SourceInfo struct {
	Width      int     `json:"width"`  // as displayed, after rotation
	Height     int     `json:"height"` // as displayed, after rotation
	FPS        float64 `json:"fps"`
	VideoCodec string  `json:"videoCodec"`
	HasAudio   bool    `json:"hasAudio"`
	Duration   float64 `json:"durationSec"`
}

// Rendition is one quality level of the finished video.
type Rendition struct {
	Name         string `json:"name"` // "1080p", "720p", "480p"
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Bandwidth    int    `json:"bandwidth"`    // peak bits/s of any chunk
	AvgBandwidth int    `json:"avgBandwidth"` // average bits/s
}

// Video is one uploaded file and everything known about it.
type Video struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Status     Status         `json:"status"`
	Error      string         `json:"error,omitempty"`
	SourceKey  string         `json:"sourceKey"`
	SourceSize int64          `json:"sourceSize"`
	Source     *SourceInfo    `json:"source,omitempty"`
	Renditions []Rendition    `json:"renditions,omitempty"`
	Progress   map[string]int `json:"progress,omitempty"` // rendition name -> percent
	MasterKey  string         `json:"masterKey,omitempty"`
	ThumbKey   string         `json:"thumbKey,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
}

// clone returns a deep copy so callers never share maps or slices with the store.
func (v Video) clone() Video {
	if v.Source != nil {
		s := *v.Source
		v.Source = &s
	}
	if v.Renditions != nil {
		v.Renditions = append([]Rendition(nil), v.Renditions...)
	}
	if v.Progress != nil {
		p := make(map[string]int, len(v.Progress))
		for k, n := range v.Progress {
			p[k] = n
		}
		v.Progress = p
	}
	return v
}
