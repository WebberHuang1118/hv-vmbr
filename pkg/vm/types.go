package vm

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VMBackupConfig represents the complete backup configuration for a VM
type VMBackupConfig struct {
	Name          string         `json:"name"`
	Namespace     string         `json:"namespace"`
	BackupSpec    BackupSpec     `json:"backupSpec"`
	VMSourceSpec  VMSpec         `json:"vmSourceSpec"`
	VolumeBackups []VolumeBackup `json:"volumeBackups"`
	SecretBackups []SecretBackup `json:"secretBackups"`
}

// BackupSpec defines the source of the backup
type BackupSpec struct {
	Source SourceRef `json:"source"`
	Type   string    `json:"type"`
}

// SourceRef references the original VM
type SourceRef struct {
	APIGroup string `json:"apiGroup"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}

// VMSpec represents the sanitized VM specification
type VMSpec struct {
	Metadata metav1.ObjectMeta `json:"metadata"`
	Spec     interface{}       `json:"spec"` // Keep as interface{} to preserve original structure
}

// VolumeBackup represents a backed-up PVC volume
type VolumeBackup struct {
	Name                  string                       `json:"name"`
	VolumeName            string                       `json:"volumeName"`
	CSIDriverName         string                       `json:"csiDriverName"`
	PersistentVolumeClaim corev1.PersistentVolumeClaim `json:"persistentVolumeClaim"`
	ResticSnapshotID      string                       `json:"resticSnapshotID,omitempty"` // Our addition for restic
	VolumeSize            int64                        `json:"volumeSize"`
	Progress              int                          `json:"progress"`
}

// SecretBackup represents a backed-up Secret
type SecretBackup struct {
	Name string            `json:"name"`
	Data map[string]string `json:"data"`
}
