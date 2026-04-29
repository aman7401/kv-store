package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/aman7401/kv-store/internal/store"
)

// WriteNode is implemented by replication.Primary so this handler
// doesn't import the replication package (avoids circular deps).
type WriteNode interface {
	WriteSet(key, value string, ttl time.Duration)
	WriteDelete(key string)
}

// PrimaryHandler routes reads to the local store and writes through the
// replication-aware WriteNode so every mutation is appended to the log.
type PrimaryHandler struct {
	store     *store.Store
	writeNode WriteNode
}

func NewPrimary(s *store.Store, wn WriteNode) *PrimaryHandler {
	return &PrimaryHandler{store: s, writeNode: wn}
}

func (h *PrimaryHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/get", h.handleGet)
	mux.HandleFunc("/set", h.handleSet)
	mux.HandleFunc("/delete", h.handleDelete)
	mux.HandleFunc("/health", h.handleHealth)
}

func (h *PrimaryHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	value, err := h.store.Get(key)
	if err != nil {
		if errors.Is(err, store.ErrKeyNotFound) || errors.Is(err, store.ErrKeyExpired) {
			http.Error(w, "key not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": value})
}

func (h *PrimaryHandler) handleSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req setRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	// WriteSet applies to store AND appends to replication log
	h.writeNode.WriteSet(req.Key, req.Value, time.Duration(req.TTL)*time.Second)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *PrimaryHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	h.writeNode.WriteDelete(key)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *PrimaryHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"role":   "primary",
		"keys":   strconv.Itoa(h.store.Len()),
	})
}
