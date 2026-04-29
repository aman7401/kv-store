package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/aman7401/kv-store/internal/lsm"
)

// LSMHandler serves the same HTTP contract as Handler but uses an LSM Tree backend.
type LSMHandler struct {
	tree *lsm.Tree
}

func NewLSM(tree *lsm.Tree) *LSMHandler {
	return &LSMHandler{tree: tree}
}

func (h *LSMHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/get", h.handleGet)
	mux.HandleFunc("/set", h.handleSet)
	mux.HandleFunc("/delete", h.handleDelete)
	mux.HandleFunc("/flush", h.handleFlush)
	mux.HandleFunc("/compact", h.handleCompact)
	mux.HandleFunc("/health", h.handleHealth)
}

func (h *LSMHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	value, err := h.tree.Get(key)
	if err != nil {
		if errors.Is(err, lsm.ErrNotFound) {
			http.Error(w, "key not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": value})
}

func (h *LSMHandler) handleSet(w http.ResponseWriter, r *http.Request) {
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
	if err := h.tree.Set(req.Key, req.Value, time.Duration(req.TTL)*time.Second); err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *LSMHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	if err := h.tree.Delete(key); err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /flush — manually flush MemTable to SSTable
func (h *LSMHandler) handleFlush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := h.tree.Flush(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "flushed"})
}

// POST /compact — manually trigger L0 compaction
func (h *LSMHandler) handleCompact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := h.tree.Compact(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "compacted"})
}

func (h *LSMHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	stats := h.tree.Stats()
	resp := map[string]string{
		"status":         "ok",
		"memtable_keys":  strconv.Itoa(stats["memtable_keys"]),
		"memtable_bytes": strconv.Itoa(stats["memtable_size"]),
		"l0_tables":      strconv.Itoa(stats["l0_tables"]),
	}
	writeJSON(w, http.StatusOK, resp)
}
