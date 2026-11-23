package api

import (
	"net/http"
	"time"

	"github.com/jasi381/abr-backend/internal/store"
)

// videoView is the JSON shape the Android app reads.
type videoView struct {
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Status     store.Status      `json:"status"`
	Step       int               `json:"step"`
	TotalSteps int               `json:"totalSteps"`
	StepName   string            `json:"stepName"`
	Error      string            `json:"error,omitempty"`
	SizeBytes  int64             `json:"sizeBytes"`
	Source     *store.SourceInfo `json:"source,omitempty"`
	Renditions []store.Rendition `json:"renditions"`
	Progress   map[string]int    `json:"progress,omitempty"`
	// MasterURL is what the app hands to ExoPlayer. Set once status is "ready".
	MasterURL    string    `json:"masterUrl,omitempty"`
	ThumbnailURL string    `json:"thumbnailUrl,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (s *Server) view(r *http.Request, v store.Video) videoView {
	return s.viewWithBase(s.hlsBase(r), v)
}

func (s *Server) viewWithBase(base string, v store.Video) videoView {
	step, name := v.Status.Step()
	out := videoView{
		ID:         v.ID,
		Title:      v.Title,
		Status:     v.Status,
		Step:       step,
		TotalSteps: store.TotalSteps,
		StepName:   name,
		Error:      v.Error,
		SizeBytes:  v.SourceSize,
		Source:     v.Source,
		Renditions: v.Renditions,
		Progress:   v.Progress,
		CreatedAt:  v.CreatedAt,
	}
	if v.MasterKey != "" {
		out.MasterURL = base + "/" + v.MasterKey
	}
	if v.ThumbKey != "" {
		out.ThumbnailURL = base + "/" + v.ThumbKey
	}
	if out.Renditions == nil {
		out.Renditions = []store.Rendition{}
	}
	return out
}
