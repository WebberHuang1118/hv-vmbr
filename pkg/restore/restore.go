package restore

import (
	"log"
	"time"

	"github.com/webberhuang/hv-vmbr/pkg/find"
	"github.com/webberhuang/hv-vmbr/pkg/k8s"
	"github.com/webberhuang/hv-vmbr/pkg/manifests"
)

// RunRestore executes the restore workflow.
func RunRestore(namespace, destPVC, sourceNs, sourcePV, snapshot, awsID, awsSecret, repository, password string) {
	snapshotID, err := find.RunFindByID(sourceNs, snapshot, awsID, awsSecret, repository, password)
	if err != nil {
		log.Fatalf("❌ Failed to find backup %v with ns %s snapshot %s", err, sourceNs, snapshot)
	}

	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		log.Fatalf("❌ Failed to generate job suffix for restore job: %v", err)
	}
	log.Println("🔧 Applying restore job manifest...")
	// For the restore job, the manifest uses default tokens {{NAMESPACE}} and {{NAME}}.
	// We pass the default name as "block-restore-job-" + jobSuffix.
	restoreRepls := map[string]string{
		"AWS_ACCESS_KEY_ID":     awsID,
		"AWS_SECRET_ACCESS_KEY": awsSecret,
		"RESTIC_REPOSITORY":     repository,
		"RESTIC_PASSWORD":       password,
		"PVC_NAME":              destPVC,  // extra token used in command args
		"PV_NAME":               sourcePV, // extra token for the source PV filename
		"SNAPSHOT_ID":           snapshotID,
	}
	if err := k8s.ApplyManifest(manifests.RestoreJob, namespace, "block-restore-job-"+jobSuffix, restoreRepls); err != nil {
		log.Fatalf("❌ Failed to apply restore job manifest: %v", err)
	}

	// Launch log streaming to capture restore progress.
	go func() {
		if err := k8s.StreamJobProgressPercentage("block-restore-job-"+jobSuffix, namespace, "restore", "WRITE progress:"); err != nil {
			log.Printf("❌ Error streaming restore progress logs: %v", err)
		}
	}()

	log.Println("⌛ Waiting for restore job to complete...")
	if err := k8s.WaitForJob("block-restore-job-"+jobSuffix, namespace, 3600*time.Second); err != nil {
		log.Fatalf("❌ Restore job did not complete: %v", err)
	}
	log.Println("✅ Restore completed successfully.")
}
