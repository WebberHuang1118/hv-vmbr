package find

import (
	"time"
)

//This file needs to be synced with https://github.com/restic/restic/blob/v0.17.3/cmd/restic/cmd_snapshots.go#L319-L330

// SnapshotSummary holds summary statistics from restic snapshots.
type SnapshotSummary struct {
	BackupStart         time.Time `json:"backup_start"`
	BackupEnd           time.Time `json:"backup_end"`
	FilesNew            uint      `json:"files_new"`
	FilesChanged        uint      `json:"files_changed"`
	FilesUnmodified     uint      `json:"files_unmodified"`
	DirsNew             uint      `json:"dirs_new"`
	DirsChanged         uint      `json:"dirs_changed"`
	DirsUnmodified      uint      `json:"dirs_unmodified"`
	DataBlobs           int       `json:"data_blobs"`
	TreeBlobs           int       `json:"tree_blobs"`
	DataAdded           uint64    `json:"data_added"`
	DataAddedPacked     uint64    `json:"data_added_packed"`
	TotalFilesProcessed uint      `json:"total_files_processed"`
	TotalBytesProcessed uint64    `json:"total_bytes_processed"`
}

// Snapshot represents a restic snapshot.
type SnapshotNested struct {
	Time           time.Time        `json:"time"`
	Parent         string           `json:"parent,omitempty"`
	Tree           string           `json:"tree"`
	Paths          []string         `json:"paths"`
	Hostname       string           `json:"hostname,omitempty"`
	Username       string           `json:"username,omitempty"`
	UID            uint32           `json:"uid,omitempty"`
	GID            uint32           `json:"gid,omitempty"`
	Excludes       []string         `json:"excludes,omitempty"`
	Tags           []string         `json:"tags,omitempty"`
	Original       string           `json:"original,omitempty"`
	ProgramVersion string           `json:"program_version,omitempty"`
	Summary        *SnapshotSummary `json:"summary,omitempty"`

	id *ID // plaintext ID, used during restore
}

type Snapshot struct {
	*SnapshotNested

	ID      *ID    `json:"id"`
	ShortID string `json:"short_id"`
}

// SnapshotGroupKey is used to group snapshots.
type SnapshotGroupKey struct {
	Hostname string   `json:"hostname"`
	Paths    []string `json:"paths"`
	Tags     []string `json:"tags"`
}

// SnapshotGroup groups snapshots with a common key.
type SnapshotGroup struct {
	GroupKey  SnapshotGroupKey `json:"group_key"`
	Snapshots []Snapshot       `json:"snapshots"`
}
