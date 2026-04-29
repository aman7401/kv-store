package replication

import (
	"sync"
)

// ReplicationLog is an in-memory ordered log of all mutations since startup.
// Replicas use the Index to request only entries they haven't seen yet.
// In production you'd persist this to disk; here it's in-memory for clarity.
type ReplicationLog struct {
	mu      sync.RWMutex
	entries []LogEntry
	nextIdx uint64
}

func NewReplicationLog() *ReplicationLog {
	return &ReplicationLog{nextIdx: 1}
}

func (l *ReplicationLog) Append(op OpType, key, value string, entry LogEntry) LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry.Index = l.nextIdx
	l.nextIdx++
	l.entries = append(l.entries, entry)
	return entry
}

// Since returns all entries with Index > fromIndex (replica catch-up).
func (l *ReplicationLog) Since(fromIndex uint64) []LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var result []LogEntry
	for _, e := range l.entries {
		if e.Index > fromIndex {
			result = append(result, e)
		}
	}
	return result
}

func (l *ReplicationLog) CommitIndex() uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if len(l.entries) == 0 {
		return 0
	}
	return l.entries[len(l.entries)-1].Index
}

// Trim drops entries older than keepAfter to bound memory usage.
func (l *ReplicationLog) Trim(keepAfter uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	i := 0
	for i < len(l.entries) && l.entries[i].Index <= keepAfter {
		i++
	}
	l.entries = l.entries[i:]
}
