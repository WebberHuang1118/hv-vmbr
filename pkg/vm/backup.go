package vm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/webberhuang/hv-vmbr/pkg/backup"
	"github.com/webberhuang/hv-vmbr/pkg/find"
	"github.com/webberhuang/hv-vmbr/pkg/k8s"
	"github.com/webberhuang/hv-vmbr/pkg/manifests"
)

var (
	// VMGVR is the GroupVersionResource for KubeVirt VirtualMachine
	VMGVR = schema.GroupVersionResource{
		Group:    "kubevirt.io",
		Version:  "v1",
		Resource: "virtualmachines",
	}
)

// RunVMBackup executes the VM backup workflow
func RunVMBackup(namespace, vmName, backupName string, vscMapping map[string]string, awsID, awsSecret, repository, password string, repoInitialized bool) {
	log.Printf("🔧 Starting VM backup for %s/%s", namespace, vmName)

	vmObj, err := k8s.DynamicClient.Resource(VMGVR).Namespace(namespace).Get(context.Background(), vmName, metav1.GetOptions{})
	if err != nil {
		log.Fatalf("❌ Failed to get VirtualMachine %s: %v", vmName, err)
	}

	sanitizedVM := sanitizeVMManifest(vmObj)
	pvcList := extractPVCsFromVM(vmObj)
	if len(pvcList) == 0 {
		log.Println("⚠️  No PVCs found in VM, backing up manifest only")
	}

	volumeBackups, repoInit := backupPVCs(vmObj, namespace, backupName, pvcList, vscMapping, awsID, awsSecret, repository, password, repoInitialized)
	secretBackups := extractAndBackupSecrets(vmObj, namespace)

	backupConfig := VMBackupConfig{
		Name:      backupName,
		Namespace: namespace,
		BackupSpec: BackupSpec{
			Source: SourceRef{
				APIGroup: "kubevirt.io",
				Kind:     "VirtualMachine",
				Name:     vmName,
			},
			Type: "backup",
		},
		VMSourceSpec:  sanitizedVM,
		VolumeBackups: volumeBackups,
		SecretBackups: secretBackups,
	}

	if err := saveBackupConfig(backupConfig, namespace, backupName, awsID, awsSecret, repository, password); err != nil {
		log.Fatalf("❌ Failed to save backup config: %v", err)
	}

	_ = repoInit // Suppress unused variable warning
	log.Printf("✅ VM backup completed successfully: %s", backupName)
}

// backupPVCs handles the backup of all PVCs in the VM
func backupPVCs(vmObj *unstructured.Unstructured, namespace, backupName string, pvcList []string, vscMapping map[string]string, awsID, awsSecret, repository, password string, repoInitialized bool) ([]VolumeBackup, bool) {
	volumeBackups := []VolumeBackup{}

	for _, pvcName := range pvcList {
		log.Printf("📦 Backing up PVC: %s", pvcName)

		pvc, err := k8s.Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(context.Background(), pvcName, metav1.GetOptions{})
		if err != nil {
			log.Fatalf("❌ Failed to get PVC %s: %v", pvcName, err)
		}

		csiDriver := getCSIDriverName(pvc)
		log.Printf("📋 PVC %s uses CSI driver: %s", pvcName, csiDriver)

		vsc, ok := vscMapping[csiDriver]
		if !ok {
			log.Fatalf("❌ No VolumeSnapshotClass mapping found for CSI driver: %s. Please provide mapping using -vsc flag", csiDriver)
		}
		log.Printf("📸 Using VolumeSnapshotClass: %s for PVC %s", vsc, pvcName)

		pvcSnapshotTag := fmt.Sprintf("%s-pvc-%s", backupName, pvcName)
		backup.RunBackup(namespace, pvcName, pvcSnapshotTag, vsc, awsID, awsSecret, repository, password, repoInitialized)
		repoInitialized = true

		snapshotID, err := find.RunFindByID(namespace, pvcSnapshotTag, awsID, awsSecret, repository, password)
		if err != nil {
			log.Fatalf("❌ Failed to verify backup for PVC %s: %v", pvcName, err)
		}

		volumeBackup := VolumeBackup{
			Name:                  fmt.Sprintf("%s-volume-%s", backupName, pvcName),
			VolumeName:            getVolumeNameForPVC(vmObj, pvcName),
			CSIDriverName:         csiDriver,
			PersistentVolumeClaim: *pvc,
			ResticSnapshotID:      snapshotID,
			VolumeSize:            pvc.Spec.Resources.Requests.Storage().Value(),
			Progress:              100,
		}
		volumeBackups = append(volumeBackups, volumeBackup)
		log.Printf("✅ PVC %s backed up with snapshot ID: %s", pvcName, snapshotID)
	}

	return volumeBackups, repoInitialized
}

