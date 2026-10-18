package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/mithrel/ginkgo/internal/blobstore"
)

// Attachment payloads are opaque to the server; clients verify them after decryption.
func (s *Server) handleAttachment(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")
	id := strings.TrimPrefix(r.URL.Path, "/v1/attachments/")
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		data, err := s.blobs.Get(r.Context(), namespace, id)
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "attachment unavailable", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "attachment read failed", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, blobstore.MaxBytes)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "attachment exceeds limit", http.StatusRequestEntityTooLarge)
			return
		}
		if err := s.blobs.Put(r.Context(), namespace, id, data); err != nil {
			if errors.Is(err, blobstore.ErrConflict) {
				http.Error(w, "attachment already exists", http.StatusConflict)
				return
			}
			http.Error(w, "attachment write failed", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
