package lsm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("key not found")

// level0 holds SSTables flushed directly from MemTable (unsorted between files).
// Compaction merges them into level1+ where files don't overlap.

type tableHandle struct {
	path   string
	reader *SSTableReader
	bloom  *BloomFilter
}

// Tree is the main LSM Tree that routes reads and writes through MemTable and SSTables.
type Tree struct {
	mu       sync.RWMutex
	dir      string
	memtable *MemTable
	// L0 SSTables newest-first (searched in order so newest wins)
	l0       []*tableHandle
	flushAt  int // flush memtable when size exceeds this (bytes)
	compact  int // run compaction when l0 exceeds this count
	seq      int // monotonically increasing SSTable sequence number
}

func NewTree(dir string, flushAt, compact int) (*Tree, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("lsm mkdir: %w", err)
	}
	t := &Tree{
		dir:      dir,
		memtable: NewMemTable(),
		flushAt:  flushAt,
		compact:  compact,
	}
	if err := t.loadExisting(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Tree) loadExisting() error {
	matches, err := filepath.Glob(filepath.Join(t.dir, "*.sst"))
	if err != nil {
		return err
	}
	// Sort ascending so we append oldest-first; l0 is then reversed below
	sort.Strings(matches)
	for _, path := range matches {
		if err := t.openTable(path); err != nil {
			return err
		}
	}
	// Reverse so newest is first (last written has highest seq)
	for i, j := 0, len(t.l0)-1; i < j; i, j = i+1, j-1 {
		t.l0[i], t.l0[j] = t.l0[j], t.l0[i]
	}
	return nil
}

func (t *Tree) openTable(path string) error {
	reader, err := OpenSSTable(path)
	if err != nil {
		return err
	}
	keys := reader.Keys()
	bloom := NewBloomFilter(max(len(keys), 1), 0.01)
	for _, k := range keys {
		bloom.Add(k)
	}
	t.l0 = append(t.l0, &tableHandle{path: path, reader: reader, bloom: bloom})
	return nil
}

func (t *Tree) Set(key, value string, ttl time.Duration) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.memtable.Set(key, value, ttl)
	if t.memtable.Size() >= t.flushAt {
		return t.flushLocked()
	}
	return nil
}

func (t *Tree) Delete(key string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.memtable.Delete(key)
	return nil
}

// Get searches MemTable first, then L0 SSTables newest-to-oldest.
func (t *Tree) Get(key string) (string, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// 1. MemTable (most recent writes)
	if e, ok := t.memtable.Get(key); ok {
		if e.Tombstone || e.isExpired() {
			return "", ErrNotFound
		}
		return e.Value, nil
	}

	// 2. L0 SSTables, newest first
	for _, handle := range t.l0 {
		if !handle.bloom.Contains(key) {
			continue
		}
		e, found, err := handle.reader.Get(key)
		if err != nil {
			return "", err
		}
		if !found {
			continue
		}
		if e.Tombstone || e.isExpired() {
			return "", ErrNotFound
		}
		return e.Value, nil
	}
	return "", ErrNotFound
}

// Flush forces the current MemTable to disk as a new SSTable.
func (t *Tree) Flush() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.flushLocked()
}

func (t *Tree) flushLocked() error {
	if t.memtable.Len() == 0 {
		return nil
	}

	t.seq++
	path := filepath.Join(t.dir, fmt.Sprintf("%010d.sst", t.seq))
	entries := t.memtable.Sorted()

	if _, err := WriteSSTable(path, entries); err != nil {
		return fmt.Errorf("flush sstable: %w", err)
	}

	reader, err := OpenSSTable(path)
	if err != nil {
		return err
	}
	bloom := NewBloomFilter(max(len(entries), 1), 0.01)
	for _, e := range entries {
		bloom.Add(e.Key)
	}

	// Prepend so newest is at index 0
	t.l0 = append([]*tableHandle{{path: path, reader: reader, bloom: bloom}}, t.l0...)
	t.memtable = NewMemTable()

	if len(t.l0) >= t.compact {
		return t.compactL0Locked()
	}
	return nil
}

// compactL0Locked merges all L0 SSTables into one, dropping tombstones and expired keys.
func (t *Tree) compactL0Locked() error {
	if len(t.l0) < 2 {
		return nil
	}

	// Merge: iterate oldest-to-newest; newer entries overwrite older ones
	merged := make(map[string]MemEntry)
	for i := len(t.l0) - 1; i >= 0; i-- {
		t.l0[i].reader.Scan(func(e MemEntry) error {
			if !e.Tombstone && !e.isExpired() {
				merged[e.Key] = e
			} else {
				delete(merged, e.Key)
			}
			return nil
		})
	}

	// Sort and write compacted SSTable
	entries := make([]MemEntry, 0, len(merged))
	for _, e := range merged {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })

	t.seq++
	path := filepath.Join(t.dir, fmt.Sprintf("%010d.sst", t.seq))
	if _, err := WriteSSTable(path, entries); err != nil {
		return err
	}

	// Close and remove old files
	for _, h := range t.l0 {
		h.reader.Close()
		os.Remove(h.path)
	}
	t.l0 = nil

	if len(entries) == 0 {
		return nil
	}
	return t.openTable(path)
}

// Compact triggers a manual compaction.
func (t *Tree) Compact() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.compactL0Locked()
}

// Stats returns basic metrics for the admin endpoint.
func (t *Tree) Stats() map[string]int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return map[string]int{
		"memtable_keys": t.memtable.Len(),
		"memtable_size": t.memtable.Size(),
		"l0_tables":     len(t.l0),
	}
}

func (t *Tree) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Flush remaining memtable entries before close
	if err := t.flushLocked(); err != nil {
		return err
	}
	for _, h := range t.l0 {
		h.reader.Close()
	}
	return nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
