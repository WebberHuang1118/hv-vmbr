package k8s

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/webberhuang/hv-vmbr/pkg/logutil"
)

var (
	Clientset     *kubernetes.Clientset
	DynamicClient dynamic.Interface
	RestMapper    meta.RESTMapper

	// VsGVR is the GroupVersionResource for VolumeSnapshot.
	VsGVR = schema.GroupVersionResource{
		Group:    "snapshot.storage.k8s.io",
		Version:  "v1",
		Resource: "volumesnapshots",
	}
)

// InitK8sClients initializes both typed and dynamic Kubernetes clients.
func InitK8sClients(kubeconfig string) error {
	var config *rest.Config
	var err error
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	}
	if err != nil {
		return fmt.Errorf("error building kubeconfig: %w", err)
	}
	Clientset, err = kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("error creating clientset: %w", err)
	}
	DynamicClient, err = dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("error creating dynamic client: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return fmt.Errorf("error creating discovery client: %w", err)
	}
	RestMapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))
	return nil
}

// ReplacePlaceholders is a helper for substituting placeholders in a string.
// This remains available for custom replacements outside of ApplyManifest.
func ReplacePlaceholders(manifest string, replacements map[string]string) string {
	for key, value := range replacements {
		placeholder := fmt.Sprintf("{{%s}}", key)
		manifest = strings.ReplaceAll(manifest, placeholder, value)
	}
	return manifest
}

// CleanupResources deletes temporary resources such as PVC clones and VolumeSnapshots.
func CleanupResources(namespace, vsName, pvcCloneName string, vsCreated, pvcCloneCreated bool) {
	if pvcCloneCreated {
		logutil.Info(fmt.Sprintf("Deleting PVC clone %s...", pvcCloneName))
		if err := Clientset.CoreV1().PersistentVolumeClaims(namespace).Delete(context.Background(), pvcCloneName, metav1.DeleteOptions{}); err != nil {
			logutil.Error(fmt.Sprintf("Failed to delete PVC clone %s: %v", pvcCloneName, err))
		} else {
			logutil.Info(fmt.Sprintf("PVC clone %s deleted.", pvcCloneName))
		}
	}
	if vsCreated {
		logutil.Info(fmt.Sprintf("Deleting VolumeSnapshot %s...", vsName))
		if err := DynamicClient.Resource(VsGVR).Namespace(namespace).Delete(context.Background(), vsName, metav1.DeleteOptions{}); err != nil {
			logutil.Error(fmt.Sprintf("Failed to delete VolumeSnapshot %s: %v", vsName, err))
		} else {
			logutil.Info(fmt.Sprintf("VolumeSnapshot %s deleted.", vsName))
		}
	}
}

// ApplyManifest applies the given manifest to the cluster.
// It always replaces the default placeholders for {{NAMESPACE}} and {{NAME}} (the object's name).
// Any additional substitutions are provided via extraReplacements.
// (For example, if your PVC name is needed in the manifest, supply it in extraReplacements with key "PVC_NAME".)
func ApplyManifest(manifest, namespace, defaultName string, extraReplacements map[string]string) error {
	// Replace the default tokens.
	manifest = strings.ReplaceAll(manifest, "{{NAMESPACE}}", namespace)
	manifest = strings.ReplaceAll(manifest, "{{NAME}}", defaultName)

	// Substitute additional replacements.
	for key, value := range extraReplacements {
		// Skip keys that belong to defaults.
		if key == "NAMESPACE" || key == "NAME" {
			continue
		}
		placeholder := fmt.Sprintf("{{%s}}", key)
		manifest = strings.ReplaceAll(manifest, placeholder, value)
	}

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(manifest)), 4096)
	for {
		var obj unstructured.Unstructured
		err := decoder.Decode(&obj)
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("error decoding YAML: %w", err)
		}
		if len(obj.Object) == 0 {
			continue
		}
		// If the object did not get a namespace, set it.
		if obj.GetNamespace() == "" && namespace != "" {
			obj.SetNamespace(namespace)
		}
		gvk := obj.GroupVersionKind()
		mapping, err := RestMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return fmt.Errorf("failed to get REST mapping for %v: %w", gvk, err)
		}
		var dr dynamic.ResourceInterface
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			dr = DynamicClient.Resource(mapping.Resource).Namespace(obj.GetNamespace())
		} else {
			dr = DynamicClient.Resource(mapping.Resource)
		}
		_, err = dr.Create(context.Background(), &obj, metav1.CreateOptions{})
		if err != nil {
			if apierrors.IsAlreadyExists(err) {
				existing, err := dr.Get(context.Background(), obj.GetName(), metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("failed to get existing object %s: %w", obj.GetName(), err)
				}
				obj.SetResourceVersion(existing.GetResourceVersion())
				_, err = dr.Update(context.Background(), &obj, metav1.UpdateOptions{})
				if err != nil {
					return fmt.Errorf("failed to update object %s: %w", obj.GetName(), err)
				}
			} else {
				return fmt.Errorf("failed to create object %s: %w", obj.GetName(), err)
			}
		}
	}
	return nil
}

