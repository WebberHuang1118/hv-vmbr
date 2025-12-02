package find

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/webberhuang/hv-vmbr/pkg/k8s"
	"github.com/webberhuang/hv-vmbr/pkg/manifests"
)

// ErrSnapshotNotFound is returned when a snapshot is not found.
var ErrSnapshotNotFound = errors.New("snapshot not found")

// checkSnapshotTags checks if any snapshot contains the specified tags and returns the matching snapshot's ID.
func checkSnapshotTags(snapshots []Snapshot, namespace, snapshot string) (string, bool) {
	targetTagNS := fmt.Sprintf("ns=%s", namespace)
	targetTagSN := fmt.Sprintf("sn=%s", snapshot)

	for _, snap := range snapshots {
		for _, tag := range snap.Tags {
			if tag == targetTagNS || tag == targetTagSN {
				return snap.ShortID, true
			}
		}
	}
	return "", false
}

// RunFind creates and executes a job to run "restic snapshots" with tags based on
// the provided findNS and findSN values. It waits for the job to complete,
// retrieves its logs (which are expected to be JSON), and unmarshals the JSON into a slice
// of Snapshot. It also checks if the specified tags are found in the snapshots.
func RunFind(namespace, snapshot, awsID, awsSecret, repository, password string) (string, error) {
	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		return "", fmt.Errorf("failed to generate job suffix for find job: %w", err)
	}
	jobName := "find-snapshots-" + jobSuffix

	findRepls := map[string]string{
		"AWS_ACCESS_KEY_ID":     awsID,
		"AWS_SECRET_ACCESS_KEY": awsSecret,
		"RESTIC_REPOSITORY":     repository,
		"RESTIC_PASSWORD":       password,
		"SNAPSHOT_NAME":         snapshot,
	}

	if err := k8s.ApplyManifest(manifests.FindJob, namespace, jobName, findRepls); err != nil {
		return "", fmt.Errorf("failed to apply find job manifest: %w", err)
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
		return "", fmt.Errorf("find job did not complete: %w", err)
	}

	var logs string
	select {
	case logs = <-logCh:
	case err := <-errCh:
		return "", fmt.Errorf("failed to retrieve job logs: %w", err)
	case <-time.After(10 * time.Second):
		return "", fmt.Errorf("timed out waiting for job logs")
	}

	var snapshots []Snapshot
	if err := json.Unmarshal([]byte(logs), &snapshots); err != nil {
		return "", fmt.Errorf("failed to unmarshal JSON output: %w", err)
	}

	// Use checkSnapshotTags to check if any snapshot matches, and return the snapshot's shortID if found.
	if shortID, found := checkSnapshotTags(snapshots, namespace, snapshot); found {
		return shortID, nil
	}

	return "", ErrSnapshotNotFound
}
