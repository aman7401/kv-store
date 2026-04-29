package replication

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/aman7401/kv-store/internal/store"
)

// Primary is the write-accepting node. All mutations go through WriteSet/WriteDelete,
// which update the store and append to the replication log.
// Replicas pull from /replication/sync; primary tracks their state via /replication/heartbeat.
type Primary struct {
	store    *store.Store
	log      *ReplicationLog
	replicas map[string]*ReplicaState
	mu       sync.RWMutex
	addr     string
}

func NewPrimary(s *store.Store, addr string) *Primary {
	return &Primary{
		store:    s,
		log:      NewReplicationLog(),
		replicas: make(map[string]*ReplicaState),
		addr:     addr,
	}
}

// WriteSet applies a SET to the store and appends to the replication log.
func (p *Primary) WriteSet(key, value string, ttl time.Duration) {
	p.store.Set(key, value, ttl)
	_, expiresAt, _ := p.store.GetEntry(key)
	p.log.Append(OpSet, key, value, LogEntry{
		Op:        OpSet,
		Key:       key,
		Value:     value,
		ExpiresAt: expiresAt,
		Timestamp: time.Now(),
	})
}

// WriteDelete applies a DELETE to the store and appends to the replication log.
func (p *Primary) WriteDelete(key string) {
	p.store.Delete(key)
	p.log.Append(OpDelete, key, "", LogEntry{
		Op:        OpDelete,
		Key:       key,
		Timestamp: time.Now(),
	})
}

// RegisterRoutes mounts the replication endpoints onto the given mux.
func (p *Primary) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/replication/sync", p.handleSync)
	mux.HandleFunc("/replication/heartbeat", p.handleHeartbeat)
	mux.HandleFunc("/replication/status", p.handleStatus)
}

// GET /replication/sync?from=<index>
// Replicas call this to pull log entries they haven't applied yet.
func (p *Primary) handleSync(w http.ResponseWriter, r *http.Request) {
	var req SyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.FromIndex = 0
	}

	entries := p.log.Since(req.FromIndex)
	resp := SyncResponse{
		Entries:     entries,
		CommitIndex: p.log.CommitIndex(),
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /replication/heartbeat
// Replicas ping primary to signal liveness and report their last applied index.
func (p *Primary) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var hb HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	p.replicas[hb.ReplicaAddr] = &ReplicaState{
		Addr:      hb.ReplicaAddr,
		LastIndex: hb.LastIndex,
		UpdatedAt: time.Now(),
	}
	p.mu.Unlock()

	log.Printf("heartbeat from replica %s (last_index=%d)", hb.ReplicaAddr, hb.LastIndex)
	writeJSON(w, http.StatusOK, HeartbeatResponse{
		PrimaryAddr: p.addr,
		CommitIndex: p.log.CommitIndex(),
		IsPrimary:   true,
	})
}

// GET /replication/status — admin view of replica lag
func (p *Primary) handleStatus(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	type replicaInfo struct {
		Addr      string    `json:"addr"`
		LastIndex uint64    `json:"last_index"`
		Lag       uint64    `json:"lag"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	commit := p.log.CommitIndex()
	infos := make([]replicaInfo, 0, len(p.replicas))
	for _, rs := range p.replicas {
		lag := uint64(0)
		if commit > rs.LastIndex {
			lag = commit - rs.LastIndex
		}
		infos = append(infos, replicaInfo{
			Addr:      rs.Addr,
			LastIndex: rs.LastIndex,
			Lag:       lag,
			UpdatedAt: rs.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"commit_index": commit,
		"replicas":     infos,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
