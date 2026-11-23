// Package events fans out pipeline updates to whoever is watching a video
// (the app's processing screen, over Server-Sent Events).
package events

import (
	"sync"
	"time"

	"github.com/jasi381/abr-backend/internal/store"
)

// Type of update.
type Type string

const (
	Step     Type = "step"     // status changed
	Progress Type = "progress" // a rendition's encode percent went up
	Ready    Type = "ready"    // published; Video has the keys for masterUrl
	Failed   Type = "failed"   // Video.Error says why
	Deleted  Type = "deleted"  // the video was removed
)

// Event is one update about one video.
type Event struct {
	Type      Type
	Video     store.Video // snapshot after the change (Step, Ready, Failed)
	Rendition string      // Progress only
	Percent   int         // Progress only
}

// Terminal events end a stream.
func (e Event) Terminal() bool { return e.Type == Ready || e.Type == Failed || e.Type == Deleted }

// Hub is an in-memory pub/sub keyed by video ID. One server process is
// enough for the POC; with several instances this becomes Redis pub/sub.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan Event]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[string]map[chan Event]struct{}{}} }

// Subscribe returns a channel of updates for id and a function to stop.
func (h *Hub) Subscribe(id string) (<-chan Event, func()) {
	ch := make(chan Event, 64)
	h.mu.Lock()
	if h.subs[id] == nil {
		h.subs[id] = map[chan Event]struct{}{}
	}
	h.subs[id][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[id], ch)
		if len(h.subs[id]) == 0 {
			delete(h.subs, id)
		}
		h.mu.Unlock()
	}
}

// Publish sends e to every subscriber of id. Progress updates are dropped for
// a subscriber that is not keeping up (the next one supersedes them); final
// events wait briefly so they are not lost.
func (h *Hub) Publish(id string, e Event) {
	if h == nil {
		return
	}
	h.mu.Lock()
	targets := make([]chan Event, 0, len(h.subs[id]))
	for ch := range h.subs[id] {
		targets = append(targets, ch)
	}
	h.mu.Unlock()
	for _, ch := range targets {
		if !e.Terminal() {
			select {
			case ch <- e:
			default:
			}
			continue
		}
		select {
		case ch <- e:
		case <-time.After(time.Second):
		}
	}
}
