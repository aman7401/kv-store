package lsm

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// SSTable is an immutable, sorted, on-disk file of MemEntry records.
//
// File format (binary):
//   [record_length uint32][record_json bytes] ...
//   [index_offset uint64]  <- last 8 bytes
//
// The index is a JSON-encoded []IndexEntry appended after all records.
// IndexEntry stores the byte offset of every key for O(1) seeks.

type IndexEntry struct {
	Key    string `json:"k"`
	Offset int64  `json:"o"`
}

// WriteSSTable flushes sorted MemEntries to path and returns the index.
func WriteSSTable(path string, entries []MemEntry) ([]IndexEntry, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("sstable create %s: %w", path, err)
	}
	defer f.Close()

	bw := bufio.NewWriter(f)
	var index []IndexEntry
	var offset int64

	for _, e := range entries {
		data, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		length := uint32(len(data))

		// record_length (4 bytes) + record_json
		if err := binary.Write(bw, binary.LittleEndian, length); err != nil {
			return nil, err
		}
		if _, err := bw.Write(data); err != nil {
			return nil, err
		}
		index = append(index, IndexEntry{Key: e.Key, Offset: offset})
		offset += 4 + int64(length)
	}

	// Flush records before writing index
	if err := bw.Flush(); err != nil {
		return nil, err
	}

	// Write index JSON
	indexStart, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	indexData, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(indexData); err != nil {
		return nil, err
	}

	// Last 8 bytes = offset where index starts
	if err := binary.Write(f, binary.LittleEndian, uint64(indexStart)); err != nil {
		return nil, err
	}
	return index, f.Sync()
}

// SSTableReader reads entries from an on-disk SSTable.
type SSTableReader struct {
	file  *os.File
	index []IndexEntry
}

func OpenSSTable(path string) (*SSTableReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("sstable open %s: %w", path, err)
	}

	// Read index offset from last 8 bytes
	if _, err := f.Seek(-8, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	var indexOffset uint64
	if err := binary.Read(f, binary.LittleEndian, &indexOffset); err != nil {
		f.Close()
		return nil, err
	}

	// Read index JSON
	if _, err := f.Seek(int64(indexOffset), io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	// index ends where the 8-byte footer starts
	stat, _ := f.Stat()
	indexLen := stat.Size() - int64(indexOffset) - 8
	indexData := make([]byte, indexLen)
	if _, err := io.ReadFull(f, indexData); err != nil {
		f.Close()
		return nil, err
	}
	var index []IndexEntry
	if err := json.Unmarshal(indexData, &index); err != nil {
		f.Close()
		return nil, err
	}

	return &SSTableReader{file: f, index: index}, nil
}

// Get performs a binary-search on the index then seeks to the record.
func (r *SSTableReader) Get(key string) (MemEntry, bool, error) {
	lo, hi := 0, len(r.index)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case r.index[mid].Key == key:
			return r.readAt(r.index[mid].Offset)
		case r.index[mid].Key < key:
			lo = mid + 1
		default:
			hi = mid - 1
		}
	}
	return MemEntry{}, false, nil
}

func (r *SSTableReader) readAt(offset int64) (MemEntry, bool, error) {
	if _, err := r.file.Seek(offset, io.SeekStart); err != nil {
		return MemEntry{}, false, err
	}
	var length uint32
	if err := binary.Read(r.file, binary.LittleEndian, &length); err != nil {
		return MemEntry{}, false, err
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r.file, data); err != nil {
		return MemEntry{}, false, err
	}
	var e MemEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return MemEntry{}, false, err
	}
	return e, true, nil
}

// Scan iterates all entries in sorted order (used by compaction).
func (r *SSTableReader) Scan(fn func(MemEntry) error) error {
	if _, err := r.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	br := bufio.NewReader(r.file)
	for i := 0; i < len(r.index); i++ {
		var length uint32
		if err := binary.Read(br, binary.LittleEndian, &length); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(br, data); err != nil {
			return err
		}
		var e MemEntry
		if err := json.Unmarshal(data, &e); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func (r *SSTableReader) Keys() []string {
	keys := make([]string, len(r.index))
	for i, e := range r.index {
		keys[i] = e.Key
	}
	return keys
}

func (r *SSTableReader) Close() error {
	return r.file.Close()
}

// EntryAge returns how old the SSTable is (for compaction scoring).
func (r *SSTableReader) ModTime() (time.Time, error) {
	stat, err := r.file.Stat()
	if err != nil {
		return time.Time{}, err
	}
	return stat.ModTime(), nil
}
