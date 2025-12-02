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
func RunVMBackup(namespace, vmName, backupName, vsc, awsID, awsSecret, repository, password string, repoInitialized bool) {
	log.Printf("🔧 Starting VM backup for %s/%s", namespace, vmName)

	// Step 1: Get the VM object
	vmObj, err := k8s.DynamicClient.Resource(VMGVR).Namespace(namespace).Get(context.Background(), vmName, metav1.GetOptions{})
	if err != nil {
		log.Fatalf("❌ Failed to get VirtualMachine %s: %v", vmName, err)
	}

	// Step 2: Sanitize the VM manifest
	sanitizedVM := sanitizeVMManifest(vmObj)

	// Step 3: Extract PVCs from VM volumes
	pvcList := extractPVCsFromVM(vmObj)
	if len(pvcList) == 0 {
		log.Println("⚠️  No PVCs found in VM, backing up manifest only")
	}

	// Step 4: Backup each PVC
	volumeBackups := []VolumeBackup{}
	for _, pvcName := range pvcList {
		log.Printf("📦 Backing up PVC: %s", pvcName)

		// Use backupName as the tag for PVC snapshots
		pvcSnapshotTag := fmt.Sprintf("%s-pvc-%s", backupName, pvcName)
		backup.RunBackup(namespace, pvcName, pvcSnapshotTag, vsc, awsID, awsSecret, repository, password, repoInitialized)
		repoInitialized = true // After first backup, repo is initialized

		// Find the snapshot ID that was just created
		snapshotID, err := find.RunFind(namespace, pvcSnapshotTag, awsID, awsSecret, repository, password)
		if err != nil {
			log.Fatalf("❌ Failed to verify backup for PVC %s: %v", pvcName, err)
		}

		// Get PVC details
		pvc, err := k8s.Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(context.Background(), pvcName, metav1.GetOptions{})
		if err != nil {
			log.Fatalf("❌ Failed to get PVC %s: %v", pvcName, err)
		}

		// Get volume name from VM spec
		volumeName := getVolumeNameForPVC(vmObj, pvcName)

		volumeBackup := VolumeBackup{
			Name:                  fmt.Sprintf("%s-volume-%s", backupName, pvcName),
			VolumeName:            volumeName,
			CSIDriverName:         getCSIDriverName(pvc),
			PersistentVolumeClaim: *pvc,
			ResticSnapshotID:      snapshotID,
			VolumeSize:            pvc.Spec.Resources.Requests.Storage().Value(),
			Progress:              100,
		}
		volumeBackups = append(volumeBackups, volumeBackup)
		log.Printf("✅ PVC %s backed up with snapshot ID: %s", pvcName, snapshotID)
	}

	// Step 5: Extract and backup secrets referenced in VM
	secretBackups := extractAndBackupSecrets(vmObj, namespace)

	// Step 6: Create backup config
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

	// Step 7: Save backup config as JSON and upload to restic
	if err := saveBackupConfig(backupConfig, namespace, backupName, awsID, awsSecret, repository, password); err != nil {
		log.Fatalf("❌ Failed to save backup config: %v", err)
	}

	log.Printf("✅ VM backup completed successfully: %s", backupName)
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

// getCSIDriverName extracts CSI driver from PVC annotations or storage class
func getCSIDriverName(pvc *corev1.PersistentVolumeClaim) string {
	// Try to get from annotations
	if driver, ok := pvc.Annotations["volume.kubernetes.io/storage-provisioner"]; ok {
		return driver
	}
	// Default to storage class name
	if pvc.Spec.StorageClassName != nil {
		return *pvc.Spec.StorageClassName
	}
	return "unknown"
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
      - name: backup-config
        image: webberhuang/restic-accelerated:latest
        imagePullPolicy: IfNotPresent
        command: ["/bin/sh", "-c"]
        args:
          - export AWS_ACCESS_KEY_ID={{AWS_ACCESS_KEY_ID}} && export AWS_SECRET_ACCESS_KEY={{AWS_SECRET_ACCESS_KEY}} && export RESTIC_REPOSITORY={{RESTIC_REPOSITORY}} && export RESTIC_PASSWORD={{RESTIC_PASSWORD}} && cat /config/{{FILENAME}} | restic backup --stdin --stdin-filename /config/{{FILENAME}} --tag=ns={{NAMESPACE}},sn={{SNAPSHOT}},type=vm-config
        volumeMounts:
        - name: config
          mountPath: /config
      volumes:
      - name: config
        configMap:
          name: {{CONFIGMAP_NAME}}
`

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
	if err := k8s.ApplyManifest(manifestTemplate, namespace, jobName, replacements); err != nil {
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