// WaitForJob waits until the specified Job succeeds, or until a timeout occurs.
func WaitForJob(jobName, namespace string, timeout time.Duration) error {
	msg := fmt.Sprintf("Waiting for job %s in namespace %s...", jobName, namespace)
	logutil.Info(msg)
	start := time.Now()
	for {
		job, err := Clientset.BatchV1().Jobs(namespace).Get(context.Background(), jobName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("error getting job %s: %w", jobName, err)
		}
		if job.Status.Succeeded >= 1 {
			logutil.Info(fmt.Sprintf("Job %s succeeded.", jobName))
			return nil
		}
		if time.Since(start) > timeout {
			return fmt.Errorf("timeout waiting for job %s", jobName)
		}
		time.Sleep(1 * time.Second)
	}
}

// WaitForVolumeSnapshot waits until the VolumeSnapshot is ready to use.
func WaitForVolumeSnapshot(vsName, namespace string, timeout time.Duration) error {
	spinner := []string{"⌛→", "⌛↑", "⌛←", "⌛↓"}
	msg := fmt.Sprintf("Waiting for VolumeSnapshot %s in namespace %s...", vsName, namespace)
	start := time.Now()
	i := 0
	for {
		obj, err := DynamicClient.Resource(VsGVR).Namespace(namespace).Get(context.Background(), vsName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("error getting VolumeSnapshot %s: %w", vsName, err)
		}
		ready, found, err := unstructured.NestedBool(obj.Object, "status", "readyToUse")
		if err != nil || !found || !ready {
			if time.Since(start) > timeout {
				return fmt.Errorf("VolumeSnapshot %s not ready within timeout", vsName)
			}
			fmt.Printf("\r%s %s", msg, spinner[i%len(spinner)])
			i++
			time.Sleep(150 * time.Millisecond)
			continue
		}
		fmt.Printf("\r✅ VolumeSnapshot %s is ready.\n", vsName)
		return nil
	}
}

// WaitForPVCBound waits until the specified PVC is in Bound state.
func WaitForPVCBound(pvcName, namespace string, timeout time.Duration) error {
	spinner := []string{"⌛→", "⌛↑", "⌛←", "⌛↓"}
	msg := fmt.Sprintf("Waiting for PVC %s to become Bound in namespace %s...", pvcName, namespace)
	start := time.Now()
	i := 0
	for {
		pvc, err := Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(context.Background(), pvcName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("error getting PVC %s: %w", pvcName, err)
		}
		if pvc.Status.Phase == "Bound" {
			fmt.Printf("\r✅ PVC %s is Bound.\n", pvcName)
			return nil
		}
		if time.Since(start) > timeout {
			return fmt.Errorf("PVC %s did not become Bound within timeout", pvcName)
		}
		fmt.Printf("\r%s %s", msg, spinner[i%len(spinner)])
		i++
		time.Sleep(150 * time.Millisecond)
	}
}

