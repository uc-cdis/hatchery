package hatchery

import (
	"context"
	"fmt"
	"io"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

const sharedWorkspaceLabelKey = "hatchery-shared-user"

// SharedWorkspacePrefix represents one S3 prefix the user can access.
type SharedWorkspacePrefix struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

// sharedPVName returns the cluster-scoped PersistentVolume name for a user+prefix pair.
func sharedPVName(userName, prefixName string) string {
	return fmt.Sprintf("shared-%s-%s", escapism(userName), escapism(prefixName))
}

// sharedPVCName returns the PersistentVolumeClaim name for a user+prefix pair.
func sharedPVCName(userName, prefixName string) string {
	return fmt.Sprintf("shared-claim-%s-%s", escapism(userName), escapism(prefixName))
}

// getSharedWorkspacePrefixes calls the configured external API with the user's
// bearer token and returns the list of S3 prefixes the user may access.
func getSharedWorkspacePrefixes(ctx context.Context, accessToken string) ([]SharedWorkspacePrefix, error) {
	endpoint := "http://arborist-service/user"
	resp, err := MakeARequestWithContext(ctx, "GET", endpoint, accessToken, "application/json", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("shared workspace API request failed: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			Config.Logger.Printf("Error closing shared workspace API response body: %v", err)
		}
	}()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("shared workspace API returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		Config.Logger.Fatal(err)
	}

	Config.Logger.Println(string(body))

	// var prefixes []SharedWorkspacePrefix
	// if err := json.NewDecoder(resp.Body).Decode(&prefixes); err != nil {
	// 	return nil, fmt.Errorf("failed to decode shared workspace API response: %w", err)
	// }

	prefixes := []SharedWorkspacePrefix{
		{Name: "group-a", Prefix: "A/"},
		{Name: "group-b", Prefix: "B/"},
	}

	return prefixes, nil
}

// cleanupUserSharedWorkspaces deletes all shared PVCs (and their bound PVs) for
// a user that were created in a prior launch. Failures are logged but not returned,
// so stale resources do not block a new launch.
func cleanupUserSharedWorkspaces(ctx context.Context, podClient corev1.CoreV1Interface, userName, namespace string) error {
	labelSelector := fmt.Sprintf("%s=%s", sharedWorkspaceLabelKey, escapism(userName))
	pvcs, err := podClient.PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return fmt.Errorf("failed to list shared PVCs for user %s: %w", userName, err)
	}
	policy := metav1.DeletePropagationBackground
	deleteOpts := metav1.DeleteOptions{PropagationPolicy: &policy}
	for _, pvc := range pvcs.Items {
		pvName := pvc.Spec.VolumeName
		if err := podClient.PersistentVolumeClaims(namespace).Delete(ctx, pvc.Name, deleteOpts); err != nil {
			Config.Logger.Printf("Warning: failed to delete shared PVC %s for user %s: %v (continuing)", pvc.Name, userName, err)
		}
		if pvName != "" {
			if err := podClient.PersistentVolumes().Delete(ctx, pvName, deleteOpts); err != nil {
				Config.Logger.Printf("Warning: failed to delete shared PV %s for user %s: %v (continuing)", pvName, userName, err)
			}
		}
	}
	return nil
}

// createSharedWorkspacePVAndPVC creates a statically-provisioned CSI PersistentVolume
// backed by the Mountpoint S3 driver and a PersistentVolumeClaim directly bound to it.
func createSharedWorkspacePVAndPVC(ctx context.Context, podClient corev1.CoreV1Interface, userName, namespace string, prefix SharedWorkspacePrefix) error {
	swCfg := Config.Config.SharedWorkspace
	pvName := sharedPVName(userName, prefix.Name)
	pvcName := sharedPVCName(userName, prefix.Name)
	labels := map[string]string{
		sharedWorkspaceLabelKey: escapism(userName),
	}
	storageSize := resource.MustParse("1Gi")
	storageClass := ""
	volumeHandle := fmt.Sprintf("%s/%s/%s", swCfg.S3BucketName, escapism(userName), escapism(prefix.Name))

	pv := &k8sv1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:   pvName,
			Labels: labels,
		},
		Spec: k8sv1.PersistentVolumeSpec{
			Capacity: k8sv1.ResourceList{
				k8sv1.ResourceStorage: storageSize,
			},
			AccessModes:                   []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadOnlyMany},
			PersistentVolumeReclaimPolicy: k8sv1.PersistentVolumeReclaimDelete,
			StorageClassName:              storageClass,
			MountOptions:                  []string{fmt.Sprintf("--prefix=%s", prefix.Prefix), "--read-only"},
			PersistentVolumeSource: k8sv1.PersistentVolumeSource{
				CSI: &k8sv1.CSIPersistentVolumeSource{
					Driver:       "s3.csi.aws.com",
					VolumeHandle: volumeHandle,
					VolumeAttributes: map[string]string{
						"bucketName": swCfg.S3BucketName,
					},
				},
			},
			ClaimRef: &k8sv1.ObjectReference{
				Kind:      "PersistentVolumeClaim",
				Namespace: namespace,
				Name:      pvcName,
			},
		},
	}

	if _, err := podClient.PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create shared PV %s: %w", pvName, err)
	}

	pvc := &k8sv1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: k8sv1.PersistentVolumeClaimSpec{
			AccessModes:      []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadOnlyMany},
			StorageClassName: &storageClass,
			VolumeName:       pvName,
			Resources: k8sv1.VolumeResourceRequirements{
				Requests: k8sv1.ResourceList{
					k8sv1.ResourceStorage: storageSize,
				},
			},
		},
	}

	if _, err := podClient.PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
		_ = podClient.PersistentVolumes().Delete(ctx, pvName, metav1.DeleteOptions{})
		return fmt.Errorf("failed to create shared PVC %s: %w", pvcName, err)
	}

	return nil
}

// addSharedWorkspaceVolumesToPod mutates the pod spec in-place, appending one
// PVC-backed volume and one read-only VolumeMount on hatchery-container per prefix.
func addSharedWorkspaceVolumesToPod(pod *k8sv1.Pod, userName string, prefixes []SharedWorkspacePrefix, mountBasePath string) {
	if mountBasePath == "" {
		mountBasePath = "/home/jovyan/shared"
	}
	for _, prefix := range prefixes {
		pvcName := sharedPVCName(userName, prefix.Name)
		volName := fmt.Sprintf("shared-%s", escapism(prefix.Name))
		mountPath := fmt.Sprintf("%s/%s", mountBasePath, prefix.Name)

		pod.Spec.Volumes = append(pod.Spec.Volumes, k8sv1.Volume{
			Name: volName,
			VolumeSource: k8sv1.VolumeSource{
				PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
					ReadOnly:  true,
				},
			},
		})

		for i := range pod.Spec.Containers {
			if pod.Spec.Containers[i].Name == "hatchery-container" {
				pod.Spec.Containers[i].VolumeMounts = append(
					pod.Spec.Containers[i].VolumeMounts,
					k8sv1.VolumeMount{
						Name:      volName,
						MountPath: mountPath,
						ReadOnly:  true,
					},
				)
				break
			}
		}
	}
}
