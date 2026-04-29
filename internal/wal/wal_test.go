package wal

import (
	"os"
	"testing"
	"time"
)

func TestWriteAndReplay(t *testing.T) {
	path := t.TempDir() + "/test.wal"

	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteSet("foo", "bar", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDelete("baz"); err != nil {
		t.Fatal(err)
	}
	w.Close()

	var records []Record
	if err := Replay(path, func(r Record) error {
		records = append(records, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].Op != OpSet || records[0].Key != "foo" || records[0].Value != "bar" {
		t.Fatalf("unexpected first record: %+v", records[0])
	}
	if records[1].Op != OpDelete || records[1].Key != "baz" {
		t.Fatalf("unexpected second record: %+v", records[1])
	}
}

func TestReplayMissingFile(t *testing.T) {
	err := Replay("/tmp/does-not-exist-kv.wal", func(r Record) error { return nil })
	if err != nil {
		t.Fatalf("expected nil for missing wal, got %v", err)
	}
}

func TestTruncate(t *testing.T) {
	path := t.TempDir() + "/trunc.wal"

	records := []Record{
		{Op: OpSet, Key: "a", Value: "1", Timestamp: time.Now()},
		{Op: OpSet, Key: "b", Value: "2", Timestamp: time.Now()},
	}
	if err := Truncate(path, records); err != nil {
		t.Fatal(err)
	}

	var replayed []Record
	Replay(path, func(r Record) error {
		replayed = append(replayed, r)
		return nil
	})
	if len(replayed) != 2 {
		t.Fatalf("expected 2 after truncate, got %d", len(replayed))
	}
}

func TestCorruptTailIsSkipped(t *testing.T) {
	path := t.TempDir() + "/corrupt.wal"

	w, _ := Open(path)
	w.WriteSet("good", "key", time.Time{})
	w.Close()

	// Append garbage to simulate partial write
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString(`{"op":"SET","key":` + "\n") // truncated JSON
	f.Close()

	var records []Record
	Replay(path, func(r Record) error {
		records = append(records, r)
		return nil
	})
	if len(records) != 1 {
		t.Fatalf("expected 1 valid record, got %d", len(records))
	}
}
