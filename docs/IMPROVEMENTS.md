# Improvements Roadmap

Grouped by effort and impact. Each item explains the problem it solves and how to implement it.

---

## Phase 1 Improvements — Storage & Durability

### 1. WAL Checkpointing
**Problem**: WAL grows unbounded. On restart, replaying 1M records is slow.
**Fix**: Periodically snapshot the full store to disk, then truncate the WAL to only records after the snapshot. On recovery: load snapshot, replay only the delta WAL.
```
checkpoint file: {key → value, expiresAt} serialized as gob or protobuf
WAL truncated to: only entries after checkpoint sequence number
```

### 2. WAL Batching (Group Commit)
**Problem**: Every `Set` does an individual `fsync` — expensive at high write rates.
**Fix**: Buffer writes for a few milliseconds, fsync once for the whole batch.
```go
// Accumulate writes in a buffer
// Flush + fsync on timer (e.g., every 2ms) or when buffer hits threshold
// Trade-off: 2ms extra latency for 10x throughput improvement
```

### 3. Persistent TTL Index
**Problem**: TTL sweeper scans all keys every 5 seconds (O(N)).
**Fix**: Maintain a min-heap sorted by `expiresAt`. Pop and delete the top when it expires — O(log N) per eviction, zero scanning.

---

## Phase 2 Improvements — LSM Tree

### 4. Skip List for MemTable
**Problem**: Current MemTable uses a `map` + sort on flush — O(N log N) at flush time.
**Fix**: Replace with a skip list (O(log N) inserts, already ordered). No sort needed on flush.

### 5. Multi-Level Compaction (Leveled Compaction)
**Problem**: All SSTables go to L0. L0 files can overlap in key ranges, so reads must check every L0 file.
**Fix**: Implement leveled compaction (like RocksDB):
```
L0: up to 4 files (unsorted between files)
L1: up to 10 files, sorted, non-overlapping, total size ~10MB
L2: up to 100 files, total size ~100MB
...
```
Reads only need to check 1 file per level (binary search on key ranges).

### 6. Sparse Index + Data Blocks
**Problem**: Current SSTable index stores every key. For 1M keys this is large.
**Fix**: Use data blocks (4KB each). Index stores only the first key of each block. Read = find block via sparse index → scan block (~few entries).

### 7. SSTable Bloom Filter Persistence
**Problem**: Bloom filter is rebuilt in memory on every startup — slow for large SSTables.
**Fix**: Serialize the bloom filter bit array into a metadata section at the end of each SSTable file. Load it on open without rebuilding.

### 8. Merge Iterator for Compaction
**Problem**: Current compaction loads all entries into a map — high memory usage for large SSTables.
**Fix**: Use a merge iterator (k-way merge with a min-heap). Stream entries from all SSTables in sorted order without loading them all into memory.

---

## Phase 3 Improvements — Replication & Consensus

### 9. Raft Consensus
**Problem**: Current failover is timeout-based — no quorum, risk of split-brain with 3+ nodes.
**Fix**: Implement Raft (or use `hashicorp/raft`). Raft guarantees:
- Only one leader at a time (no split-brain)
- A write is committed only after N/2+1 nodes acknowledge it
- Automatic leader election with majority vote

### 10. Synchronous Replication Option
**Problem**: Async replication means a primary crash can lose the last few writes.
**Fix**: Add a `--replication=sync` flag. Primary waits for at least 1 replica to acknowledge before returning 200 OK. Trade-off: higher latency, stronger durability.

### 11. Read-Your-Writes Consistency
**Problem**: After writing to primary, reading from replica may return stale data.
**Fix**: Client attaches the `commit_index` it received from the last write. Replica checks if its `last_index >= commit_index` — if not, it either waits or redirects to primary.

### 12. Persistent Replication Log
**Problem**: Replication log is in-memory. If primary restarts, replicas must re-sync from scratch.
**Fix**: Persist the replication log to a WAL file. On restart, replicas can catch up from where they left off using their stored `last_index`.

### 13. Replication Log Compaction
**Problem**: Replication log grows unbounded in memory.
**Fix**: After all known replicas have applied up to index N, trim entries where `index <= N`. Track per-replica `last_index` via heartbeats.

---

## Operational Improvements

### 14. Metrics with Prometheus
Expose a `/metrics` endpoint (Prometheus format):
```
kv_get_requests_total
kv_set_requests_total
kv_keys_total
kv_wal_bytes_total
kv_lsm_memtable_size_bytes
kv_lsm_l0_tables
kv_replication_lag_entries{replica="..."}
kv_ttl_evictions_total
```

### 15. Structured Logging (zerolog / zap)
Replace `log.Printf` with structured JSON logs:
```json
{"level":"info","ts":"2024-...","op":"SET","key":"foo","latency_ms":0.3}
```
Makes logs searchable in Datadog/CloudWatch/Loki.

### 16. Configuration File
Replace hardcoded constants with a YAML/TOML config:
```yaml
server:
  addr: ":8080"
  engine: lsm
lsm:
  flush_threshold_bytes: 4194304
  compact_threshold: 8
  data_dir: ./data/lsm
wal:
  path: ./wal/kv.wal
replication:
  mode: async
  pull_interval: 500ms
  heartbeat_interval: 1s
  failover_timeout: 5s
```

### 17. gRPC API
Replace HTTP/JSON with gRPC + Protobuf:
- ~3-5x lower latency (binary protocol, HTTP/2 multiplexing)
- Strongly typed API with generated clients
- Bidirectional streaming for replication push (instead of pull)

### 18. Client SDK
Build a Go client that handles:
- Primary/replica routing (writes → primary, reads → any replica)
- Automatic retry with exponential backoff
- Connection pooling

### 19. Docker + docker-compose
```yaml
services:
  primary:
    image: kv-store
    command: primary --addr=:8080
  replica1:
    image: kv-store
    command: replica --addr=:8081 --primary=primary:8080
  replica2:
    image: kv-store
    command: replica --addr=:8082 --primary=primary:8080
```

### 20. Benchmarks
Add `go test -bench` benchmarks:
```go
BenchmarkHashMapGet-8       50000000    25 ns/op
BenchmarkHashMapSet-8       10000000   150 ns/op   (includes fsync)
BenchmarkLSMGet-8            5000000   280 ns/op
BenchmarkLSMSetFlush-8       1000000  1200 ns/op
```

---

## Priority Matrix

| Improvement | Impact | Effort | Do Next? |
|-------------|--------|--------|----------|
| WAL Checkpointing | High | Medium | ✅ Yes |
| Persistent Replication Log | High | Medium | ✅ Yes |
| Skip List MemTable | Medium | Medium | Maybe |
| Metrics (Prometheus) | High | Low | ✅ Yes |
| Leveled Compaction | High | High | Later |
| Raft Consensus | Very High | Very High | Later |
| gRPC API | Medium | Medium | Later |
| docker-compose | High | Low | ✅ Yes |
