package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestStorePersistsAndCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "videos.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(Video{ID: "a1", Title: "First", Status: StatusQueued}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("a1", func(v *Video) {
		v.Status = StatusTranscoding
		v.Progress = map[string]int{"720p": 40}
	}); err != nil {
		t.Fatal(err)
	}

	// Callers get copies: changing one must not change the store.
	got, _ := s.Get("a1")
	got.Progress["720p"] = 99

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := reopened.Get("a1")
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusTranscoding || v.Progress["720p"] != 40 || v.CreatedAt.IsZero() {
		t.Errorf("reloaded video = %+v", v)
	}

	if err := reopened.Delete("a1"); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Update("a1", func(*Video) {}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update after delete: %v", err)
	}
}

func TestStatusSteps(t *testing.T) {
	if n, _ := StatusTranscoding.Step(); n != 4 {
		t.Errorf("transcoding step = %d", n)
	}
	if !StatusPublishing.Active() || StatusReady.Active() || StatusFailed.Active() {
		t.Error("Active() wrong")
	}
}
