package store

import (
	"errors"
	"sync"
	"time"
)

var ErrKeyNotFound = errors.New("key not found")
var ErrKeyExpired = errors.New("key expired")

type entry struct {
	value     string
	expiresAt time.Time // zero means no expiry
}

func (e entry) isExpired() bool {
	return !e.expiresAt.IsZero() && time.Now().After(e.expiresAt)
}

// Store is a thread-safe in-memory key-value store.
type Store struct {
	mu   sync.RWMutex
	data map[string]entry
}

func New() *Store {
	return &Store{data: make(map[string]entry)}
}

func (s *Store) Set(key, value string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e := entry{value: value}
	if ttl > 0 {
		e.expiresAt = time.Now().Add(ttl)
	}
	s.data[key] = e
}

func (s *Store) Get(key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, ok := s.data[key]
	if !ok {
		return "", ErrKeyNotFound
	}
	if e.isExpired() {
		return "", ErrKeyExpired
	}
	return e.value, nil
}

func (s *Store) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
}

// Keys returns all non-expired keys — used by WAL snapshot and TTL cleanup.
func (s *Store) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := make([]string, 0, len(s.data))
	for k, e := range s.data {
		if !e.isExpired() {
			keys = append(keys, k)
		}
	}
	return keys
}

// GetEntry returns the raw entry for WAL persistence (includes TTL).
func (s *Store) GetEntry(key string) (value string, expiresAt time.Time, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	e, exists := s.data[key]
	if !exists || e.isExpired() {
		return "", time.Time{}, false
	}
	return e.value, e.expiresAt, true
}

// SetWithExpiry sets a key with an absolute expiry time (used by WAL recovery).
func (s *Store) SetWithExpiry(key, value string, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = entry{value: value, expiresAt: expiresAt}
}

// Len returns the number of entries (including expired, for metrics).
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}