// RunVMCleanup removes all backup resources for a given backup name
func RunVMCleanup(namespace, backupName, awsID, awsSecret, repository, password string) {
	log.Printf("🔧 Starting cleanup for backup: %s", backupName)

	// Download backup config to get the list of PVCs
	backupConfig, err := downloadBackupConfigForCleanup(namespace, backupName, awsID, awsSecret, repository, password)
	if err != nil {
		log.Printf("⚠️  Failed to download backup config (may already be deleted): %v", err)
	}

	// Delete PVC snapshots from restic
	if backupConfig != nil {
		for _, volumeBackup := range backupConfig.VolumeBackups {
			snapshotTag := fmt.Sprintf("%s-pvc-%s", backupName, volumeBackup.PersistentVolumeClaim.Name)
			log.Printf("🗑️  Deleting snapshot for PVC: %s", volumeBackup.PersistentVolumeClaim.Name)

			if err := deleteResticSnapshot(namespace, snapshotTag, awsID, awsSecret, repository, password); err != nil {
				log.Printf("⚠️  Failed to delete snapshot for PVC %s: %v", volumeBackup.PersistentVolumeClaim.Name, err)
				continue
			}
			log.Printf("✅ Deleted snapshot for PVC: %s", volumeBackup.PersistentVolumeClaim.Name)
		}
	}

	// Delete VM config from restic
	log.Printf("🗑️  Deleting VM config from restic...")
	if err := deleteVMConfigSnapshot(namespace, backupName, awsID, awsSecret, repository, password); err != nil {
		log.Printf("⚠️  Failed to delete VM config: %v", err)
	} else {
		log.Printf("✅ Deleted VM config from restic")
	}

	// Delete local config file if exists
	filename := fmt.Sprintf("%s.cfg", backupName)
	if err := os.Remove(filename); err != nil && !os.IsNotExist(err) {
		log.Printf("⚠️  Failed to delete local config file %s: %v", filename, err)
	} else if err == nil {
		log.Printf("✅ Deleted local config file: %s", filename)
	}

	log.Printf("✅ Cleanup completed for backup: %s", backupName)
}

// downloadBackupConfigForCleanup attempts to download the backup config (used for cleanup)
func downloadBackupConfigForCleanup(namespace, backupName, awsID, awsSecret, repository, password string) (*VMBackupConfig, error) {
	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		return nil, fmt.Errorf("failed to generate job suffix: %w", err)
	}

	replacements := map[string]string{
		"AWS_ACCESS_KEY_ID":     awsID,
		"AWS_SECRET_ACCESS_KEY": awsSecret,
		"RESTIC_REPOSITORY":     repository,
		"RESTIC_PASSWORD":       password,
		"BACKUP_NAME":           backupName,
	}

	jobName := "vm-cleanup-config-" + jobSuffix
	if err := k8s.ApplyManifest(manifests.VMRestoreConfigJob, namespace, jobName, replacements); err != nil {
		return nil, fmt.Errorf("failed to apply cleanup config job: %w", err)
	}

	if err := k8s.WaitForJob(jobName, namespace, 60*time.Second); err != nil {
		return nil, fmt.Errorf("cleanup config job failed: %w", err)
	}

	logs, err := k8s.GetJobLogs(jobName, namespace, "restore-config")
	if err != nil {
		return nil, fmt.Errorf("failed to get job logs: %w", err)
	}

	var config VMBackupConfig
	if err := json.Unmarshal([]byte(logs), &config); err != nil {
		return nil, fmt.Errorf("failed to parse backup config: %w", err)
	}

	return &config, nil
}

