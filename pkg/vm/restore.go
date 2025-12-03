package vm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/webberhuang/hv-vmbr/pkg/k8s"
	"github.com/webberhuang/hv-vmbr/pkg/manifests"
	"github.com/webberhuang/hv-vmbr/pkg/restore"
)

// RunVMRestore executes the VM restore workflow
func RunVMRestore(namespace, vmName, backupName, awsID, awsSecret, repository, password string) {
	log.Printf("🔧 Starting VM restore for backup: %s", backupName)

	// Step 1: Download and parse backup config from restic
	backupConfig, err := downloadBackupConfig(namespace, backupName, awsID, awsSecret, repository, password)
	if err != nil {
		log.Fatalf("❌ Failed to download backup config: %v", err)
	}

	// Step 2: Update namespace and VM name if different
	if vmName != "" && vmName != backupConfig.VMSourceSpec.Metadata.Name {
		log.Printf("📝 Restoring VM as new name: %s (original: %s)", vmName, backupConfig.VMSourceSpec.Metadata.Name)
		backupConfig.VMSourceSpec.Metadata.Name = vmName
	} else {
		vmName = backupConfig.VMSourceSpec.Metadata.Name
	}
	backupConfig.VMSourceSpec.Metadata.Namespace = namespace

	// Step 3: Create new PVCs and restore data
	pvcMapping := restoreVolumes(backupConfig, namespace, backupName, awsID, awsSecret, repository, password)
	log.Printf("✅ Restored %d volume(s)", len(pvcMapping))

	// Step 4: Generate secret names mapping (but don't create them yet)
	secretMapping := generateSecretMapping(backupConfig)

	// Step 5: Update VM spec with new PVC and secret names
	updatedVMSpec := updateVMSpec(backupConfig.VMSourceSpec, pvcMapping, secretMapping)

	// Step 6: Create the VM first
	vmUID, err := createVM(updatedVMSpec, namespace)
	if err != nil {
		log.Fatalf("❌ Failed to create VM: %v", err)
	}

	// Step 7: Now restore secrets with owner reference to the VM
	restoreSecretsWithOwner(backupConfig, namespace, vmName, vmUID, secretMapping)
	log.Printf("✅ Restored %d secret(s)", len(secretMapping))

	log.Printf("✅ VM restore completed successfully: %s/%s", namespace, vmName)
}

// downloadBackupConfig downloads the backup config from restic
func downloadBackupConfig(namespace, backupName, awsID, awsSecret, repository, password string) (*VMBackupConfig, error) {
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

	jobName := "vm-restore-config-" + jobSuffix
	if err := k8s.ApplyManifest(manifests.VMRestoreConfigJob, namespace, jobName, replacements); err != nil {
		return nil, fmt.Errorf("failed to apply restore config job: %w", err)
	}

	log.Println("⌛ Downloading VM config from restic...")
	if err := k8s.WaitForJob(jobName, namespace, 60*time.Second); err != nil {
		return nil, fmt.Errorf("restore config job failed: %w", err)
	}

	// Get job logs which contain the config
	logs, err := k8s.GetJobLogs(jobName, namespace, "restore-config")
	if err != nil {
		return nil, fmt.Errorf("failed to get job logs: %w", err)
	}

	// Parse the config
	var config VMBackupConfig
	if err := json.Unmarshal([]byte(logs), &config); err != nil {
		return nil, fmt.Errorf("failed to parse backup config: %w", err)
	}

	log.Println("✅ VM config downloaded successfully")
	return &config, nil
}

// restoreSecrets restores secrets and returns a mapping of old names to new names
func restoreSecrets(config *VMBackupConfig, namespace string) map[string]string {
	secretMapping := make(map[string]string)

	for _, secretBackup := range config.SecretBackups {
		// Generate new secret name
		newSecretName := fmt.Sprintf("%s-%s", secretBackup.Name, generateRandomSuffix(5))

		// Convert base64 strings back to binary data
		dataMap := make(map[string][]byte)
		for k, v := range secretBackup.Data {
			decoded, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				log.Printf("⚠️  Failed to decode secret data for %s: %v", k, err)
				continue
			}
			dataMap[k] = decoded
		}

		// Create the secret
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      newSecretName,
				Namespace: namespace,
			},
			Data: dataMap,
		}

		_, err := k8s.Clientset.CoreV1().Secrets(namespace).Create(context.Background(), secret, metav1.CreateOptions{})
		if err != nil {
			log.Printf("⚠️  Failed to create secret %s: %v", newSecretName, err)
			continue
		}

		secretMapping[secretBackup.Name] = newSecretName
		log.Printf("📝 Restored secret: %s -> %s", secretBackup.Name, newSecretName)
	}

	return secretMapping
}

