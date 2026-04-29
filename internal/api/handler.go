package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/aman7401/kv-store/internal/store"
	"github.com/aman7401/kv-store/internal/wal"
)

type Handler struct {
	store *store.Store
	wal   *wal.WAL
}

func New(s *store.Store, w *wal.WAL) *Handler {
	return &Handler{store: s, wal: w}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/get", h.handleGet)
	mux.HandleFunc("/set", h.handleSet)
	mux.HandleFunc("/delete", h.handleDelete)
	mux.HandleFunc("/health", h.handleHealth)
}

// GET /get?key=foo
func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
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

type setRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	TTL   int    `json:"ttl"` // seconds; 0 = no expiry
}

// POST /set
func (h *Handler) handleSet(w http.ResponseWriter, r *http.Request) {
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

	ttl := time.Duration(req.TTL) * time.Second
	h.store.Set(req.Key, req.Value, ttl)

	// WAL write — record absolute expiry so replay is deterministic
	_, expiresAt, _ := h.store.GetEntry(req.Key)
	if err := h.wal.WriteSet(req.Key, req.Value, expiresAt); err != nil {
		http.Error(w, "wal write error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DELETE /delete?key=foo
func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}

	h.store.Delete(key)
	if err := h.wal.WriteDelete(key); err != nil {
		http.Error(w, "wal write error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GET /health
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"keys":   strconv.Itoa(h.store.Len()),
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
