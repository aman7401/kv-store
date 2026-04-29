package replication

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aman7401/kv-store/internal/store"
)

// Replica pulls log entries from the primary and applies them to its local store.
// It also serves reads (GET) from its own store.
// If the primary is unreachable for failoverTimeout, it promotes itself to primary.
type Replica struct {
	store           *store.Store
	primaryAddr     string
	selfAddr        string
	lastIndex       atomic.Uint64
	pullInterval    time.Duration
	heartbeatInterval time.Duration
	failoverTimeout time.Duration
	lastHeartbeat   time.Time
	mu              sync.Mutex
	promoted        bool
	stop            chan struct{}
}

func NewReplica(s *store.Store, primaryAddr, selfAddr string) *Replica {
	return &Replica{
		store:             s,
		primaryAddr:       primaryAddr,
		selfAddr:          selfAddr,
		pullInterval:      500 * time.Millisecond,
		heartbeatInterval: 1 * time.Second,
		failoverTimeout:   5 * time.Second,
		lastHeartbeat:     time.Now(),
		stop:              make(chan struct{}),
	}
}

// Start begins background pull and heartbeat loops.
func (r *Replica) Start() {
	go r.pullLoop()
	go r.heartbeatLoop()
}

func (r *Replica) Stop() {
	close(r.stop)
}

func (r *Replica) pullLoop() {
	ticker := time.NewTicker(r.pullInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := r.pull(); err != nil {
				log.Printf("replica pull error: %v", err)
			}
		case <-r.stop:
			return
		}
	}
}

func (r *Replica) pull() error {
	req := SyncRequest{FromIndex: r.lastIndex.Load()}
	body, _ := json.Marshal(req)

	resp, err := http.Post(
		fmt.Sprintf("http://%s/replication/sync", r.primaryAddr),
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("sync request: %w", err)
	}
	defer resp.Body.Close()

	var sync SyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&sync); err != nil {
		return fmt.Errorf("sync decode: %w", err)
	}

	for _, entry := range sync.Entries {
		r.apply(entry)
	}
	return nil
}

func (r *Replica) apply(entry LogEntry) {
	switch entry.Op {
	case OpSet:
		r.store.SetWithExpiry(entry.Key, entry.Value, entry.ExpiresAt)
	case OpDelete:
		r.store.Delete(entry.Key)
	}
	r.lastIndex.Store(entry.Index)
	log.Printf("replica applied index=%d op=%s key=%s", entry.Index, entry.Op, entry.Key)
}

func (r *Replica) heartbeatLoop() {
	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.sendHeartbeat()
		case <-r.stop:
			return
		}
	}
}

func (r *Replica) sendHeartbeat() {
	hb := HeartbeatRequest{
		ReplicaAddr: r.selfAddr,
		LastIndex:   r.lastIndex.Load(),
	}
	body, _ := json.Marshal(hb)
	resp, err := http.Post(
		fmt.Sprintf("http://%s/replication/heartbeat", r.primaryAddr),
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		log.Printf("heartbeat failed: %v — checking failover", err)
		r.checkFailover()
		return
	}
	defer resp.Body.Close()

	r.mu.Lock()
	r.lastHeartbeat = time.Now()
	r.mu.Unlock()
}

func (r *Replica) checkFailover() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.promoted {
		return
	}
	if time.Since(r.lastHeartbeat) >= r.failoverTimeout {
		log.Printf("primary %s unreachable for %v — replica %s PROMOTING TO PRIMARY",
			r.primaryAddr, r.failoverTimeout, r.selfAddr)
		r.promoted = true
	}
}

// IsPromoted returns true if this replica has taken over as primary.
func (r *Replica) IsPromoted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.promoted
}

// LastIndex returns the last applied log index.
func (r *Replica) LastIndex() uint64 {
	return r.lastIndex.Load()
}

// RegisterRoutes mounts read and status endpoints for the replica.
func (r *Replica) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/get", r.handleGet)
	mux.HandleFunc("/health", r.handleHealth)
}

func (r *Replica) handleGet(w http.ResponseWriter, req *http.Request) {
	key := req.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	value, err := r.store.Get(key)
	if err != nil {
		http.Error(w, "key not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": value})
}

func (r *Replica) handleHealth(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	promoted := r.promoted
	lastHB := r.lastHeartbeat
	r.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"role":            "replica",
		"promoted":        promoted,
		"last_index":      r.lastIndex.Load(),
		"primary_addr":    r.primaryAddr,
		"last_heartbeat":  lastHB,
	})
}
