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

type cliFlags struct {
	mode       string
	namespace  string
	kubeconfig string
	vscMapping string
	awsID      string
	awsSecret  string
	repository string
	password   string
	tags       tagsFlag
	vmName     string
	backupName string
}

func parseFlags() *cliFlags {
	flags := &cliFlags{}
	flag.StringVar(&flags.mode, "mode", "", "Operation mode: find, vm-backup, vm-restore, or cleanup")
	flag.StringVar(&flags.namespace, "namespace", "backup", "Kubernetes namespace (default: backup)")
	flag.StringVar(&flags.kubeconfig, "kubeconfig", "", "Path to kubeconfig file (optional)")
	flag.StringVar(&flags.vscMapping, "vsc", "driver.longhorn.io=longhorn-snapshot,nfs.csi.k8s.io=csi-nfs-snapclass", "Mapping between CSI driver and VolumeSnapshotClass (format: driver1=class1,driver2=class2)")
	flag.StringVar(&flags.awsID, "awsid", "", "AWS_ACCESS_KEY_ID for restic")
	flag.StringVar(&flags.awsSecret, "awssecret", "", "AWS_SECRET_ACCESS_KEY for restic")
	flag.StringVar(&flags.repository, "repository", "", "RESTIC_REPOSITORY value")
	flag.StringVar(&flags.password, "password", "", "RESTIC_PASSWORD value")
	flag.Var(&flags.tags, "tag", "Tag for filtering snapshots (can be specified multiple times, e.g., -tag ns=backup -tag sn=vm1-b). If not specified, lists all snapshots.")
	flag.StringVar(&flags.vmName, "vm", "", "Name of the VirtualMachine to backup or restore")
	flag.StringVar(&flags.backupName, "backupname", "", "Name for the VM backup (required for vm-backup, vm-restore, and cleanup). For find mode, specify this to get detailed backup info.")
	flag.Parse()
	return flags
}

func parseVSCMapping(mappingStr string) map[string]string {
	mapping := make(map[string]string)
	if mappingStr == "" {
		return mapping
	}

	pairs := strings.Split(mappingStr, ",")
	for _, pair := range pairs {
		kv := strings.Split(pair, "=")
		if len(kv) == 2 {
			driver := strings.TrimSpace(kv[0])
			class := strings.TrimSpace(kv[1])
			mapping[driver] = class
		}
	}
	return mapping
}

func validateFlags(flags *cliFlags) {
	if flags.mode != "find" && flags.mode != "vm-backup" && flags.mode != "vm-restore" && flags.mode != "cleanup" {
		log.Fatal("❌ Please specify -mode=find, -mode=vm-backup, -mode=vm-restore, or -mode=cleanup")
	}
	if flags.namespace == "" {
		log.Fatal("❌ Please provide a valid namespace using -namespace")
	}
	if flags.awsID == "" || flags.awsSecret == "" || flags.repository == "" || flags.password == "" {
		log.Fatal("❌ Please provide all secret parameters: -awsid, -awssecret, -repository, -password")
	}

	switch flags.mode {
	case "vm-backup":
		if flags.vmName == "" || flags.backupName == "" {
			log.Fatal("❌ For vm-backup mode, please provide -vm and -backupname")
		}
		if flags.vscMapping == "" {
			log.Fatal("❌ For vm-backup mode, please provide -vsc mapping (format: driver1=class1,driver2=class2)")
		}
	case "vm-restore", "cleanup":
		if flags.backupName == "" {
			log.Fatal("❌ For " + flags.mode + " mode, please provide -backupname")
		}
	}
}

func checkRepository(flags *cliFlags) bool {
	log.Println("🔧 Applying repository check job manifest...")
	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		log.Fatalf("❌ Failed to generate job suffix: %v", err)
	}

	checkRepls := map[string]string{
		"AWS_ACCESS_KEY_ID":     flags.awsID,
		"AWS_SECRET_ACCESS_KEY": flags.awsSecret,
		"RESTIC_REPOSITORY":     flags.repository,
		"RESTIC_PASSWORD":       flags.password,
	}
	checkJobName := "restic-check-" + jobSuffix

	if err := k8s.ApplyManifest(manifests.ResticCheckJob, flags.namespace, checkJobName, checkRepls); err != nil {
		log.Fatalf("❌ Failed to apply repository check job manifest: %v", err)
	}

	log.Println("⌛ Waiting for repository check job to complete...")
	return k8s.WaitForJob(checkJobName, flags.namespace, 10*time.Second) == nil
}