// restoreVolumes restores all volumes and returns a mapping of old PVC names to new PVC names
func restoreVolumes(config *VMBackupConfig, namespace, backupName, awsID, awsSecret, repository, password string) map[string]string {
	pvcMapping := make(map[string]string)

	for _, volumeBackup := range config.VolumeBackups {
		oldPVCName := volumeBackup.PersistentVolumeClaim.Name
		newPVCName := fmt.Sprintf("%s-%s", oldPVCName, generateRandomSuffix(5))

		log.Printf("📦 Restoring volume: %s -> %s", oldPVCName, newPVCName)

		// Create new PVC with cleaned metadata
		newPVC := createCleanPVC(&volumeBackup.PersistentVolumeClaim, newPVCName, namespace)

		_, err := k8s.Clientset.CoreV1().PersistentVolumeClaims(namespace).Create(context.Background(), newPVC, metav1.CreateOptions{})
		if err != nil {
			log.Fatalf("❌ Failed to create PVC %s: %v", newPVCName, err)
		}

		log.Printf("✅ PVC %s created successfully", newPVCName)

		// Restore the data
		restoreVolumeData(volumeBackup, newPVCName, namespace, backupName, oldPVCName, awsID, awsSecret, repository, password)

		pvcMapping[oldPVCName] = newPVCName
		log.Printf("✅ Volume restored: %s -> %s", oldPVCName, newPVCName)
	}

	return pvcMapping
}

// createCleanPVC creates a new PVC with all CDI and binding metadata removed
func createCleanPVC(sourcePVC *corev1.PersistentVolumeClaim, newName, namespace string) *corev1.PersistentVolumeClaim {
	newPVC := sourcePVC.DeepCopy()
	newPVC.Name = newName
	newPVC.Namespace = namespace

	// Clear all metadata that could cause binding issues
	newPVC.ResourceVersion = ""
	newPVC.UID = ""
	newPVC.SelfLink = ""
	newPVC.CreationTimestamp = metav1.Time{}
	newPVC.Generation = 0
	newPVC.ManagedFields = nil
	newPVC.OwnerReferences = nil

	// Clear the old PV binding - this is critical!
	newPVC.Spec.VolumeName = ""

	// Clear dataSource and dataSourceRef to prevent CDI from managing this PVC
	newPVC.Spec.DataSource = nil
	newPVC.Spec.DataSourceRef = nil

	// Reset status to allow new binding
	newPVC.Status = corev1.PersistentVolumeClaimStatus{}

	// Clean annotations
	cleanPVCAnnotations(newPVC)

	// Clean labels
	cleanPVCLabels(newPVC)

	return newPVC
}

// cleanPVCAnnotations removes all CDI and binding-related annotations
func cleanPVCAnnotations(pvc *corev1.PersistentVolumeClaim) {
	if pvc.Annotations == nil {
		return
	}

	// PV binding annotations
	delete(pvc.Annotations, "pv.kubernetes.io/bind-completed")
	delete(pvc.Annotations, "pv.kubernetes.io/bound-by-controller")

	// CDI annotations
	cdiAnnotations := []string{
		"cdi.kubevirt.io/clonePhase",
		"cdi.kubevirt.io/cloneType",
		"cdi.kubevirt.io/createdForDataVolume",
		"cdi.kubevirt.io/storage.condition.running",
		"cdi.kubevirt.io/storage.condition.running.message",
		"cdi.kubevirt.io/storage.condition.running.reason",
		"cdi.kubevirt.io/storage.contentType",
		"cdi.kubevirt.io/storage.pod.restarts",
		"cdi.kubevirt.io/storage.populator.progress",
		"cdi.kubevirt.io/storage.preallocation.requested",
		"cdi.kubevirt.io/storage.usePopulator",
	}

	for _, ann := range cdiAnnotations {
		delete(pvc.Annotations, ann)
	}

	// Storage provisioner annotations
	delete(pvc.Annotations, "volume.beta.kubernetes.io/storage-provisioner")
	delete(pvc.Annotations, "volume.kubernetes.io/storage-provisioner")
}

