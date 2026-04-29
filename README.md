# kv-store

A distributed key-value store built from scratch in Go — a mini Redis/DynamoDB with real internals.

## Phases

| Phase | Focus | Status |
|-------|-------|--------|
| 1 | Single-node KV (HashMap + WAL + TTL + HTTP) | ✅ Complete |
| 2 | LSM Tree engine (MemTable + SSTables + Bloom + Compaction) | ✅ Complete |
| 3 | Replication (Primary/Replica + Async Pull + Failover) | ✅ Complete |

---

## Quick Start

### Phase 1 — HashMap engine (with WAL)

```bash
# Start server
go run cmd/server/main.go --engine=hashmap --addr=:8080

# Set a key
curl -X POST http://localhost:8080/set \
  -H 'Content-Type: application/json' \
  -d '{"key":"foo","value":"bar"}'

# Set with TTL (60 seconds)
curl -X POST http://localhost:8080/set \
  -d '{"key":"session","value":"abc","ttl":60}'

# Get a key
curl http://localhost:8080/get?key=foo

# Delete
curl -X DELETE http://localhost:8080/delete?key=foo

# Health
curl http://localhost:8080/health
```

### Phase 2 — LSM Tree engine

```bash
go run cmd/server/main.go --engine=lsm --addr=:8080

# Same GET/SET/DELETE API, plus:
curl -X POST http://localhost:8080/flush    # flush MemTable → SSTable
curl -X POST http://localhost:8080/compact  # merge L0 SSTables
curl http://localhost:8080/health           # shows memtable_keys, l0_tables
```

### Phase 3 — Replication

```bash
# Terminal 1: start primary
go run cmd/primary/main.go --addr=:8080

# Terminal 2: start replica (points to primary)
go run cmd/replica/main.go --addr=:8081 --primary=localhost:8080

# Write to primary
curl -X POST http://localhost:8080/set -d '{"key":"x","value":"hello"}'

# Read from replica (after replication lag)
curl http://localhost:8081/get?key=x

# Check replication lag
curl http://localhost:8080/replication/status

# Health check on replica (shows promoted=true if primary died)
curl http://localhost:8081/health
```

---

## Architecture

### Phase 1: Single-Node KV Store

```
Client
  │
  ▼
HTTP API (/get /set /delete /health)
  │          │
  │          ▼
  │     WAL (write-ahead log)
  │       • append-only JSON lines
  │       • fsync after each write
  │       • replayed on startup
  │
  ▼
In-Memory HashMap (sync.RWMutex)
  • O(1) Get/Set/Delete
  • entry = {value, expiresAt}
  • Background TTL sweeper (goroutine, 5s interval)
```

**WAL format** — one JSON record per line:
```json
{"op":"SET","key":"foo","value":"bar","expires_at":"0001-01-01T00:00:00Z","ts":"2024-..."}
{"op":"DEL","key":"foo","ts":"2024-..."}
```

**Crash recovery**: on startup, replay the WAL top-to-bottom, skipping records whose `expires_at` is already past.

---

### Phase 2: LSM Tree Storage Engine

This is the same engine used by RocksDB, Cassandra, and LevelDB.

```
Write Path:
  Set("k","v") → MemTable (sorted in-memory map)
                     │
                     │ (size threshold: 4MB)
                     ▼
                SSTable file (immutable, sorted, binary)
                     │
                     │ (L0 table count threshold: 8)
                     ▼
              Compaction (merge + dedup + drop tombstones)

Read Path:
  Get("k") → 1. MemTable (O(1) map lookup)
               2. L0 SSTables, newest → oldest
                    └─ Bloom filter check (skip if definitely absent)
                    └─ Binary search on index → seek to record
```

**SSTable file format:**
```
[4-byte length][JSON record bytes] × N
[index JSON]                         ← offsets for binary search
[8-byte index offset]                ← last 8 bytes of file
```

**Key design decisions:**
- Tombstones (deleted keys) are written as `{Tombstone: true}` entries, shadow older values during read
- Compaction drops tombstones and expired TTL entries, reclaiming space
- Bloom filter (1% false positive rate) avoids disk reads for missing keys
- SSTables are immutable — safe to read concurrently, no locking needed

---

### Phase 3: Replication

```
Primary (:8080)                    Replica (:8081)
────────────────                   ────────────────
WriteSet(k,v)                      pullLoop() [every 500ms]
  → store.Set()                      → POST /replication/sync
  → replicationLog.Append()              { from_index: lastIndex }
                                         ← { entries: [...], commit_index: N }
POST /replication/sync               → apply(entry) for each
  ← SyncResponse{entries, index}         → store.SetWithExpiry()
                                         → lastIndex++
POST /replication/heartbeat          heartbeatLoop() [every 1s]
  ← HeartbeatResponse{commit_index}    → POST /replication/heartbeat
                                            { replica_addr, last_index }

GET /replication/status              GET /health
  ← [{addr, last_index, lag}...]       ← {role, promoted, last_index, ...}
```

**Failover**: if a replica's heartbeat to the primary fails and `lastHeartbeat` is older than 5 seconds, the replica sets `promoted=true`. You can detect this via `GET /health` on the replica and route writes to it.

**Replication is asynchronous** — replicas may lag. The `/replication/status` endpoint on the primary shows per-replica `lag` (commit_index − last_index).

---

## Running All Tests

```bash
go test ./... -v
```

| Package | Tests |
|---------|-------|
| `internal/store` | Set, Get, Delete, TTL expiry, overwrite |
| `internal/wal` | Write, replay, missing file, corrupt tail, truncate |
| `internal/lsm` | MemTable, SSTable flush, tombstone, compaction, TTL drop |
| `internal/replication` | Log ordering, trim, apply, failover promotion |

---

## Project Structure

```
kv-store/
├── cmd/
│   ├── server/       # Phase 1 & 2 server (--engine=hashmap|lsm)
│   ├── primary/      # Phase 3 primary node
│   └── replica/      # Phase 3 replica node
├── internal/
│   ├── store/        # Thread-safe in-memory hashmap
│   ├── wal/          # Write-ahead log
│   ├── ttl/          # Background expiry sweeper
│   ├── api/          # HTTP handlers (hashmap + LSM)
│   ├── lsm/          # MemTable, SSTable, Bloom filter, LSM Tree
│   └── replication/  # Protocol types, ReplicationLog, Primary, Replica
└── README.md
```
