package lsm

import (
	"sort"
	"sync"
	"time"
)

// MemEntry holds a single KV record in the MemTable.
// Deleted keys are represented with Tombstone=true so they can shadow SSTable reads.
type MemEntry struct {
	Key       string
	Value     string
	ExpiresAt time.Time
	Tombstone bool
}

func (e MemEntry) isExpired() bool {
	return !e.ExpiresAt.IsZero() && time.Now().After(e.ExpiresAt)
}

// MemTable is an in-memory sorted structure that buffers writes before they
// are flushed to an immutable SSTable on disk.
// Internally it uses a sorted slice; for production you'd use a skip list or red-black tree.
type MemTable struct {
	mu      sync.RWMutex
	entries map[string]MemEntry
	size    int // approximate byte size to trigger flush
}

func NewMemTable() *MemTable {
	return &MemTable{entries: make(map[string]MemEntry)}
}

func (m *MemTable) Set(key, value string, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	e := MemEntry{Key: key, Value: value}
	if ttl > 0 {
		e.ExpiresAt = time.Now().Add(ttl)
	}
	m.entries[key] = e
	m.size += len(key) + len(value)
}

func (m *MemTable) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.entries[key] = MemEntry{Key: key, Tombstone: true}
	m.size += len(key)
}

func (m *MemTable) Get(key string) (MemEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	e, ok := m.entries[key]
	return e, ok
}

// Size returns approximate bytes — used by LSM to decide when to flush.
func (m *MemTable) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.size
}

// Sorted returns all entries in lexicographic key order for SSTable flush.
func (m *MemTable) Sorted() []MemEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entries := make([]MemEntry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})
	return entries
}

func (m *MemTable) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}