// cleanPVCLabels removes CDI-related labels
func cleanPVCLabels(pvc *corev1.PersistentVolumeClaim) {
	if pvc.Labels == nil {
		return
	}

	delete(pvc.Labels, "app")
	delete(pvc.Labels, "app.kubernetes.io/component")
	delete(pvc.Labels, "app.kubernetes.io/managed-by")
}

// restoreVolumeData restores the actual volume data using restic
func restoreVolumeData(volumeBackup VolumeBackup, newPVCName, namespace, backupName, oldPVCName, awsID, awsSecret, repository, password string) {
	// Get the source PV name from the backup
	sourcePV := volumeBackup.PersistentVolumeClaim.Spec.VolumeName
	sourceNs := volumeBackup.PersistentVolumeClaim.Namespace

	// The tag format is: {backupName}-pvc-{oldPVCName}
	snapshotTag := fmt.Sprintf("%s-pvc-%s", backupName, oldPVCName)

	// Restore the data using existing restore functionality
	restore.RunRestore(namespace, newPVCName, sourceNs, sourcePV, snapshotTag, awsID, awsSecret, repository, password)
}

// updateVMSpec updates the VM spec with new PVC and secret names
func updateVMSpec(vmSpec VMSpec, pvcMapping, secretMapping map[string]string) VMSpec {
	// Convert spec to map for manipulation
	specMap := vmSpec.Spec.(map[string]interface{})

	// Navigate to volumes
	template, ok := specMap["template"].(map[string]interface{})
	if !ok {
		return vmSpec
	}

	templateSpec, ok := template["spec"].(map[string]interface{})
	if !ok {
		return vmSpec
	}

	volumes, ok := templateSpec["volumes"].([]interface{})
	if !ok {
		return vmSpec
	}

	// Update PVC and secret references
	for _, vol := range volumes {
		volume, ok := vol.(map[string]interface{})
		if !ok {
			continue
		}

		updatePVCReference(volume, pvcMapping)
		updateSecretReferences(volume, secretMapping)
	}

	return vmSpec
}

// updatePVCReference updates PVC claim name in a volume
func updatePVCReference(volume map[string]interface{}, pvcMapping map[string]string) {
	pvc, found := volume["persistentVolumeClaim"].(map[string]interface{})
	if !found {
		return
	}

	oldName, ok := pvc["claimName"].(string)
	if !ok {
		return
	}

	if newName, exists := pvcMapping[oldName]; exists {
		pvc["claimName"] = newName
		log.Printf("📝 Updated PVC reference: %s -> %s", oldName, newName)
	}
}

// updateSecretReferences updates secret references in cloudInitNoCloud volume
func updateSecretReferences(volume map[string]interface{}, secretMapping map[string]string) {
	cloudInit, found := volume["cloudInitNoCloud"].(map[string]interface{})
	if !found {
		return
	}

	updateSecretRef(cloudInit, "secretRef", secretMapping, true)
	updateSecretRef(cloudInit, "networkDataSecretRef", secretMapping, false)
}

// updateSecretRef updates a single secret reference field
func updateSecretRef(cloudInit map[string]interface{}, fieldName string, secretMapping map[string]string, logUpdate bool) {
	secretRef, found := cloudInit[fieldName].(map[string]interface{})
	if !found {
		return
	}

	oldName, ok := secretRef["name"].(string)
	if !ok {
		return
	}

	if newName, exists := secretMapping[oldName]; exists {
		secretRef["name"] = newName
		if logUpdate {
			log.Printf("📝 Updated secret reference: %s -> %s", oldName, newName)
		}
	}
}

