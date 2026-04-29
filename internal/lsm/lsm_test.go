package lsm

import (
	"testing"
	"time"
)

func newTree(t *testing.T) *Tree {
	t.Helper()
	dir := t.TempDir()
	tree, err := NewTree(dir, 1024, 4) // flush at 1KB, compact at 4 tables
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tree.Close() })
	return tree
}

func TestSetGet(t *testing.T) {
	tr := newTree(t)
	tr.Set("foo", "bar", 0)
	val, err := tr.Get("foo")
	if err != nil || val != "bar" {
		t.Fatalf("expected bar, got %q %v", val, err)
	}
}

func TestDelete(t *testing.T) {
	tr := newTree(t)
	tr.Set("k", "v", 0)
	tr.Delete("k")
	_, err := tr.Get("k")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestOverwriteInMemTable(t *testing.T) {
	tr := newTree(t)
	tr.Set("k", "v1", 0)
	tr.Set("k", "v2", 0)
	val, _ := tr.Get("k")
	if val != "v2" {
		t.Fatalf("expected v2, got %q", val)
	}
}

func TestFlushAndRead(t *testing.T) {
	tr := newTree(t)
	tr.Set("a", "1", 0)
	tr.Set("b", "2", 0)
	if err := tr.Flush(); err != nil {
		t.Fatal(err)
	}
	val, err := tr.Get("a")
	if err != nil || val != "1" {
		t.Fatalf("expected 1 from SSTable, got %q %v", val, err)
	}
}

func TestTombstoneAcrossFlush(t *testing.T) {
	tr := newTree(t)
	tr.Set("x", "y", 0)
	tr.Flush()
	tr.Delete("x")
	_, err := tr.Get("x")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after tombstone, got %v", err)
	}
}

func TestCompaction(t *testing.T) {
	tr := newTree(t)
	// Flush 5 tables; auto-compact fires at 4, leaving 1+1=2 max
	for i := 0; i < 5; i++ {
		tr.Set("key", "latest", 0)
		tr.Flush()
	}
	// Manual compaction to collapse everything
	if err := tr.Compact(); err != nil {
		t.Fatal(err)
	}
	val, err := tr.Get("key")
	if err != nil || val != "latest" {
		t.Fatalf("expected latest after compaction, got %q %v", val, err)
	}
	if len(tr.l0) > 1 {
		t.Fatalf("expected at most 1 table after compaction, got %d", len(tr.l0))
	}
}

func TestTTLExpiredKeysDroppedOnFlush(t *testing.T) {
	tr := newTree(t)
	tr.Set("exp", "val", 50*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	tr.Flush()
	_, err := tr.Get("exp")
	if err != ErrNotFound {
		t.Fatalf("expected expired key to be gone, got %v", err)
	}
}

func TestBloomFilterPreventsRead(t *testing.T) {
	bf := NewBloomFilter(100, 0.01)
	bf.Add("exists")
	if !bf.Contains("exists") {
		t.Fatal("bloom should contain added key")
	}
	// A key never added should very likely be absent
	if bf.Contains("definitely-not-here-xyzabc") {
		// This is a probabilistic test; with p=0.01 this is almost certain not to trigger
		t.Log("bloom false positive (acceptable)")
	}
}
