package store

import (
	"testing"
	"time"
)

func TestSetAndGet(t *testing.T) {
	s := New()
	s.Set("k", "v", 0)
	val, err := s.Get("k")
	if err != nil || val != "v" {
		t.Fatalf("expected v, got %q %v", val, err)
	}
}

func TestGetMissing(t *testing.T) {
	s := New()
	_, err := s.Get("missing")
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	s := New()
	s.Set("k", "v", 0)
	s.Delete("k")
	_, err := s.Get("k")
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound after delete, got %v", err)
	}
}

func TestTTLExpiry(t *testing.T) {
	s := New()
	s.Set("k", "v", 50*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	_, err := s.Get("k")
	if err != ErrKeyExpired {
		t.Fatalf("expected ErrKeyExpired, got %v", err)
	}
}

func TestTTLNotExpiredYet(t *testing.T) {
	s := New()
	s.Set("k", "v", 10*time.Second)
	val, err := s.Get("k")
	if err != nil || val != "v" {
		t.Fatalf("expected v within TTL, got %q %v", val, err)
	}
}

func TestOverwrite(t *testing.T) {
	s := New()
	s.Set("k", "v1", 0)
	s.Set("k", "v2", 0)
	val, _ := s.Get("k")
	if val != "v2" {
		t.Fatalf("expected v2, got %q", val)
	}
}

func TestLen(t *testing.T) {
	s := New()
	s.Set("a", "1", 0)
	s.Set("b", "2", 0)
	if s.Len() != 2 {
		t.Fatalf("expected 2, got %d", s.Len())
	}
}
