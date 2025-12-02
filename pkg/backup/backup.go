package backup

import (
	"errors"
	"log"
	"time"

	"github.com/webberhuang/hv-vmbr/pkg/find"
	"github.com/webberhuang/hv-vmbr/pkg/k8s"
	"github.com/webberhuang/hv-vmbr/pkg/manifests"
)

// RunBackup executes the backup workflow for a given namespace and PVC.
// It performs the following steps:
// 1. Checks for existing backup snapshots if the repository is initialized.
// 2. Creates a VolumeSnapshot for the specified PVC.
// 3. Creates a clone PVC from the VolumeSnapshot.
// 4. Initializes the Restic repository if not already initialized.
// 5. Runs the backup job and streams its progress.
func RunBackup(namespace, pvcName, snapshot, vsc, awsID, awsSecret, repository, password string, repoInitialized bool) {
	// Define resource names and initialization flags.
	vsName := pvcName + "-vs"          // Name for the VolumeSnapshot.
	clonePVCName := pvcName + "-clone" // Name for the cloned PVC.
	vsCreated := false                 // Tracks if the VolumeSnapshot was created.
	pvcCloneCreated := false           // Tracks if the cloned PVC was created.

	// Check for existing backup snapshots if the repository is initialized.
	if repoInitialized {
		snapshotID, err := find.RunFindByID(namespace, snapshot, awsID, awsSecret, repository, password)
		if err == nil {
			log.Fatalf("❌ Found existing backup snapshotID %s with same tags ns %s snapshot %s", snapshotID, namespace, snapshot)
		}
		if !errors.Is(err, find.ErrSnapshotNotFound) {
			log.Fatalf("❌ Failed to check %v current backup with ns %s snapshot %s", err, namespace, snapshot)
		}
	}

	// Step 1: Create VolumeSnapshot.
	// The VolumeSnapshot manifest uses tokens for PVC_NAME and VOLUME_SNAPSHOT_CLASSNAME.
	vsRepls := map[string]string{
		"PVC_NAME":                  pvcName, // Source PVC name.
		"VOLUME_SNAPSHOT_CLASSNAME": vsc,     // Snapshot class name.
	}
	if err := k8s.ApplyManifest(manifests.VolumeSnapshot, namespace, vsName, vsRepls); err != nil {
		k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
		log.Fatalf("❌ Failed to create VolumeSnapshot: %v", err)
	}
	vsCreated = true

	log.Printf("⌛ Waiting for VolumeSnapshot %s to be ready...", vsName)
	if err := k8s.WaitForVolumeSnapshot(vsName, namespace, 300*time.Second); err != nil {
		k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
		log.Fatalf("❌ VolumeSnapshot %s not ready: %v", vsName, err)
	}

	// Step 2: Create clone PVC.
	// The PVCClone manifest uses tokens for VOLUME_MODE, STORAGE_CLASS, STORAGE_SIZE, and VOLUME_SNAPSHOT_NAME.
	// Retrieve storage class, size, and volume mode of the source PVC.
	sc, err := k8s.GetPVCStorageClass(pvcName, namespace)
	if err != nil {
		k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
		log.Fatalf("❌ Failed to get storage class: %v", err)
	}
	ssize, err := k8s.GetPVCStorageSize(pvcName, namespace)
	if err != nil {
		k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
		log.Fatalf("❌ Failed to get storage size: %v", err)
	}
	vmode, err := k8s.GetPVCVolumeMode(pvcName, namespace)
	if err != nil {
		k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
		log.Fatalf("❌ Failed to get volume mode: %v", err)
	}
	cloneRepls := map[string]string{
		"VOLUME_MODE":          vmode,  // Volume mode of the source PVC.
		"STORAGE_CLASS":        sc,     // Storage class of the source PVC.
		"STORAGE_SIZE":         ssize,  // Storage size of the source PVC.
		"VOLUME_SNAPSHOT_NAME": vsName, // Name of the VolumeSnapshot.
	}
	if err := k8s.ApplyManifest(manifests.PVCClone, namespace, clonePVCName, cloneRepls); err != nil {
		k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
		log.Fatalf("❌ Failed to create PVC clone: %v", err)
	}
	pvcCloneCreated = true

	log.Printf("⌛ Waiting for PVC clone %s to become Bound...", clonePVCName)
	if err := k8s.WaitForPVCBound(clonePVCName, namespace, 300*time.Second); err != nil {
		k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
		log.Fatalf("❌ PVC clone did not become Bound: %v", err)
	}

	// Step 3: Retrieve source PVC's PV name.
	pvName, err := k8s.GetPVCVolumeName(pvcName, namespace)
	if err != nil {
		k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
		log.Fatalf("❌ Failed to get PV name: %v", err)
	}

	// Step 4: Initialize repository if needed.
	if !repoInitialized {
		log.Println("🔧 Restic repository not initialized. Applying init job...")
		jobSuffix, err := k8s.GenerateJobSuffix()
		if err != nil {
			log.Fatalf("❌ Failed to generate job suffix for init job: %v", err)
		}
		// The init job manifest uses tokens for AWS credentials and Restic repository details.
		initRepls := map[string]string{
			"AWS_ACCESS_KEY_ID":     awsID,
			"AWS_SECRET_ACCESS_KEY": awsSecret,
			"RESTIC_REPOSITORY":     repository,
			"RESTIC_PASSWORD":       password,
		}
		if err := k8s.ApplyManifest(manifests.ResticInitJob, namespace, "restic-init-"+jobSuffix, initRepls); err != nil {
			k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
			log.Fatalf("❌ Failed to apply init job: %v", err)
		}
		if err := k8s.WaitForJob("restic-init-"+jobSuffix, namespace, 30*time.Second); err != nil {
			k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
			log.Fatalf("❌ Init job did not complete: %v", err)
		}
	} else {
		log.Println("✅ Restic repository already initialized.")
	}

	// Step 5: Run backup job.
	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		log.Fatalf("❌ Failed to generate job suffix for backup job: %v", err)
	}
	// The backup job manifest uses tokens for AWS credentials, Restic repository details, and PVC information.
	backupRepls := map[string]string{
		"AWS_ACCESS_KEY_ID":     awsID,
		"AWS_SECRET_ACCESS_KEY": awsSecret,
		"RESTIC_REPOSITORY":     repository,
		"RESTIC_PASSWORD":       password,
		"PVC_NAME":              clonePVCName, // Name of the cloned PVC.
		"PV_NAME":               pvName,       // Name of the source PVC's PV.
		"SNAPSHOT_NAME":         snapshot,     // Snapshot name for the backup.
	}
	if err := k8s.ApplyManifest(manifests.BackupJob, namespace, "block-backup-job-"+jobSuffix, backupRepls); err != nil {
		k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
		log.Fatalf("❌ Failed to apply backup job manifest: %v", err)
	}

	// Launch log streaming to capture backup progress.
	go func() {
		if err := k8s.StreamJobProgressPercentage("block-backup-job-"+jobSuffix, namespace, "backup", "READ progress:"); err != nil {
			log.Printf("❌ Error streaming backup progress logs: %v", err)
		}
	}()

	log.Println("⌛ Waiting for backup job to complete...")
	if err := k8s.WaitForJob("block-backup-job-"+jobSuffix, namespace, 3600*time.Second); err != nil {
		k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
		log.Fatalf("❌ Backup job did not complete: %v", err)
	}
	log.Println("✅ Backup completed successfully.")

	// Cleanup resources created during the backup process.
	k8s.CleanupResources(namespace, vsName, clonePVCName, vsCreated, pvcCloneCreated)
}
