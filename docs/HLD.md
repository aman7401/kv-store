# High Level Design — Distributed Key-Value Store

## 1. Problem Statement

Design a distributed key-value store that:
- Supports Get / Set / Delete with optional TTL
- Survives process crashes (durability)
- Scales reads via replication
- Handles primary failure with automatic failover
- Supports two pluggable storage engines (HashMap and LSM Tree)

---

## 2. System Overview

```
┌────────────────────────────────────────────────────────────────────┐
│                          Clients (curl / SDK)                       │
└──────────────┬─────────────────────────────────┬───────────────────┘
               │  Writes                          │  Reads
               ▼                                  ▼
┌──────────────────────────┐         ┌────────────────────────────┐
│       PRIMARY NODE        │         │       REPLICA NODE(s)       │
│  :8080                    │         │  :8081, :8082, ...          │
│                           │         │                             │
│  ┌─────────────────────┐  │         │  ┌─────────────────────┐   │
│  │    HTTP API Layer    │  │         │  │  Read-only HTTP API  │   │
│  │  /get /set /delete   │  │         │  │  /get /health        │   │
│  └────────┬────────────┘  │         │  └─────────┬───────────┘   │
│           │                │         │            │                │
│  ┌────────▼────────────┐  │         │  ┌─────────▼───────────┐   │
│  │   Storage Engine     │  │         │  │   Local Store Copy   │   │
│  │  HashMap or LSM Tree │  │         │  │  (eventually sync'd) │   │
│  └────────┬────────────┘  │         │  └─────────┬───────────┘   │
│           │                │         │            │                │
│  ┌────────▼────────────┐  │  pull   │  ┌─────────▼───────────┐   │
│  │  Replication Log     │◄─┼─────── │  │   Replication Pull   │   │
│  │  (ordered mutations) │  │  sync  │  │   Loop (500ms tick)  │   │
│  └────────┬────────────┘  │         │  └─────────┬───────────┘   │
│           │                │         │            │                │
│  ┌────────▼────────────┐  │         │  ┌─────────▼───────────┐   │
│  │    WAL (disk)        │  │         │  │  Heartbeat Monitor   │   │
│  │  crash recovery      │  │         │  │  failover detection  │   │
│  └─────────────────────┘  │         │  └─────────────────────┘   │
└──────────────────────────┘         └────────────────────────────┘
```

---

## 3. Key Design Decisions

### 3.1 Two Storage Engines

| Property | HashMap Engine | LSM Tree Engine |
|----------|---------------|-----------------|
| Read speed | O(1) in-memory | O(log N) with bloom filter |
| Write speed | O(1) | O(1) amortized (memtable) |
| Disk usage | WAL only | SSTables + compaction |
| Crash recovery | WAL replay | SSTable persistence |
| Use case | Low-latency cache | High write throughput, large datasets |

### 3.2 Write Path (Primary)

```
Client SET
  → HTTP Handler
  → primary.WriteSet()        ← single entry point, no bypass
      → store.Set()           ← updates in-memory state
      → replicationLog.Append() ← index-ordered mutation log
  → 200 OK
```

All writes go through `WriteSet/WriteDelete` on the Primary — this is the critical invariant that keeps the replication log complete.

### 3.3 Replication (Async Pull)

- Replicas poll `/replication/sync` every 500ms with their `last_index`
- Primary returns all log entries since that index
- Replica applies them in order and advances `last_index`
- This is **asynchronous** — replicas may lag behind primary

**Trade-off**: we chose availability over consistency (AP in CAP theorem). Reads from replicas may be stale.

### 3.4 Failover

- Replica sends heartbeat to primary every 1 second
- If heartbeat fails and `time.Since(lastHeartbeat) > 5s`, replica sets `promoted=true`
- External traffic can be redirected to the promoted replica via `/health` polling

### 3.5 TTL

- Each entry stores an absolute `expiresAt` timestamp (not duration)
- Expiry is checked lazily on `Get()` — no timer per key
- Background sweeper goroutine evicts expired keys every 5 seconds
- WAL records `expiresAt` so recovery skips already-expired entries

---

## 4. Data Flow Diagrams

### Write Flow (HashMap engine)

```
Client ──POST /set──► HTTP Handler
                           │
                           ▼
                    store.Set(k, v, ttl)    ← in-memory O(1)
                           │
                           ▼
                    wal.WriteSet(k, v, expiresAt)  ← fsync to disk
                           │
                           ▼
                    replicationLog.Append()  ← in-memory ordered log
                           │
                           ▼
                    200 OK ◄──────────────────────────────────────────
```

### Write Flow (LSM engine)

```
Client ──POST /set──► HTTP Handler
                           │
                           ▼
                    lsm.Set(k, v, ttl)
                           │
                           ▼
                    MemTable.Set()           ← sorted in-memory map
                           │
                    [if size > 4MB]
                           │
                           ▼
                    WriteSSTable()           ← flush to disk (immutable file)
                           │
                    [if L0 count > 8]
                           │
                           ▼
                    Compact()                ← merge SSTables, drop tombstones
```

### Read Flow (LSM engine)

```
Client ──GET /get?key=foo──►
                           │
                           ▼
                    MemTable.Get(key)        ← check newest writes first
                    [miss]
                           │
                           ▼
                    for each L0 SSTable (newest → oldest):
                        bloom.Contains(key)  ← if false, skip entire file
                        [maybe present]
                           │
                           ▼
                        binary search index  ← O(log N) in index array
                           │
                           ▼
                        seek to offset       ← single disk read
                           │
                           ▼
                    return value / not found
```

---

## 5. Capacity Estimates

Assuming 10,000 requests/sec, average key=32 bytes, value=256 bytes:

| Metric | Estimate |
|--------|----------|
| Write throughput | ~10K ops/sec |
| WAL write size | ~3 MB/sec |
| MemTable flush frequency (4MB) | ~every 1.3s |
| Replication lag (pull every 500ms) | < 1 second |
| Bloom filter memory (1M keys, 1% FP) | ~1.2 MB |

---

## 6. Failure Modes

| Failure | Behavior |
|---------|----------|
| Primary crash | WAL replayed on restart; replicas promote after 5s timeout |
| Replica crash | No data loss; replica re-syncs from primary on restart |
| Network partition | Replica reads stale data; promotes after 5s |
| Partial WAL write | Corrupt tail record skipped (incomplete JSON) |
| SSTable corruption | Detected on open; compaction creates new clean file |
