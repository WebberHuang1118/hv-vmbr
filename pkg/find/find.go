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
