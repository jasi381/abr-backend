package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ErrNotFound is returned for an unknown video ID.
var ErrNotFound = errors.New("video not found")

// Store is a small JSON-file-backed index of videos. It keeps everything in
// memory and rewrites the file atomically on each change, which is plenty for
// a POC. Swap it for SQLite or Postgres behind the same methods later.
type Store struct {
	mu     sync.RWMutex
	path   string
	videos map[string]Video
}

// Open loads the index from path, creating an empty one if it does not exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path, videos: map[string]Video{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Video
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	for _, v := range list {
		s.videos[v.ID] = v
	}
	return s, nil
}

// Create adds a new video.
func (s *Store) Create(v Video) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.videos[v.ID]; ok {
		return fmt.Errorf("video %s already exists", v.ID)
	}
	now := time.Now().UTC()
	v.CreatedAt, v.UpdatedAt = now, now
	s.videos[v.ID] = v.clone()
	return s.saveLocked()
}

// Get returns a copy of one video.
func (s *Store) Get(id string) (Video, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.videos[id]
	if !ok {
		return Video{}, ErrNotFound
	}
	return v.clone(), nil
}

// List returns all videos, newest first.
func (s *Store) List() []Video {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Video, 0, len(s.videos))
	for _, v := range s.videos {
		out = append(out, v.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Update applies fn to a video under the lock and saves it. It returns the
// updated copy, or ErrNotFound if the video was deleted meanwhile.
func (s *Store) Update(id string, fn func(*Video)) (Video, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.videos[id]
	if !ok {
		return Video{}, ErrNotFound
	}
	v = v.clone()
	fn(&v)
	v.UpdatedAt = time.Now().UTC()
	s.videos[id] = v
	return v.clone(), s.saveLocked()
}

// Delete removes a video from the index.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.videos[id]; !ok {
		return ErrNotFound
	}
	delete(s.videos, id)
	return s.saveLocked()
}

// saveLocked writes the whole index to a temp file and renames it into place,
// so a crash mid-write never leaves a half-written file.
func (s *Store) saveLocked() error {
	list := make([]Video, 0, len(s.videos))
	for _, v := range s.videos {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
