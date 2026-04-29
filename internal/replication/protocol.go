package replication

import "time"

// OpType mirrors WAL op types for the replication wire protocol.
type OpType string

const (
	OpSet    OpType = "SET"
	OpDelete OpType = "DEL"
)

// LogEntry is a single replication event sent from primary to replicas.
// The Index field is a monotonically increasing sequence number used for
// replica catch-up: a replica requests all entries after its last known index.
type LogEntry struct {
	Index     uint64    `json:"index"`
	Op        OpType    `json:"op"`
	Key       string    `json:"key"`
	Value     string    `json:"value,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Timestamp time.Time `json:"ts"`
}

// ReplicaState describes what a replica reports to the primary.
type ReplicaState struct {
	Addr      string    `json:"addr"`
	LastIndex uint64    `json:"last_index"`
	UpdatedAt time.Time `json:"updated_at"`
}

// HeartbeatRequest is sent by replicas to the primary to signal liveness.
type HeartbeatRequest struct {
	ReplicaAddr string `json:"replica_addr"`
	LastIndex   uint64 `json:"last_index"`
}

// HeartbeatResponse includes the current primary's commit index.
type HeartbeatResponse struct {
	PrimaryAddr  string `json:"primary_addr"`
	CommitIndex  uint64 `json:"commit_index"`
	IsPrimary    bool   `json:"is_primary"`
}

// SyncRequest asks the primary for all log entries after FromIndex.
type SyncRequest struct {
	FromIndex uint64 `json:"from_index"`
}

// SyncResponse contains the log entries and current commit index.
type SyncResponse struct {
	Entries     []LogEntry `json:"entries"`
	CommitIndex uint64     `json:"commit_index"`
}
