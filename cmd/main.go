package main

import (
	"flag"
	"log"
	"time"

	"github.com/webberhuang/hv-vmbr/pkg/backup"
	"github.com/webberhuang/hv-vmbr/pkg/find"
	"github.com/webberhuang/hv-vmbr/pkg/k8s"
	"github.com/webberhuang/hv-vmbr/pkg/manifests"
	"github.com/webberhuang/hv-vmbr/pkg/restore"
	"github.com/webberhuang/hv-vmbr/pkg/vm"
)

func main() {
	// Define command-line flags.
	mode := flag.String("mode", "", "Operation mode: backup, restore, find, vm-backup, or vm-restore")
	pvcName := flag.String("pvc", "", "Name of the PVC to backup or the destination PVC for restore")
	namespace := flag.String("namespace", "backup", "Kubernetes namespace (default: backup)")
	kubeconfig := flag.String("kubeconfig", "", "Path to kubeconfig file (optional)")
	vsc := flag.String("vsc", "my-volumesnapshotclass", "VolumeSnapshotClass name to use")
	awsID := flag.String("awsid", "", "AWS_ACCESS_KEY_ID for restic")
	awsSecret := flag.String("awssecret", "", "AWS_SECRET_ACCESS_KEY for restic")
	repository := flag.String("repository", "", "RESTIC_REPOSITORY value")
	password := flag.String("password", "", "RESTIC_PASSWORD value")
	// For restore mode:
	sourcePV := flag.String("sourcepv", "", "Original source PV name used for the backup (required in restore mode)")
	sourceNs := flag.String("sourcens", "", "Original source PVC namespace used for the backup (required in restore mode)")
	// For backup and find modes, the snapshot tag is required.
	snapshot := flag.String("snapshot", "", "Tag value for snapshot name (used by backup and find subcommands)")
	// For VM backup/restore modes:
	vmName := flag.String("vm", "", "Name of the VirtualMachine to backup or restore")
	backupName := flag.String("backupname", "", "Name for the VM backup (required for vm-backup and vm-restore)")

	flag.Parse()

	// Combined flag checks.
	if *mode != "backup" && *mode != "restore" && *mode != "find" && *mode != "vm-backup" && *mode != "vm-restore" {
		log.Fatal("❌ Please specify -mode=backup, -mode=restore, -mode=find, -mode=vm-backup, or -mode=vm-restore")
	}
	if *namespace == "" {
		log.Fatal("❌ Please provide a valid namespace using -namespace")
	}
	if *awsID == "" || *awsSecret == "" || *repository == "" || *password == "" {
		log.Fatal("❌ Please provide all secret parameters: -awsid, -awssecret, -repository, -password")
	}

	// Mode-specific checks.
	switch *mode {
	case "backup":
		if *pvcName == "" {
			log.Fatal("❌ For backup mode, please provide the PVC name using -pvc")
		}
		if *snapshot == "" {
			log.Fatal("❌ For backup mode, please provide a snapshot tag value using -snapshot")
		}
	case "restore":
		if *pvcName == "" {
			log.Fatal("❌ For restore mode, please provide the destination PVC name using -pvc")
		}
		if *sourcePV == "" {
			log.Fatal("❌ For restore mode, please provide the source PV name using -sourcepv")
		}
		if *snapshot == "" {
			log.Fatal("❌ For restore mode, please provide a snapshot tag value using -snapshot")
		}
	case "find":
		if *snapshot == "" {
			log.Fatal("❌ For find mode, please provide a snapshot tag value using -snapshot")
		}
	case "vm-backup":
		if *vmName == "" {
			log.Fatal("❌ For vm-backup mode, please provide the VM name using -vm")
		}
		if *backupName == "" {
			log.Fatal("❌ For vm-backup mode, please provide a backup name using -backupname")
		}
	case "vm-restore":
		if *backupName == "" {
			log.Fatal("❌ For vm-restore mode, please provide a backup name using -backupname")
		}
		// vmName is optional for restore (will use original name if not specified)
	}

	// Initialize Kubernetes clients.
	if err := k8s.InitK8sClients(*kubeconfig); err != nil {
		log.Fatalf("❌ Error initializing Kubernetes clients: %v", err)
	}

	// Run repository check job for all modes.
	log.Println("🔧 Applying repository check job manifest...")
	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		log.Fatalf("❌ Failed to generate job suffix: %v", err)
	}
	checkRepls := map[string]string{
		"AWS_ACCESS_KEY_ID":     *awsID,
		"AWS_SECRET_ACCESS_KEY": *awsSecret,
		"RESTIC_REPOSITORY":     *repository,
		"RESTIC_PASSWORD":       *password,
	}
	checkJobName := "restic-check-" + jobSuffix
	if err := k8s.ApplyManifest(manifests.ResticCheckJob, *namespace, checkJobName, checkRepls); err != nil {
		log.Fatalf("❌ Failed to apply repository check job manifest: %v", err)
	}
	log.Println("⌛ Waiting for repository check job to complete...")
	checkErr := k8s.WaitForJob(checkJobName, *namespace, 10*time.Second)
	repoInitialized := (checkErr == nil)

	// For restore, find, and vm-restore modes, repository must be initialized.
	if (*mode == "restore" || *mode == "find" || *mode == "vm-restore") && !repoInitialized {
		log.Fatal("❌ Repository is not initialized; cannot run restore, find, or vm-restore subcommand")
	}

	switch *mode {
	case "backup":
		backup.RunBackup(*namespace, *pvcName, *snapshot, *vsc, *awsID, *awsSecret, *repository, *password, repoInitialized)
	case "restore":
		restore.RunRestore(*namespace, *pvcName, *sourceNs, *sourcePV, *snapshot, *awsID, *awsSecret, *repository, *password)
	case "find":
		snapshotID, err := find.RunFind(*namespace, *snapshot, *awsID, *awsSecret, *repository, *password)
		if err != nil {
			log.Fatalf("❌ Find job failed: %v", err)
		}
		if snapshotID != "" {
			log.Printf("✅ Snapshot found with ID: %s", snapshotID)
		} else {
			log.Println("❌ Snapshot not found.")
		}
	case "vm-backup":
		vm.RunVMBackup(*namespace, *vmName, *backupName, *vsc, *awsID, *awsSecret, *repository, *password, repoInitialized)
	case "vm-restore":
		vm.RunVMRestore(*namespace, *vmName, *backupName, *awsID, *awsSecret, *repository, *password)
	}
}