// createVM creates the VirtualMachine resource
func createVM(vmSpec VMSpec, namespace string) (string, error) {
	// Delete the harvesterhci.io/volumeClaimTemplates annotation if present
	if vmSpec.Metadata.Annotations != nil {
		delete(vmSpec.Metadata.Annotations, "harvesterhci.io/volumeClaimTemplates")
		log.Println("📝 Removed harvesterhci.io/volumeClaimTemplates annotation")

		// Remove the harvesterhci.io/mac-address annotation if present
		delete(vmSpec.Metadata.Annotations, "harvesterhci.io/mac-address")
		log.Println("📝 Removed harvesterhci.io/mac-address annotation")
	}

	// Clear MAC addresses for all network interfaces
	clearMACAddresses(&vmSpec)

	// Set runStrategy to Halted for the restored VM
	if specMap, ok := vmSpec.Spec.(map[string]interface{}); ok {
		specMap["runStrategy"] = "Halted"
		log.Println("📝 Set runStrategy to Halted")
	}

	// Construct the VM object
	vmObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":        vmSpec.Metadata.Name,
				"namespace":   namespace,
				"labels":      vmSpec.Metadata.Labels,
				"annotations": vmSpec.Metadata.Annotations,
			},
			"spec": vmSpec.Spec,
		},
	}

	// Create the VM
	createdVM, err := k8s.DynamicClient.Resource(VMGVR).Namespace(namespace).Create(context.Background(), vmObj, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create VirtualMachine: %w", err)
	}

	vmUID := string(createdVM.GetUID())
	log.Printf("✅ VirtualMachine created: %s/%s", namespace, vmSpec.Metadata.Name)
	return vmUID, nil
}

// clearMACAddresses clears MAC addresses for all network interfaces in the VM spec
func clearMACAddresses(vmSpec *VMSpec) {
	specMap, ok := vmSpec.Spec.(map[string]interface{})
	if !ok {
		return
	}

	template, ok := specMap["template"].(map[string]interface{})
	if !ok {
		return
	}

	templateSpec, ok := template["spec"].(map[string]interface{})
	if !ok {
		return
	}

	domain, ok := templateSpec["domain"].(map[string]interface{})
	if !ok {
		return
	}

	devices, ok := domain["devices"].(map[string]interface{})
	if !ok {
		return
	}

	interfaces, ok := devices["interfaces"].([]interface{})
	if !ok {
		return
	}

	// Clear MAC address for each interface
	for i, iface := range interfaces {
		ifaceMap, ok := iface.(map[string]interface{})
		if !ok {
			continue
		}

		if _, hasMac := ifaceMap["macAddress"]; hasMac {
			ifaceMap["macAddress"] = ""
			log.Printf("📝 Cleared MAC address for interface[%d]", i)
		}
	}
}

// generateRandomSuffix generates a random suffix for resource names
func generateRandomSuffix(length int) string {
	suffix, _ := k8s.GenerateJobSuffix()
	if len(suffix) > length {
		return suffix[:length]
	}
	return suffix
}

// generateSecretMapping generates new secret names without creating them
func generateSecretMapping(config *VMBackupConfig) map[string]string {
	secretMapping := make(map[string]string)
	for _, secretBackup := range config.SecretBackups {
		newSecretName := fmt.Sprintf("%s-%s", secretBackup.Name, generateRandomSuffix(5))
		secretMapping[secretBackup.Name] = newSecretName
	}
	return secretMapping
}

// restoreSecretsWithOwner restores secrets with owner reference to the VM
func restoreSecretsWithOwner(config *VMBackupConfig, namespace, vmName, vmUID string, secretMapping map[string]string) {
	// Set up owner reference
	trueVal := true
	ownerRef := metav1.OwnerReference{
		APIVersion:         "kubevirt.io/v1",
		Kind:               "VirtualMachine",
		Name:               vmName,
		UID:                types.UID(vmUID),
		Controller:         &trueVal,
		BlockOwnerDeletion: &trueVal,
	}

	for _, secretBackup := range config.SecretBackups {
		newSecretName := secretMapping[secretBackup.Name]

		// Convert base64 strings back to binary data
		dataMap := make(map[string][]byte)
		for k, v := range secretBackup.Data {
			decoded, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				log.Printf("⚠️  Failed to decode secret data for %s: %v", k, err)
				continue
			}
			dataMap[k] = decoded
		}

		// Create the secret with owner reference
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            newSecretName,
				Namespace:       namespace,
				OwnerReferences: []metav1.OwnerReference{ownerRef},
			},
			Data: dataMap,
		}

		_, err := k8s.Clientset.CoreV1().Secrets(namespace).Create(context.Background(), secret, metav1.CreateOptions{})
		if err != nil {
			log.Printf("⚠️  Failed to create secret %s: %v", newSecretName, err)
			continue
		}

		log.Printf("📝 Restored secret with owner reference: %s -> %s", secretBackup.Name, newSecretName)
	}
}
