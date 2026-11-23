package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jasi381/abr-backend/internal/storage"
	"github.com/jasi381/abr-backend/internal/store"
)

var allowedExt = map[string]bool{".mp4": true, ".mov": true, ".m4v": true, ".mkv": true, ".webm": true}

// POST /videos  (multipart/form-data: file=<video>, title=<optional>)
//
// The file is streamed to a local spool file (never held in memory), copied
// to storage as raw/{id}/source.ext, and queued for processing. The response
// is 202 Accepted: processing happens in the background.
func (s *Server) handleCreateVideo(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data with a 'file' field")
		return
	}

	id := newID()
	var title, spool, ext string
	var size int64
	ok := false
	defer func() {
		if !ok && spool != "" {
			os.Remove(spool)
		}
	}()

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			s.uploadError(w, err)
			return
		}
		switch part.FormName() {
		case "title":
			b, _ := io.ReadAll(io.LimitReader(part, 200))
			if t := strings.TrimSpace(string(b)); t != "" {
				title = t
			}
		case "file":
			if spool != "" {
				part.Close()
				writeError(w, http.StatusBadRequest, "send one file per upload")
				return
			}
			name := filepath.Base(part.FileName())
			ext = strings.ToLower(filepath.Ext(name))
			if !allowedExt[ext] {
				part.Close()
				writeError(w, http.StatusUnsupportedMediaType, "upload an .mp4, .mov, .m4v, .mkv or .webm file")
				return
			}
			if title == "" {
				title = strings.TrimSuffix(name, filepath.Ext(name))
			}
			spool = s.cfg.SpoolPath(id, ext)
			if size, err = saveToFile(part, spool); err != nil {
				part.Close()
				s.uploadError(w, err)
				return
			}
		}
		part.Close()
	}

	if spool == "" {
		writeError(w, http.StatusBadRequest, "missing 'file' field")
		return
	}
	if size == 0 {
		writeError(w, http.StatusBadRequest, "uploaded file is empty")
		return
	}

	key := storage.SourceKey(id, ext)
	if err := s.blob.PutFile(r.Context(), key, spool, storage.ContentTypeFor(key), "private, max-age=0"); err != nil {
		s.log.Error("store upload", "id", id, "err", err)
		writeError(w, http.StatusBadGateway, "could not save the upload to storage")
		return
	}

	v := store.Video{ID: id, Title: title, Status: store.StatusQueued, SourceKey: key, SourceSize: size}
	if err := s.store.Create(v); err != nil {
		s.log.Error("create video", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "could not save the video record")
		return
	}
	ok = true // keep the spool file: the worker reads it instead of downloading again
	if s.jobs != nil {
		s.jobs.Enqueue(id)
	}
	v, _ = s.store.Get(id)
	s.log.Info("upload accepted", "id", id, "title", title, "bytes", size)
	writeJSON(w, http.StatusAccepted, s.view(r, v))
}

func (s *Server) uploadError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, "file is larger than the upload limit")
		return
	}
	s.log.Warn("upload failed", "err", err)
	writeError(w, http.StatusBadRequest, "upload was interrupted or malformed")
}

func saveToFile(r io.Reader, path string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// GET /videos
func (s *Server) handleListVideos(w http.ResponseWriter, r *http.Request) {
	list := s.store.List()
	out := make([]videoView, 0, len(list))
	for _, v := range list {
		out = append(out, s.view(r, v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"videos": out})
}

// GET /videos/{id}
func (s *Server) handleGetVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookup(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.view(r, v))
}

// DELETE /videos/{id} — stops any running job, then removes the source,
// every HLS file, and the record.
func (s *Server) handleDeleteVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if s.jobs != nil {
		s.jobs.Cancel(v.ID)
	}
	for _, prefix := range []string{storage.RawPrefix(v.ID), storage.HLSPrefix(v.ID)} {
		if err := s.blob.DeletePrefix(r.Context(), prefix); err != nil {
			s.log.Error("delete objects", "id", v.ID, "prefix", prefix, "err", err)
			writeError(w, http.StatusBadGateway, "could not delete files from storage; try again")
			return
		}
	}
	if err := s.store.Delete(v.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "could not delete the video record")
		return
	}
	os.Remove(s.cfg.SpoolPath(v.ID, filepath.Ext(v.SourceKey)))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (store.Video, bool) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "video not found")
		return store.Video{}, false
	}
	v, err := s.store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "video not found")
		return store.Video{}, false
	}
	return v, true
}
