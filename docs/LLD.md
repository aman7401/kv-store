# Low Level Design — Distributed Key-Value Store

## 1. Package Structure

```
kv-store/
├── cmd/
│   ├── server/main.go       — Phase 1/2: single-node server (--engine flag)
│   ├── primary/main.go      — Phase 3: primary node
│   └── replica/main.go      — Phase 3: replica node
└── internal/
    ├── store/               — In-memory HashMap engine
    ├── wal/                 — Write-Ahead Log
    ├── ttl/                 — Background expiry sweeper
    ├── api/                 — HTTP handlers (HashMap, LSM, Primary)
    ├── lsm/                 — LSM Tree: MemTable, SSTable, Bloom, Tree
    └── replication/         — Protocol types, ReplicationLog, Primary, Replica
```

---

## 2. Component Deep-Dive

### 2.1 Store (`internal/store/store.go`)

```
Store
├── mu       sync.RWMutex       — reader-writer lock
└── data     map[string]entry   — O(1) lookup

entry
├── value     string
└── expiresAt time.Time         — zero = no expiry
```

**Key methods:**

| Method | Complexity | Notes |
|--------|-----------|-------|
| `Set(key, value, ttl)` | O(1) | Converts duration → absolute time |
| `Get(key)` | O(1) | Returns ErrKeyExpired (lazy check) |
| `Delete(key)` | O(1) | Immediate removal |
| `Keys()` | O(N) | Snapshot for TTL sweeper |
| `SetWithExpiry(key, value, t)` | O(1) | Used by WAL recovery and replication |

**Locking strategy**: `RWMutex` — concurrent reads don't block each other; writes take exclusive lock. Keys() is O(N) but only called by the background sweeper, not on the hot path.

---

### 2.2 WAL (`internal/wal/wal.go`)

**File format** — newline-delimited JSON (one record per line):

```
{"op":"SET","key":"foo","value":"bar","expires_at":"0001-01-01T00:00:00Z","ts":"2024-01-01T..."}
{"op":"DEL","key":"foo","ts":"2024-01-01T..."}
```

**Durability guarantee**: every `write()` calls `file.Sync()` (fsync). This flushes the OS page cache to disk, so the record survives a process crash.

**Recovery algorithm** (startup):
```
for each line in wal file:
    parse JSON → Record
    if corrupt/truncated → skip (partial write on crash)
    if op == SET:
        if expiresAt is past → skip (already expired)
        store.SetWithExpiry(key, value, expiresAt)
    if op == DEL:
        store.Delete(key)
```

**Truncation**: after a checkpoint, `Truncate()` atomically rewrites the WAL via `rename(tmp, wal)`. Rename is atomic on POSIX — no partial state visible.

---

### 2.3 TTL Cleaner (`internal/ttl/cleaner.go`)

```
Cleaner
├── interval   time.Duration    — sweep frequency (default 5s)
├── keys()     func()[]string   — snapshot callback (avoids lock hold)
├── isExpired() func(string)bool
├── evict()    func(string)
└── stop       chan struct{}
```

**Design**: callbacks decouple the cleaner from the store implementation — the same cleaner works with the HashMap store. The `keys()` snapshot is taken without holding the store lock across the entire sweep, avoiding long lock contention.

---

### 2.4 MemTable (`internal/lsm/memtable.go`)

```
MemTable
├── mu       sync.RWMutex
├── entries  map[string]MemEntry   — O(1) writes
└── size     int                   — approximate byte count

MemEntry
├── Key        string
├── Value      string
├── ExpiresAt  time.Time
└── Tombstone  bool                — true = deleted key
```

**Sorted()** — called only on flush, not on hot path. Sorts entries by key into a slice for SSTable writing. For a production system, this would be a skip list or red-black tree (O(log N) inserts, already ordered).

**Tombstones** — deleted keys are not removed from the MemTable. They are written as `{Tombstone: true}` entries. During a read, a tombstone shadows any older value in an SSTable. Tombstones are dropped during compaction.

---

### 2.5 SSTable (`internal/lsm/sstable.go`)

**File format:**

```
┌──────────────────────────────────────────────────────┐
│  [4 bytes: record length] [N bytes: JSON record]     │  ← record 0
│  [4 bytes: record length] [N bytes: JSON record]     │  ← record 1
│  ...                                                  │
│  [JSON: [{k:"a",o:0},{k:"b",o:52},...]]              │  ← index
│  [8 bytes: offset of index start]                     │  ← footer
└──────────────────────────────────────────────────────┘
```

**Read algorithm (Get):**
1. Seek to last 8 bytes → read `indexOffset`
2. Seek to `indexOffset` → read + unmarshal index JSON
3. Binary search index for key → get byte `offset`
4. Seek to `offset` → read 4-byte length → read N bytes → unmarshal record

**Cost**: 3 seeks + 2 reads per SSTable lookup. With bloom filters, most misses are skipped entirely.

**Immutability**: SSTables are never modified after creation. This makes them safe to read from multiple goroutines without locking, and simplifies crash recovery (a partially written SSTable is just not registered in the tree).

---

### 2.6 Bloom Filter (`internal/lsm/bloom.go`)

**Formula:**
```
m = -(n * ln(p)) / (ln(2))^2     — optimal bit array size
k = (m/n) * ln(2)                 — optimal number of hash functions
```