// deleteResticSnapshot deletes a restic snapshot by tag
func deleteResticSnapshot(namespace, snapshotTag, awsID, awsSecret, repository, password string) error {
	// First, find the snapshot ID
	tags := []string{
		fmt.Sprintf("ns=%s", namespace),
		fmt.Sprintf("sn=%s", snapshotTag),
	}

	snapshots, err := find.RunFind(namespace, tags, awsID, awsSecret, repository, password)
	if err != nil {
		return fmt.Errorf("failed to find snapshot: %w", err)
	}

	if len(snapshots) == 0 {
		return fmt.Errorf("snapshot not found with tag: %s", snapshotTag)
	}

	// Delete the snapshot
	snapshotID := snapshots[0].ShortID
	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		return fmt.Errorf("failed to generate job suffix: %w", err)
	}

	replacements := map[string]string{
		"AWS_ACCESS_KEY_ID":     awsID,
		"AWS_SECRET_ACCESS_KEY": awsSecret,
		"RESTIC_REPOSITORY":     repository,
		"RESTIC_PASSWORD":       password,
		"SNAPSHOT_ID":           snapshotID,
	}

	jobName := "delete-snapshot-" + jobSuffix
	if err := k8s.ApplyManifest(manifests.ResticForgetJob, namespace, jobName, replacements); err != nil {
		return fmt.Errorf("failed to apply delete job: %w", err)
	}

	if err := k8s.WaitForJob(jobName, namespace, 120*time.Second); err != nil {
		return fmt.Errorf("delete job failed: %w", err)
	}

	return nil
}

// deleteVMConfigSnapshot deletes the VM config snapshot from restic
func deleteVMConfigSnapshot(namespace, backupName, awsID, awsSecret, repository, password string) error {
	// Find the VM config snapshot
	tags := []string{
		fmt.Sprintf("ns=%s", namespace),
		fmt.Sprintf("sn=%s", backupName),
		"type=vm-config",
	}

	snapshots, err := find.RunFind(namespace, tags, awsID, awsSecret, repository, password)
	if err != nil {
		return fmt.Errorf("failed to find VM config snapshot: %w", err)
	}

	if len(snapshots) == 0 {
		return fmt.Errorf("VM config snapshot not found")
	}

	// Delete the snapshot
	snapshotID := snapshots[0].ShortID
	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		return fmt.Errorf("failed to generate job suffix: %w", err)
	}

	replacements := map[string]string{
		"AWS_ACCESS_KEY_ID":     awsID,
		"AWS_SECRET_ACCESS_KEY": awsSecret,
		"RESTIC_REPOSITORY":     repository,
		"RESTIC_PASSWORD":       password,
		"SNAPSHOT_ID":           snapshotID,
	}

	jobName := "delete-vm-config-" + jobSuffix
	if err := k8s.ApplyManifest(manifests.ResticForgetJob, namespace, jobName, replacements); err != nil {
		return fmt.Errorf("failed to apply delete job: %w", err)
	}

	if err := k8s.WaitForJob(jobName, namespace, 120*time.Second); err != nil {
		return fmt.Errorf("delete job failed: %w", err)
	}

	return nil
}

