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

	"github.com/webberhuang/hv-vmbr/pkg/k8s"
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

	// Step 3: Restore secrets first
	secretMapping := restoreSecrets(backupConfig, namespace)
	log.Printf("✅ Restored %d secret(s)", len(secretMapping))

	// Step 4: Create new PVCs and restore data
	pvcMapping := restoreVolumes(backupConfig, namespace, backupName, awsID, awsSecret, repository, password)
	log.Printf("✅ Restored %d volume(s)", len(pvcMapping))

	// Step 5: Update VM spec with new PVC and secret names
	updatedVMSpec := updateVMSpec(backupConfig.VMSourceSpec, pvcMapping, secretMapping)

	// Step 6: Create the VM
	if err := createVM(updatedVMSpec, namespace); err != nil {
		log.Fatalf("❌ Failed to create VM: %v", err)
	}

	log.Printf("✅ VM restore completed successfully: %s/%s", namespace, vmName)
}

// downloadBackupConfig downloads the backup config from restic
func downloadBackupConfig(namespace, backupName, awsID, awsSecret, repository, password string) (*VMBackupConfig, error) {
	jobSuffix, err := k8s.GenerateJobSuffix()
	if err != nil {
		return nil, fmt.Errorf("failed to generate job suffix: %w", err)
	}

	// Create job to download config from restic
	manifestTemplate := `
apiVersion: batch/v1
kind: Job
metadata:
  name: {{NAME}}
  namespace: {{NAMESPACE}}
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 30
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: restore-config
        image: webberhuang/restic-accelerated:latest
        imagePullPolicy: IfNotPresent
        command: ["/bin/sh", "-c"]
        args:
          - |
            export AWS_ACCESS_KEY_ID={{AWS_ACCESS_KEY_ID}}
            export AWS_SECRET_ACCESS_KEY={{AWS_SECRET_ACCESS_KEY}}
            export RESTIC_REPOSITORY={{RESTIC_REPOSITORY}}
            export RESTIC_PASSWORD={{RESTIC_PASSWORD}}
            
            # Get snapshots as JSON array and extract the first snapshot's short_id
            restic snapshots --tag=ns={{NAMESPACE}},sn={{BACKUP_NAME}},type=vm-config --json 2>/dev/null > /tmp/snapshots.json
            SNAPSHOT_ID=$(cat /tmp/snapshots.json | grep -o '"short_id":"[^"]*"' | head -n1 | cut -d'"' -f4)
            
            if [ -z "$SNAPSHOT_ID" ]; then
              echo "Error: No snapshot found with tags ns={{NAMESPACE}},sn={{BACKUP_NAME}},type=vm-config"
              exit 1
            fi
            
            # Dump the backup config from the snapshot (outputs as tar) and extract the JSON file
            restic dump $SNAPSHOT_ID / 2>/dev/null | tar -xO
`

	replacements := map[string]string{
		"AWS_ACCESS_KEY_ID":     awsID,
		"AWS_SECRET_ACCESS_KEY": awsSecret,
		"RESTIC_REPOSITORY":     repository,
		"RESTIC_PASSWORD":       password,
		"BACKUP_NAME":           backupName,
	}

	jobName := "vm-restore-config-" + jobSuffix
	if err := k8s.ApplyManifest(manifestTemplate, namespace, jobName, replacements); err != nil {
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

		// Create new PVC
		newPVC := volumeBackup.PersistentVolumeClaim.DeepCopy()
		newPVC.Name = newPVCName
		newPVC.Namespace = namespace

		// Clear all metadata that could cause binding issues
		newPVC.ResourceVersion = ""
		newPVC.UID = ""
		newPVC.SelfLink = ""
		newPVC.CreationTimestamp = metav1.Time{}
		newPVC.Generation = 0
		newPVC.ManagedFields = nil

		// Clear annotations related to PV binding
		if newPVC.Annotations != nil {
			delete(newPVC.Annotations, "pv.kubernetes.io/bind-completed")
			delete(newPVC.Annotations, "pv.kubernetes.io/bound-by-controller")
		}

		// Clear the old PV binding - this is critical!
		newPVC.Spec.VolumeName = ""

		// Reset status to allow new binding
		newPVC.Status = corev1.PersistentVolumeClaimStatus{}

		_, err := k8s.Clientset.CoreV1().PersistentVolumeClaims(namespace).Create(context.Background(), newPVC, metav1.CreateOptions{})
		if err != nil {
			log.Fatalf("❌ Failed to create PVC %s: %v", newPVCName, err)
		}

		// Wait for PVC to be bound
		log.Printf("⌛ Waiting for PVC %s to become Bound...", newPVCName)
		if err := k8s.WaitForPVCBound(newPVCName, namespace, 300*time.Second); err != nil {
			log.Fatalf("❌ PVC %s did not become Bound: %v", newPVCName, err)
		}

		// Get the source PV name from the backup
		sourcePV := volumeBackup.PersistentVolumeClaim.Spec.VolumeName
		sourceNs := volumeBackup.PersistentVolumeClaim.Namespace

		// Use backupName instead of snapshot for tag
		// The tag format is: {backupName}-pvc-{oldPVCName}
		snapshotTag := fmt.Sprintf("%s-pvc-%s", backupName, oldPVCName)

		// Restore the data using existing restore functionality
		restore.RunRestore(namespace, newPVCName, sourceNs, sourcePV, snapshotTag, awsID, awsSecret, repository, password)

		pvcMapping[oldPVCName] = newPVCName
		log.Printf("✅ Volume restored: %s -> %s", oldPVCName, newPVCName)
	}

	return pvcMapping
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
		volume := vol.(map[string]interface{})

		// Update PVC references
		if pvc, found := volume["persistentVolumeClaim"]; found {
			pvcMap := pvc.(map[string]interface{})
			if oldName, ok := pvcMap["claimName"].(string); ok {
				if newName, exists := pvcMapping[oldName]; exists {
					pvcMap["claimName"] = newName
					log.Printf("📝 Updated PVC reference: %s -> %s", oldName, newName)
				}
			}
		}

		// Update secret references
		if cloudInit, found := volume["cloudInitNoCloud"]; found {
			cloudInitMap := cloudInit.(map[string]interface{})

			if secretRef, found := cloudInitMap["secretRef"]; found {
				secretRefMap := secretRef.(map[string]interface{})
				if oldName, ok := secretRefMap["name"].(string); ok {
					if newName, exists := secretMapping[oldName]; exists {
						secretRefMap["name"] = newName
						log.Printf("📝 Updated secret reference: %s -> %s", oldName, newName)
					}
				}
			}

			if networkRef, found := cloudInitMap["networkDataSecretRef"]; found {
				networkRefMap := networkRef.(map[string]interface{})
				if oldName, ok := networkRefMap["name"].(string); ok {
					if newName, exists := secretMapping[oldName]; exists {
						networkRefMap["name"] = newName
					}
				}
			}
		}
	}

	return vmSpec
}

// createVM creates the VirtualMachine resource
func createVM(vmSpec VMSpec, namespace string) error {
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
	_, err := k8s.DynamicClient.Resource(VMGVR).Namespace(namespace).Create(context.Background(), vmObj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create VirtualMachine: %w", err)
	}

	log.Printf("✅ VirtualMachine created: %s/%s", namespace, vmSpec.Metadata.Name)
	return nil
}

// generateRandomSuffix generates a random suffix for resource names
func generateRandomSuffix(length int) string {
	suffix, _ := k8s.GenerateJobSuffix()
	if len(suffix) > length {
		return suffix[:length]
	}
	return suffix
}
