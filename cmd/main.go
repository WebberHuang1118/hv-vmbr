package main

import (
	"flag"
	"log"
	"strings"
	"time"

	"github.com/webberhuang/hv-vmbr/pkg/find"
	"github.com/webberhuang/hv-vmbr/pkg/k8s"
	"github.com/webberhuang/hv-vmbr/pkg/manifests"
	"github.com/webberhuang/hv-vmbr/pkg/vm"
)

// tagsFlag allows multiple -tag flags
type tagsFlag []string

func (t *tagsFlag) String() string {
	return strings.Join(*t, ",")
}

func (t *tagsFlag) Set(value string) error {
	*t = append(*t, value)
	return nil
}

func main() {
	// Define command-line flags.
	mode := flag.String("mode", "", "Operation mode: find, vm-backup, or vm-restore")
	namespace := flag.String("namespace", "backup", "Kubernetes namespace (default: backup)")
	kubeconfig := flag.String("kubeconfig", "", "Path to kubeconfig file (optional)")
	vsc := flag.String("vsc", "my-volumesnapshotclass", "VolumeSnapshotClass name to use")
	awsID := flag.String("awsid", "", "AWS_ACCESS_KEY_ID for restic")
	awsSecret := flag.String("awssecret", "", "AWS_SECRET_ACCESS_KEY for restic")
	repository := flag.String("repository", "", "RESTIC_REPOSITORY value")
	password := flag.String("password", "", "RESTIC_PASSWORD value")
	// For find mode, tags are optional and can be specified multiple times
	var tags tagsFlag
	flag.Var(&tags, "tag", "Tag for filtering snapshots (can be specified multiple times, e.g., -tag ns=backup -tag sn=vm1-b). If not specified, lists all snapshots.")
	// For VM backup/restore modes:
	vmName := flag.String("vm", "", "Name of the VirtualMachine to backup or restore")
	backupName := flag.String("backupname", "", "Name for the VM backup (required for vm-backup and vm-restore)")

	flag.Parse()

	// Combined flag checks.
	if *mode != "find" && *mode != "vm-backup" && *mode != "vm-restore" {
		log.Fatal("❌ Please specify -mode=find, -mode=vm-backup, or -mode=vm-restore")
	}
	if *namespace == "" {
		log.Fatal("❌ Please provide a valid namespace using -namespace")
	}
	if *awsID == "" || *awsSecret == "" || *repository == "" || *password == "" {
		log.Fatal("❌ Please provide all secret parameters: -awsid, -awssecret, -repository, -password")
	}

	// Mode-specific checks.
	switch *mode {
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

	// For find and vm-restore modes, repository must be initialized.
	if (*mode == "find" || *mode == "vm-restore") && !repoInitialized {
		log.Fatal("❌ Repository is not initialized; cannot run find or vm-restore subcommand")
	}

	switch *mode {
	case "find":
		snapshots, err := find.RunFind(*namespace, tags, *awsID, *awsSecret, *repository, *password)
		if err != nil {
			log.Fatalf("❌ Find job failed: %v", err)
		}
		if len(snapshots) > 0 {
			log.Printf("✅ Found %d snapshot(s):", len(snapshots))
			for _, snap := range snapshots {
				log.Printf("  ID: %s, Time: %s, Tags: %v", snap.ShortID, snap.Time.Format("2006-01-02 15:04:05"), snap.Tags)
			}
		} else {
			log.Println("❌ No snapshots found.")
		}
	case "vm-backup":
		vm.RunVMBackup(*namespace, *vmName, *backupName, *vsc, *awsID, *awsSecret, *repository, *password, repoInitialized)
	case "vm-restore":
		vm.RunVMRestore(*namespace, *vmName, *backupName, *awsID, *awsSecret, *repository, *password)
	}
}