// sanitizeVMManifest removes runtime and status fields from VM manifest
func sanitizeVMManifest(vmObj *unstructured.Unstructured) VMSpec {
	metadata := vmObj.Object["metadata"].(map[string]interface{})
	spec := vmObj.Object["spec"]

	// Clean metadata
	cleanMeta := metav1.ObjectMeta{}
	if name, ok := metadata["name"].(string); ok {
		cleanMeta.Name = name
	}
	if ns, ok := metadata["namespace"].(string); ok {
		cleanMeta.Namespace = ns
	}
	cleanMeta.CreationTimestamp = metav1.Time{}

	// Copy labels
	if labels, ok := metadata["labels"].(map[string]interface{}); ok {
		cleanMeta.Labels = make(map[string]string)
		for k, v := range labels {
			if str, ok := v.(string); ok {
				cleanMeta.Labels[k] = str
			}
		}
	}

	// Copy annotations
	if annotations, ok := metadata["annotations"].(map[string]interface{}); ok {
		cleanMeta.Annotations = make(map[string]string)
		for k, v := range annotations {
			if str, ok := v.(string); ok {
				cleanMeta.Annotations[k] = str
			}
		}
	}

	return VMSpec{
		Metadata: cleanMeta,
		Spec:     spec,
	}
}

// extractPVCsFromVM extracts PVC names from VM volumes
func extractPVCsFromVM(vmObj *unstructured.Unstructured) []string {
	pvcList := []string{}

	spec, found, err := unstructured.NestedMap(vmObj.Object, "spec", "template", "spec")
	if err != nil || !found {
		return pvcList
	}

	volumes, found, err := unstructured.NestedSlice(spec, "volumes")
	if err != nil || !found {
		return pvcList
	}

	for _, vol := range volumes {
		volume := vol.(map[string]interface{})
		if pvc, found := volume["persistentVolumeClaim"]; found {
			pvcMap := pvc.(map[string]interface{})
			if claimName, ok := pvcMap["claimName"].(string); ok {
				pvcList = append(pvcList, claimName)
			}
		}
	}

	return pvcList
}

// getVolumeNameForPVC finds the volume name in VM spec that references the PVC
func getVolumeNameForPVC(vmObj *unstructured.Unstructured, pvcName string) string {
	spec, found, err := unstructured.NestedMap(vmObj.Object, "spec", "template", "spec")
	if err != nil || !found {
		return pvcName
	}

	volumes, found, err := unstructured.NestedSlice(spec, "volumes")
	if err != nil || !found {
		return pvcName
	}

	for _, vol := range volumes {
		volume := vol.(map[string]interface{})
		if pvc, found := volume["persistentVolumeClaim"]; found {
			pvcMap := pvc.(map[string]interface{})
			if claimName, ok := pvcMap["claimName"].(string); ok && claimName == pvcName {
				if name, ok := volume["name"].(string); ok {
					return name
				}
			}
		}
	}

	return pvcName
}

// getCSIDriverName extracts CSI driver from PV or PVC annotations
func getCSIDriverName(pvc *corev1.PersistentVolumeClaim) string {
	// Use the k8s package function for accurate CSI driver detection
	driver, err := k8s.GetPVCSIDriver(pvc.Name, pvc.Namespace)
	if err != nil {
		log.Printf("⚠️  Failed to get CSI driver for PVC %s: %v, using fallback", pvc.Name, err)
		// Fallback to annotation
		if fallbackDriver, ok := pvc.Annotations["volume.kubernetes.io/storage-provisioner"]; ok {
			return fallbackDriver
		}
		// Last resort: storage class name
		if pvc.Spec.StorageClassName != nil {
			return *pvc.Spec.StorageClassName
		}
		return "unknown"
	}
	return driver
}

// extractAndBackupSecrets finds and backs up secrets referenced in VM
func extractAndBackupSecrets(vmObj *unstructured.Unstructured, namespace string) []SecretBackup {
	secretBackups := []SecretBackup{}
	secretNames := extractSecretNames(vmObj)

	for _, secretName := range secretNames {
		secret, err := k8s.Clientset.CoreV1().Secrets(namespace).Get(context.Background(), secretName, metav1.GetOptions{})
		if err != nil {
			log.Printf("⚠️  Failed to get secret %s: %v", secretName, err)
			continue
		}

		// Convert binary data to base64 strings
		dataMap := make(map[string]string)
		for k, v := range secret.Data {
			dataMap[k] = base64.StdEncoding.EncodeToString(v)
		}

		secretBackups = append(secretBackups, SecretBackup{
			Name: secretName,
			Data: dataMap,
		})
		log.Printf("📝 Backed up secret: %s", secretName)
	}

	return secretBackups
}