With `n=1000 keys`, `p=0.01` (1% false positive rate):
- `m = 9585 bits (~1.2 KB)`
- `k = 7 hash functions`

**Hash function**: FNV-64a seeded with the function index — cheap, good distribution, no external deps.

**Invariant**: if `bloom.Contains(key) == false`, the key is **definitely not** in this SSTable. Skip the disk read entirely. If true, the key *might* be there (1% false positive) — do the actual disk lookup.

---

### 2.7 LSM Tree (`internal/lsm/lsm.go`)

```
Tree
├── mu        sync.RWMutex
├── dir       string            — SSTable directory
├── memtable  *MemTable         — current write buffer
├── l0        []*tableHandle    — L0 SSTables, newest first
├── flushAt   int               — flush threshold (bytes)
├── compact   int               — compaction threshold (table count)
└── seq       int               — monotonic SSTable sequence number

tableHandle
├── path    string
├── reader  *SSTableReader
└── bloom   *BloomFilter
```

**Read path (Get):**
```
1. memtable.Get(key)         → found? return / tombstone? not found
2. for handle in l0:          → newest SSTable first
       bloom.Contains(key)   → false? skip
       handle.reader.Get(key) → found? return / tombstone? not found
3. return ErrNotFound
```

**Compaction algorithm:**
```
merged = map[string]MemEntry{}
for table in l0 (oldest → newest):         // newer writes overwrite older
    table.Scan(each entry):
        if not tombstone and not expired:
            merged[key] = entry
        else:
            delete(merged, key)             // drop tombstones

write merged as new SSTable
delete old SSTable files
```

---

### 2.8 Replication Log (`internal/replication/log.go`)

```
ReplicationLog
├── mu      sync.RWMutex
├── entries []LogEntry        — ordered by Index
└── nextIdx uint64            — monotonically increasing

LogEntry
├── Index     uint64          — sequence number
├── Op        OpType          — SET | DEL
├── Key       string
├── Value     string
├── ExpiresAt time.Time
└── Timestamp time.Time
```

**Since(fromIndex)** — linear scan returning entries where `Index > fromIndex`. For large logs, this would be replaced with a binary search or a ring buffer with an index map.

---

### 2.9 Primary (`internal/replication/primary.go`)

**Endpoints:**

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/replication/sync` | POST | Replica pulls log entries since `from_index` |
| `/replication/heartbeat` | POST | Replica signals liveness + reports last_index |
| `/replication/status` | GET | Admin: per-replica lag view |

**Lag calculation:**
```
lag = commit_index - replica.last_index
```

Zero lag means the replica is fully caught up. High lag means the replica is falling behind (slow network, crashed, or restarting).

---

### 2.10 Replica (`internal/replication/replica.go`)

**Two background goroutines:**

```
pullLoop (every 500ms):
    POST /replication/sync {from_index: lastIndex}
    for each entry in response:
        apply(entry)               → store.SetWithExpiry or store.Delete
        lastIndex = entry.Index

heartbeatLoop (every 1s):
    POST /replication/heartbeat {replica_addr, last_index}
    on success: lastHeartbeat = now
    on failure: checkFailover()
        if time.Since(lastHeartbeat) > 5s:
            promoted = true        → replica takes over as primary
```

**Failover**: purely timeout-based. No Raft, no quorum. Simple and sufficient for a two-node setup. Multi-node would require a consensus algorithm to avoid split-brain.

---

## 3. Sequence Diagrams

### Normal Write + Replication

```
Client          Primary                    Replica
  │                │                          │
  │──POST /set────►│                          │
  │                │─store.Set()              │
  │                │─log.Append(index=N)      │
  │◄─200 OK────────│                          │
  │                │                          │
  │                │         ◄──POST /sync────│  (500ms tick)
  │                │──SyncResponse{entries}──►│
  │                │                          │─apply(entry)
  │                │                          │─lastIndex = N
  │                │                          │
  │                │         ◄──POST /hb──────│  (1s tick)
  │                │──HeartbeatResponse──────►│
```

### Failover

```
Client          Primary (dead)             Replica
  │                ✗                          │
  │                │                          │──POST /heartbeat → FAIL
  │                │                          │──POST /heartbeat → FAIL
  │                │                          │  [5s elapsed]
  │                │                          │──promoted = true
  │                │                          │
  │──GET /health──────────────────────────────►│
  │◄──{promoted:true}─────────────────────────│
  │                                           │
  │──POST /set────────────────────────────────►│  (redirect writes here)
```

---

## 4. Interface Contracts

### WriteNode interface (avoids circular imports)

```go
type WriteNode interface {
    WriteSet(key, value string, ttl time.Duration)
    WriteDelete(key string)
}
```

`replication.Primary` implements this. `api.PrimaryHandler` depends on `WriteNode`, not on `replication.Primary` directly. This keeps the API package free of replication logic.

---

## 5. Goroutine Map

| Goroutine | Owner | Lifecycle |
|-----------|-------|-----------|
| HTTP server | `cmd/*` | Until SIGINT |
| TTL sweeper | `ttl.Cleaner` | Start() → Stop() |
| Replica pull loop | `replication.Replica` | Start() → Stop() |
| Replica heartbeat loop | `replication.Replica` | Start() → Stop() |

All goroutines are stopped on graceful shutdown via `Stop()` or `close(stop)`. No goroutine leaks.
