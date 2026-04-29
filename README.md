# kv-store

A distributed key-value store built from scratch in Go — a mini Redis/DynamoDB with real internals.

## Phases

| Phase | Focus | Status |
|-------|-------|--------|
| 1 | Single-node KV store (HashMap + WAL + HTTP + TTL) | 🔨 In Progress |
| 2 | LSM Tree storage engine (MemTable + SSTables + Compaction) | 📋 Planned |
| 3 | Replication (Primary/Replica + Failover) | 📋 Planned |

## Quick Start

```bash
# Run the server
go run cmd/server/main.go

# Set a key
curl -X POST http://localhost:8080/set -d '{"key":"foo","value":"bar"}'

# Get a key
curl http://localhost:8080/get?key=foo

# Set with TTL (seconds)
curl -X POST http://localhost:8080/set -d '{"key":"session","value":"abc","ttl":60}'

# Delete a key
curl -X DELETE http://localhost:8080/delete?key=foo
```

## Architecture

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for deep-dive design notes.
