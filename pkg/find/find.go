package find

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/webberhuang/hv-vmbr/pkg/k8s"
	"github.com/webberhuang/hv-vmbr/pkg/manifests"
)

// ErrSnapshotNotFound is returned when a snapshot is not found.
var ErrSnapshotNotFound = errors.New("snapshot not found")

// BackupInfo represents detailed information about a backup
type BackupInfo struct {
	BackupName string               `json:"backupName"`
	Namespace  string               `json:"namespace"`
	VMConfig   *BackupSnapshotInfo  `json:"vmConfig,omitempty"`
	PVCBackups []BackupSnapshotInfo `json:"pvcBackups"`
	TotalSize  uint64               `json:"totalSize"`
	BackupTime time.Time            `json:"backupTime"`
}

// BackupSnapshotInfo represents information about a specific snapshot
type BackupSnapshotInfo struct {
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	SnapshotID string    `json:"snapshotId"`
	ShortID    string    `json:"shortId"`
	Time       time.Time `json:"time"`
	Tags       []string  `json:"tags"`
	Paths      []string  `json:"paths"`
	DataAdded  uint64    `json:"dataAdded,omitempty"`
	TotalSize  uint64    `json:"totalSize,omitempty"`
}

// RunFind creates and executes a job to run "restic snapshots" with optional tags.
// If tags are provided, it filters by those tags. Otherwise, it lists all snapshots.
// Returns a slice of matching snapshots.
func RunFind(namespace string, tags []string, awsID, awsSecret, repository, password string) ([]Snapshot, error) {
	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		return nil, fmt.Errorf("failed to generate job suffix for find job: %w", err)
	}
	jobName := "find-snapshots-" + jobSuffix

	// Build the tag filter string
	tagFilter := ""
	if len(tags) > 0 {
		tagFilter = strings.Join(tags, ",")
	}

	findRepls := map[string]string{
		"AWS_ACCESS_KEY_ID":     awsID,
		"AWS_SECRET_ACCESS_KEY": awsSecret,
		"RESTIC_REPOSITORY":     repository,
		"RESTIC_PASSWORD":       password,
		"TAG_FILTER":            tagFilter,
	}

	if err := k8s.ApplyManifest(manifests.FindJob, namespace, jobName, findRepls); err != nil {
		return nil, fmt.Errorf("failed to apply find job manifest: %w", err)
	}

	logCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		logs, err := k8s.GetJobLogs(jobName, namespace, "find")
		if err != nil {
			errCh <- err
			return
		}
		logCh <- logs
	}()

	if err := k8s.WaitForJob(jobName, namespace, 60*time.Second); err != nil {
		return nil, fmt.Errorf("find job did not complete: %w", err)
	}

	var logs string
	select {
	case logs = <-logCh:
	case err := <-errCh:
		return nil, fmt.Errorf("failed to retrieve job logs: %w", err)
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("timed out waiting for job logs")
	}

	var snapshots []Snapshot
	if err := json.Unmarshal([]byte(logs), &snapshots); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON output: %w", err)
	}

	return snapshots, nil
}

// RunFindByID is a helper function that searches for a snapshot by namespace and snapshot name tags,
// and returns the first matching snapshot ID. This is used internally by backup/restore operations.
func RunFindByID(namespace, snapshot, awsID, awsSecret, repository, password string) (string, error) {
	tags := []string{
		fmt.Sprintf("ns=%s", namespace),
		fmt.Sprintf("sn=%s", snapshot),
	}

	snapshots, err := RunFind(namespace, tags, awsID, awsSecret, repository, password)
	if err != nil {
		return "", err
	}

	if len(snapshots) == 0 {
		return "", ErrSnapshotNotFound
	}

	// Return the first matching snapshot's ID
	return snapshots[0].ShortID, nil
}

// RunFindBackupInfo retrieves detailed information about a specific backup
func RunFindBackupInfo(namespace, backupName, awsID, awsSecret, repository, password string) (*BackupInfo, error) {
	backupInfo := &BackupInfo{
		BackupName: backupName,
		Namespace:  namespace,
		PVCBackups: []BackupSnapshotInfo{},
	}

	// Find VM config snapshot
	vmConfigTags := []string{
		fmt.Sprintf("ns=%s", namespace),
		fmt.Sprintf("sn=%s", backupName),
		"type=vm-config",
	}

	vmConfigSnapshots, err := RunFind(namespace, vmConfigTags, awsID, awsSecret, repository, password)
	if err != nil {
		return nil, fmt.Errorf("failed to find VM config: %w", err)
	}

	if len(vmConfigSnapshots) > 0 {
		snap := vmConfigSnapshots[0]
		backupInfo.VMConfig = &BackupSnapshotInfo{
			Type:       "VM Config",
			Name:       backupName,
			SnapshotID: snap.ID.String(),
			ShortID:    snap.ShortID,
			Time:       snap.Time,
			Tags:       snap.Tags,
			Paths:      snap.Paths,
		}
		if snap.Summary != nil {
			backupInfo.VMConfig.DataAdded = snap.Summary.DataAdded
			backupInfo.VMConfig.TotalSize = snap.Summary.TotalBytesProcessed
		}
		backupInfo.BackupTime = snap.Time
		backupInfo.TotalSize += backupInfo.VMConfig.DataAdded
	}

	// Find all PVC snapshots with the backup name prefix
	pvcTagPrefix := fmt.Sprintf("%s-pvc-", backupName)

	// Get all snapshots for this namespace
	nsTags := []string{
		fmt.Sprintf("ns=%s", namespace),
	}

	allSnapshots, err := RunFind(namespace, nsTags, awsID, awsSecret, repository, password)
	if err != nil {
		return nil, fmt.Errorf("failed to find PVC snapshots: %w", err)
	}

	// Filter snapshots that belong to this backup
	for _, snap := range allSnapshots {
		// Check if this snapshot is a PVC backup for our backup name
		isPVCBackup := false
		pvcName := ""

		for _, tag := range snap.Tags {
			if strings.HasPrefix(tag, "sn=") {
				snapshotName := strings.TrimPrefix(tag, "sn=")
				if strings.HasPrefix(snapshotName, pvcTagPrefix) {
					isPVCBackup = true
					pvcName = strings.TrimPrefix(snapshotName, pvcTagPrefix)
					break
				}
			}
		}

		if isPVCBackup {
			pvcInfo := BackupSnapshotInfo{
				Type:       "PVC",
				Name:       pvcName,
				SnapshotID: snap.ID.String(),
				ShortID:    snap.ShortID,
				Time:       snap.Time,
				Tags:       snap.Tags,
				Paths:      snap.Paths,
			}
			if snap.Summary != nil {
				pvcInfo.DataAdded = snap.Summary.DataAdded
				pvcInfo.TotalSize = snap.Summary.TotalBytesProcessed
				backupInfo.TotalSize += pvcInfo.DataAdded
			}
			backupInfo.PVCBackups = append(backupInfo.PVCBackups, pvcInfo)
		}
	}

	if backupInfo.VMConfig == nil && len(backupInfo.PVCBackups) == 0 {
		return nil, fmt.Errorf("no backup found with name: %s", backupName)
	}

	return backupInfo, nil
}
