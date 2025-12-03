package backup

import (
	"errors"
	"log"
	"time"

	"github.com/webberhuang/hv-vmbr/pkg/find"
	"github.com/webberhuang/hv-vmbr/pkg/k8s"
	"github.com/webberhuang/hv-vmbr/pkg/manifests"
)

type backupContext struct {
	namespace       string
	pvcName         string
	snapshot        string
	vsc             string
	awsID           string
	awsSecret       string
	repository      string
	password        string
	vsName          string
	clonePVCName    string
	vsCreated       bool
	pvcCloneCreated bool
}

func (b *backupContext) cleanup() {
	k8s.CleanupResources(b.namespace, b.vsName, b.clonePVCName, b.vsCreated, b.pvcCloneCreated)
}

func (b *backupContext) fatalCleanup(format string, args ...interface{}) {
	b.cleanup()
	log.Fatalf(format, args...)
}

// RunBackup executes the backup workflow for a given namespace and PVC.
func RunBackup(namespace, pvcName, snapshot, vsc, awsID, awsSecret, repository, password string, repoInitialized bool) {
	ctx := &backupContext{
		namespace:    namespace,
		pvcName:      pvcName,
		snapshot:     snapshot,
		vsc:          vsc,
		awsID:        awsID,
		awsSecret:    awsSecret,
		repository:   repository,
		password:     password,
		vsName:       pvcName + "-vs",
		clonePVCName: pvcName + "-clone",
	}

	checkExistingBackup(ctx, repoInitialized)
	createVolumeSnapshot(ctx)
	createClonePVC(ctx)
	pvName := getPVName(ctx)
	initializeRepository(ctx, repoInitialized)
	runBackupJob(ctx, pvName)

	log.Println("✅ Backup completed successfully.")
	ctx.cleanup()
}

func checkExistingBackup(ctx *backupContext, repoInitialized bool) {
	if !repoInitialized {
		return
	}
	snapshotID, err := find.RunFindByID(ctx.namespace, ctx.snapshot, ctx.awsID, ctx.awsSecret, ctx.repository, ctx.password)
	if err == nil {
		log.Fatalf("❌ Found existing backup snapshotID %s with same tags ns %s snapshot %s", snapshotID, ctx.namespace, ctx.snapshot)
	}
	if !errors.Is(err, find.ErrSnapshotNotFound) {
		log.Fatalf("❌ Failed to check %v current backup with ns %s snapshot %s", err, ctx.namespace, ctx.snapshot)
	}
}

func createVolumeSnapshot(ctx *backupContext) {
	vsRepls := map[string]string{
		"PVC_NAME":                  ctx.pvcName,
		"VOLUME_SNAPSHOT_CLASSNAME": ctx.vsc,
	}
	if err := k8s.ApplyManifest(manifests.VolumeSnapshot, ctx.namespace, ctx.vsName, vsRepls); err != nil {
		ctx.fatalCleanup("❌ Failed to create VolumeSnapshot: %v", err)
	}
	ctx.vsCreated = true

	log.Printf("⌛ Waiting for VolumeSnapshot %s to be ready...", ctx.vsName)
	if err := k8s.WaitForVolumeSnapshot(ctx.vsName, ctx.namespace, 300*time.Second); err != nil {
		ctx.fatalCleanup("❌ VolumeSnapshot %s not ready: %v", ctx.vsName, err)
	}
}

func createClonePVC(ctx *backupContext) {
	sc, err := k8s.GetPVCStorageClass(ctx.pvcName, ctx.namespace)
	if err != nil {
		ctx.fatalCleanup("❌ Failed to get storage class: %v", err)
	}
	ssize, err := k8s.GetPVCStorageSize(ctx.pvcName, ctx.namespace)
	if err != nil {
		ctx.fatalCleanup("❌ Failed to get storage size: %v", err)
	}
	vmode, err := k8s.GetPVCVolumeMode(ctx.pvcName, ctx.namespace)
	if err != nil {
		ctx.fatalCleanup("❌ Failed to get volume mode: %v", err)
	}

	cloneRepls := map[string]string{
		"VOLUME_MODE":          vmode,
		"STORAGE_CLASS":        sc,
		"STORAGE_SIZE":         ssize,
		"VOLUME_SNAPSHOT_NAME": ctx.vsName,
	}
	if err := k8s.ApplyManifest(manifests.PVCClone, ctx.namespace, ctx.clonePVCName, cloneRepls); err != nil {
		ctx.fatalCleanup("❌ Failed to create PVC clone: %v", err)
	}
	ctx.pvcCloneCreated = true

	log.Printf("✅ PVC clone %s created successfully", ctx.clonePVCName)
}

func getPVName(ctx *backupContext) string {
	pvName, err := k8s.GetPVCVolumeName(ctx.pvcName, ctx.namespace)
	if err != nil {
		ctx.fatalCleanup("❌ Failed to get PV name: %v", err)
	}
	return pvName
}

func initializeRepository(ctx *backupContext, repoInitialized bool) {
	if repoInitialized {
		log.Println("✅ Restic repository already initialized.")
		return
	}

	log.Println("🔧 Restic repository not initialized. Applying init job...")
	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		log.Fatalf("❌ Failed to generate job suffix for init job: %v", err)
	}

	initRepls := map[string]string{
		"AWS_ACCESS_KEY_ID":     ctx.awsID,
		"AWS_SECRET_ACCESS_KEY": ctx.awsSecret,
		"RESTIC_REPOSITORY":     ctx.repository,
		"RESTIC_PASSWORD":       ctx.password,
	}
	if err := k8s.ApplyManifest(manifests.ResticInitJob, ctx.namespace, "restic-init-"+jobSuffix, initRepls); err != nil {
		ctx.fatalCleanup("❌ Failed to apply init job: %v", err)
	}
	if err := k8s.WaitForJob("restic-init-"+jobSuffix, ctx.namespace, 30*time.Second); err != nil {
		ctx.fatalCleanup("❌ Init job did not complete: %v", err)
	}
}

func runBackupJob(ctx *backupContext, pvName string) {
	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		log.Fatalf("❌ Failed to generate job suffix for backup job: %v", err)
	}

	backupRepls := map[string]string{
		"AWS_ACCESS_KEY_ID":     ctx.awsID,
		"AWS_SECRET_ACCESS_KEY": ctx.awsSecret,
		"RESTIC_REPOSITORY":     ctx.repository,
		"RESTIC_PASSWORD":       ctx.password,
		"PVC_NAME":              ctx.clonePVCName,
		"PV_NAME":               pvName,
		"SNAPSHOT_NAME":         ctx.snapshot,
	}
	if err := k8s.ApplyManifest(manifests.BackupJob, ctx.namespace, "block-backup-job-"+jobSuffix, backupRepls); err != nil {
		ctx.fatalCleanup("❌ Failed to apply backup job manifest: %v", err)
	}

	go func() {
		if err := k8s.StreamJobProgressPercentage("block-backup-job-"+jobSuffix, ctx.namespace, "backup", "READ progress:"); err != nil {
			log.Printf("❌ Error streaming backup progress logs: %v", err)
		}
	}()

	log.Println("⌛ Waiting for backup job to complete...")
	if err := k8s.WaitForJob("block-backup-job-"+jobSuffix, ctx.namespace, 3600*time.Second); err != nil {
		ctx.fatalCleanup("❌ Backup job did not complete: %v", err)
	}
}
