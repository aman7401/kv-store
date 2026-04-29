package wal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type OpType string

const (
	OpSet    OpType = "SET"
	OpDelete OpType = "DEL"
)

// Record is a single WAL entry, serialized as newline-delimited JSON.
type Record struct {
	Op        OpType    `json:"op"`
	Key       string    `json:"key"`
	Value     string    `json:"value,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Timestamp time.Time `json:"ts"`
}

// WAL is an append-only write-ahead log that persists mutations to disk.
// Each record is a JSON line, fsynced after every write for durability.
type WAL struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
}

func Open(path string) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("wal open %s: %w", path, err)
	}
	return &WAL{file: f, enc: json.NewEncoder(f)}, nil
}

func (w *WAL) WriteSet(key, value string, expiresAt time.Time) error {
	return w.write(Record{
		Op:        OpSet,
		Key:       key,
		Value:     value,
		ExpiresAt: expiresAt,
		Timestamp: time.Now(),
	})
}

func (w *WAL) WriteDelete(key string) error {
	return w.write(Record{
		Op:        OpDelete,
		Key:       key,
		Timestamp: time.Now(),
	})
}

func (w *WAL) write(r Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.enc.Encode(r); err != nil {
		return fmt.Errorf("wal encode: %w", err)
	}
	// fsync guarantees the record survives a process crash
	return w.file.Sync()
}

// Replay reads all records from the beginning of the WAL and calls fn for each.
// Used on startup to rebuild in-memory state after a crash.
func Replay(path string, fn func(Record) error) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // fresh start
		}
		return fmt.Errorf("wal replay open: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			// skip corrupt tail records (crash mid-write)
			continue
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// Truncate rewrites the WAL with only the provided records.
// Call this after a checkpoint to keep WAL size bounded.
func Truncate(path string, records []Record) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("wal truncate create: %w", err)
	}
	enc := json.NewEncoder(f)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, path)
}

func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}
