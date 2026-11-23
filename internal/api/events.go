package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jasi381/abr-backend/internal/events"
	"github.com/jasi381/abr-backend/internal/store"
)

// GET /videos/{id}/events — Server-Sent Events for the processing screen.
//
// The stream opens with a "snapshot" (the full video, same JSON as
// GET /videos/{id}), then sends:
//
//	event: step      data: {"status":"transcoding","step":4,"totalSteps":6,"stepName":"Transcoding"}
//	event: progress  data: {"rendition":"720p","percent":61}
//	event: ready     data: <full video, with masterUrl>     (stream ends)
//	event: failed    data: {"error":"..."}                  (stream ends)
//	event: deleted   data: {}                               (stream ends)
//
// A comment line is sent every 15 s so proxies and OkHttp keep it open. If the
// video is already ready or failed, the stream ends right after the snapshot.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	// Subscribe before reading the snapshot so no update falls in between.
	ch, unsubscribe := s.hub.Subscribe(id)
	defer unsubscribe()
	v, err := s.store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // stop nginx from buffering the stream
	w.WriteHeader(http.StatusOK)

	base := s.hlsBase(r)
	send := func(event string, data any) bool {
		b, err := json.Marshal(data)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	if !send("snapshot", s.viewWithBase(base, v)) || !v.Status.Active() {
		return
	}

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.closing:
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case ev := <-ch:
			ok := true
			switch ev.Type {
			case events.Progress:
				ok = send("progress", map[string]any{"rendition": ev.Rendition, "percent": ev.Percent})
			case events.Step:
				ok = send("step", stepPayload(ev.Video))
			case events.Ready:
				send("ready", s.viewWithBase(base, ev.Video))
				return
			case events.Failed:
				send("failed", map[string]string{"error": ev.Video.Error})
				return
			case events.Deleted:
				send("deleted", map[string]string{})
				return
			}
			if !ok {
				return
			}
		}
	}
}

func stepPayload(v store.Video) map[string]any {
	step, name := v.Status.Step()
	return map[string]any{"status": v.Status, "step": step, "totalSteps": store.TotalSteps, "stepName": name}
}

// CloseStreams ends every open event stream; call it when shutting down so
// http.Server.Shutdown does not wait on them.
func (s *Server) CloseStreams() {
	s.closeOnce.Do(func() { close(s.closing) })
}
