package replication

import (
	"testing"
	"time"

	"github.com/aman7401/kv-store/internal/store"
)

func TestReplicationLog_SinceAndCommitIndex(t *testing.T) {
	l := NewReplicationLog()

	l.Append(OpSet, "a", "1", LogEntry{Op: OpSet, Key: "a", Value: "1"})
	l.Append(OpSet, "b", "2", LogEntry{Op: OpSet, Key: "b", Value: "2"})
	l.Append(OpDelete, "a", "", LogEntry{Op: OpDelete, Key: "a"})

	if l.CommitIndex() != 3 {
		t.Fatalf("expected commit index 3, got %d", l.CommitIndex())
	}

	entries := l.Since(1)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries since index 1, got %d", len(entries))
	}
	if entries[0].Index != 2 || entries[1].Index != 3 {
		t.Fatalf("unexpected indices: %v", entries)
	}
}

func TestReplicationLog_Trim(t *testing.T) {
	l := NewReplicationLog()
	for i := 0; i < 5; i++ {
		l.Append(OpSet, "k", "v", LogEntry{Op: OpSet, Key: "k", Value: "v"})
	}
	l.Trim(3) // keep entries with index > 3
	entries := l.Since(0)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries after trim, got %d", len(entries))
	}
}

func TestPrimaryWriteAndReplicaApply(t *testing.T) {
	primaryStore := store.New()
	primary := NewPrimary(primaryStore, "localhost:8080")

	// Write via primary
	primary.WriteSet("foo", "bar", 0)
	primary.WriteSet("baz", "qux", 0)
	primary.WriteDelete("foo")

	if primary.log.CommitIndex() != 3 {
		t.Fatalf("expected commit index 3, got %d", primary.log.CommitIndex())
	}

	// Simulate replica applying all entries
	replicaStore := store.New()
	for _, entry := range primary.log.Since(0) {
		switch entry.Op {
		case OpSet:
			replicaStore.SetWithExpiry(entry.Key, entry.Value, entry.ExpiresAt)
		case OpDelete:
			replicaStore.Delete(entry.Key)
		}
	}

	// foo was deleted
	if _, err := replicaStore.Get("foo"); err == nil {
		t.Fatal("expected foo to be deleted on replica")
	}
	// baz should exist
	val, err := replicaStore.Get("baz")
	if err != nil || val != "qux" {
		t.Fatalf("expected baz=qux on replica, got %q %v", val, err)
	}
}

func TestReplicaPromotesOnTimeout(t *testing.T) {
	kv := store.New()
	r := NewReplica(kv, "localhost:9999", ":9998")
	// Set last heartbeat far in the past
	r.mu.Lock()
	r.lastHeartbeat = time.Now().Add(-10 * time.Second)
	r.mu.Unlock()

	r.checkFailover()

	if !r.IsPromoted() {
		t.Fatal("expected replica to be promoted after timeout")
	}
}

func TestReplicaNoPromoteWithinTimeout(t *testing.T) {
	kv := store.New()
	r := NewReplica(kv, "localhost:9999", ":9998")
	r.mu.Lock()
	r.lastHeartbeat = time.Now() // recent heartbeat
	r.mu.Unlock()

	r.checkFailover()

	if r.IsPromoted() {
		t.Fatal("replica should NOT be promoted while heartbeat is fresh")
	}
}