// GetPVCStorageClass retrieves the storage class of the PVC.
func GetPVCStorageClass(pvcName, namespace string) (string, error) {
	pvc, err := Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(context.Background(), pvcName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if pvc.Spec.StorageClassName == nil {
		return "", fmt.Errorf("no storage class found for PVC %s", pvcName)
	}
	return *pvc.Spec.StorageClassName, nil
}

// GetPVCStorageSize retrieves the storage request size of the PVC.
func GetPVCStorageSize(pvcName, namespace string) (string, error) {
	pvc, err := Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(context.Background(), pvcName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	storage, found := pvc.Spec.Resources.Requests["storage"]
	if !found {
		return "", fmt.Errorf("no storage request found for PVC %s", pvcName)
	}
	return storage.String(), nil
}

// GetPVCVolumeMode retrieves the volume mode of the PVC.
func GetPVCVolumeMode(pvcName, namespace string) (string, error) {
	pvc, err := Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(context.Background(), pvcName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if pvc.Spec.VolumeMode == nil {
		return "Filesystem", nil
	}
	return string(*pvc.Spec.VolumeMode), nil
}

// GetPVCVolumeName retrieves the volume name (PV) bound to the PVC.
func GetPVCVolumeName(pvcName, namespace string) (string, error) {
	pvc, err := Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(context.Background(), pvcName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if pvc.Spec.VolumeName == "" {
		return "", fmt.Errorf("PVC %s is not bound to any PV", pvcName)
	}
	return pvc.Spec.VolumeName, nil
}

// GetPVCSIDriver retrieves the CSI driver name from the PV bound to the PVC.
// It first checks the PV's CSI driver, then falls back to PVC annotations.
func GetPVCSIDriver(pvcName, namespace string) (string, error) {
	pvc, err := Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(context.Background(), pvcName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	// If PVC is bound to a PV, get the CSI driver from the PV
	if pvc.Spec.VolumeName != "" {
		pv, err := Clientset.CoreV1().PersistentVolumes().Get(context.Background(), pvc.Spec.VolumeName, metav1.GetOptions{})
		if err == nil && pv.Spec.CSI != nil {
			return pv.Spec.CSI.Driver, nil
		}
	}

	// Fall back to PVC annotation
	if driver, ok := pvc.Annotations["volume.kubernetes.io/storage-provisioner"]; ok {
		return driver, nil
	}

	// Last resort: use storage class name
	if pvc.Spec.StorageClassName != nil {
		return *pvc.Spec.StorageClassName, nil
	}

	return "", fmt.Errorf("unable to determine CSI driver for PVC %s", pvcName)
}

// findRunningPod locates a running pod for the given job and container.
func findRunningPod(jobName, namespace, container string, retryCount int) (string, error) {
	labelSelector := fmt.Sprintf("job-name=%s", jobName)

	for i := 0; i < retryCount; i++ {
		podList, err := Clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return "", fmt.Errorf("error listing pods for job %s: %w", jobName, err)
		}

		for _, pod := range podList.Items {
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.Name == container && cs.State.Running != nil {
					return pod.Name, nil
				}
			}
		}

		time.Sleep(6 * time.Second)
	}

	return "", fmt.Errorf("no running pod for job %s with container %s after %d retries", jobName, container, retryCount)
}

// StreamJobProgressPercentage streams logs from a job's container and parses progress metrics.
func StreamJobProgressPercentage(jobName, namespace, container, progressLabel string) error {
	podName, err := findRunningPod(jobName, namespace, container, 10)
	if err != nil {
		return err
	}

	opts := &corev1.PodLogOptions{
		Container: container,
		Follow:    true,
	}
	req := Clientset.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(context.TODO())
	if err != nil {
		return fmt.Errorf("error streaming logs for pod %s (container %s): %w", podName, container, err)
	}
	defer stream.Close()

	return parseProgressLogs(stream, progressLabel)
}

// parseProgressLogs scans log stream and prints progress updates.
func parseProgressLogs(stream io.ReadCloser, progressLabel string) error {
	scanner := bufio.NewScanner(stream)
	format := progressLabel + " %d/%d bytes (%f%%)"

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, progressLabel) {
			continue
		}

		var current, total int
		var percent float64
		if n, _ := fmt.Sscanf(line, format, &current, &total, &percent); n == 3 {
			log.Printf("progress: %.2f%%", percent)
		}
	}

	return scanner.Err()
}

// GetJobLogs retrieves complete logs (non-streaming) from the first pod of the given job and container.
func GetJobLogs(jobName, namespace, container string) (string, error) {
	var podName string
	const retryCount = 50
	for i := 0; i < retryCount; i++ {
		labelSelector := fmt.Sprintf("job-name=%s", jobName)
		podList, err := Clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return "", fmt.Errorf("error listing pods for job %s: %w", jobName, err)
		}
		for _, pod := range podList.Items {
			for _, cs := range pod.Status.ContainerStatuses {
				// Check if the container is running or has already terminated successfully.
				if cs.Name == container && (cs.State.Running != nil || cs.State.Terminated != nil) {
					podName = pod.Name
					break
				}
			}
			if podName != "" {
				break
			}
		}
		if podName != "" {
			break
		}
		time.Sleep(6 * time.Second)
	}
	if podName == "" {
		return "", fmt.Errorf("no running pod for job %s with container %s after multiple retries", jobName, container)
	}

	opts := &corev1.PodLogOptions{
		Container: container,
		Follow:    false,
	}
	req := Clientset.CoreV1().Pods(namespace).GetLogs(podName, opts)
	logsBytes, err := req.Do(context.TODO()).Raw()
	if err != nil {
		return "", fmt.Errorf("error retrieving logs from pod %s: %w", podName, err)
	}
	return string(logsBytes), nil
}

// GenerateJobSuffix generates a random hexadecimal string to use as a unique job suffix.
func GenerateJobSuffix() (string, error) {
	bytes := make([]byte, 4) // Adjust the length as needed.
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