// extractSecretNames extracts secret names from VM volumes
func extractSecretNames(vmObj *unstructured.Unstructured) []string {
	secretNames := []string{}

	spec, found, err := unstructured.NestedMap(vmObj.Object, "spec", "template", "spec")
	if err != nil || !found {
		return secretNames
	}

	volumes, found, err := unstructured.NestedSlice(spec, "volumes")
	if err != nil || !found {
		return secretNames
	}

	for _, vol := range volumes {
		volume := vol.(map[string]interface{})

		// Check cloudInitNoCloud
		if cloudInit, found := volume["cloudInitNoCloud"]; found {
			cloudInitMap := cloudInit.(map[string]interface{})

			if secretRef, found := cloudInitMap["secretRef"]; found {
				secretRefMap := secretRef.(map[string]interface{})
				if name, ok := secretRefMap["name"].(string); ok {
					secretNames = append(secretNames, name)
				}
			}

			if networkRef, found := cloudInitMap["networkDataSecretRef"]; found {
				networkRefMap := networkRef.(map[string]interface{})
				if name, ok := networkRefMap["name"].(string); ok {
					// Avoid duplicates
					if len(secretNames) == 0 || secretNames[len(secretNames)-1] != name {
						secretNames = append(secretNames, name)
					}
				}
			}
		}
	}

	return secretNames
}

// saveBackupConfig saves the backup configuration to restic repository
func saveBackupConfig(config VMBackupConfig, namespace, backupName, awsID, awsSecret, repository, password string) error {
	// Marshal to JSON
	jsonData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal backup config: %w", err)
	}

	// Save to local file first
	filename := fmt.Sprintf("%s.cfg", config.Name)
	if err := os.WriteFile(filename, jsonData, 0644); err != nil {
		return fmt.Errorf("failed to write backup config file: %w", err)
	}
	log.Printf("💾 Saved backup config to: %s", filename)

	// Upload to restic
	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		return fmt.Errorf("failed to generate job suffix: %w", err)
	}

	// Create a ConfigMap with the backup config
	configMapName := fmt.Sprintf("vm-backup-config-%s", jobSuffix)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
		Data: map[string]string{
			filename: string(jsonData),
		},
	}

	_, err = k8s.Clientset.CoreV1().ConfigMaps(namespace).Create(context.Background(), configMap, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create ConfigMap: %w", err)
	}

	// Create job to backup the config file to restic
	replacements := map[string]string{
		"AWS_ACCESS_KEY_ID":     awsID,
		"AWS_SECRET_ACCESS_KEY": awsSecret,
		"RESTIC_REPOSITORY":     repository,
		"RESTIC_PASSWORD":       password,
		"FILENAME":              filename,
		"SNAPSHOT":              backupName,
		"CONFIGMAP_NAME":        configMapName,
	}

	jobName := "vm-backup-config-" + jobSuffix
	if err := k8s.ApplyManifest(manifests.VMBackupConfigJob, namespace, jobName, replacements); err != nil {
		return fmt.Errorf("failed to apply backup config job: %w", err)
	}

	log.Println("⌛ Uploading VM config to restic...")
	if err := k8s.WaitForJob(jobName, namespace, 60*time.Second); err != nil {
		return fmt.Errorf("backup config job failed: %w", err)
	}

	// Cleanup ConfigMap
	if err := k8s.Clientset.CoreV1().ConfigMaps(namespace).Delete(context.Background(), configMapName, metav1.DeleteOptions{}); err != nil {
		log.Printf("⚠️  Failed to cleanup ConfigMap: %v", err)
	}

	log.Println("✅ VM config uploaded to restic")
	return nil
}