func displayBackupInfo(backupInfo *find.BackupInfo) {
	log.Printf("✅ Backup Information:")
	log.Printf("📦 Backup Name: %s", backupInfo.BackupName)
	log.Printf("📁 Namespace: %s", backupInfo.Namespace)
	log.Printf("🕐 Backup Time: %s", backupInfo.BackupTime.Format("2006-01-02 15:04:05"))
	log.Printf("💾 Total Size: %.2f MB", float64(backupInfo.TotalSize)/(1024*1024))
	log.Println("")

	if backupInfo.VMConfig != nil {
		log.Printf("🖥️  VM Configuration:")
		log.Printf("   Snapshot ID: %s", backupInfo.VMConfig.ShortID)
		log.Printf("   Time: %s", backupInfo.VMConfig.Time.Format("2006-01-02 15:04:05"))
		log.Printf("   Size: %.2f MB", float64(backupInfo.VMConfig.DataAdded)/(1024*1024))
		log.Printf("   Tags: %v", backupInfo.VMConfig.Tags)
		log.Println("")
	}

	if len(backupInfo.PVCBackups) > 0 {
		log.Printf("💿 PVC Backups (%d):", len(backupInfo.PVCBackups))
		for i, pvc := range backupInfo.PVCBackups {
			log.Printf("   [%d] PVC Name: %s", i+1, pvc.Name)
			log.Printf("       Snapshot ID: %s", pvc.ShortID)
			log.Printf("       Time: %s", pvc.Time.Format("2006-01-02 15:04:05"))
			log.Printf("       Size: %.2f MB", float64(pvc.DataAdded)/(1024*1024))
			log.Printf("       Tags: %v", pvc.Tags)
			log.Println("")
		}
	} else {
		log.Println("⚠️  No PVC backups found")
	}
}

func handleFindMode(flags *cliFlags) {
	// Handle specific backup info lookup
	if flags.backupName != "" {
		backupInfo, err := find.RunFindBackupInfo(flags.namespace, flags.backupName, flags.awsID, flags.awsSecret, flags.repository, flags.password)
		if err != nil {
			log.Fatalf("❌ Failed to retrieve backup info: %v", err)
		}
		displayBackupInfo(backupInfo)
		return
	}

	// Handle general snapshot search
	snapshots, err := find.RunFind(flags.namespace, flags.tags, flags.awsID, flags.awsSecret, flags.repository, flags.password)
	if err != nil {
		log.Fatalf("❌ Find job failed: %v", err)
	}

	if len(snapshots) == 0 {
		log.Println("❌ No snapshots found.")
		return
	}

	log.Printf("✅ Found %d snapshot(s):", len(snapshots))
	for _, snap := range snapshots {
		log.Printf("  ID: %s, Time: %s, Tags: %v", snap.ShortID, snap.Time.Format("2006-01-02 15:04:05"), snap.Tags)
	}
}

func main() {
	flags := parseFlags()
	validateFlags(flags)

	if err := k8s.InitK8sClients(flags.kubeconfig); err != nil {
		log.Fatalf("❌ Error initializing Kubernetes clients: %v", err)
	}

	repoInitialized := checkRepository(flags)

	if (flags.mode == "find" || flags.mode == "vm-restore" || flags.mode == "cleanup") && !repoInitialized {
		log.Fatal("❌ Repository is not initialized; cannot run find, vm-restore, or cleanup subcommand")
	}

	switch flags.mode {
	case "find":
		handleFindMode(flags)
	case "vm-backup":
		vscMapping := parseVSCMapping(flags.vscMapping)
		vm.RunVMBackup(flags.namespace, flags.vmName, flags.backupName, vscMapping, flags.awsID, flags.awsSecret, flags.repository, flags.password, repoInitialized)
	case "vm-restore":
		vm.RunVMRestore(flags.namespace, flags.vmName, flags.backupName, flags.awsID, flags.awsSecret, flags.repository, flags.password)
	case "cleanup":
		vm.RunVMCleanup(flags.namespace, flags.backupName, flags.awsID, flags.awsSecret, flags.repository, flags.password)
	}
}
