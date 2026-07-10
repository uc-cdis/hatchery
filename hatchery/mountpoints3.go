package hatchery

import (
	"context"
	"fmt"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

// mountpointS3CSIDriver is the CSI driver name for AWS Mountpoint for S3.
const mountpointS3CSIDriver = "s3.csi.aws.com"

// mountpointS3PVSpec holds all parameters needed to create a statically-provisioned
// Mountpoint-S3 CSI PersistentVolume and its bound PersistentVolumeClaim.
type mountpointS3PVSpec struct {
	PVName       string
	PVCName      string
	Namespace    string
	Labels       map[string]string
	BucketName   string
	VolumeHandle string
	MountOptions []string
	AccessMode   k8sv1.PersistentVolumeAccessMode
}

// createMountpointS3PVAndPVC creates a statically-provisioned PV backed by the
// Mountpoint S3 CSI driver and a PVC directly bound to it. If PVC creation fails,
// the PV is deleted to avoid orphaned resources.
func createMountpointS3PVAndPVC(ctx context.Context, podClient corev1.CoreV1Interface, spec mountpointS3PVSpec) error {
	storageSize := resource.MustParse("1Gi")
	storageClass := ""

	pv := &k8sv1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:   spec.PVName,
			Labels: spec.Labels,
		},
		Spec: k8sv1.PersistentVolumeSpec{
			Capacity: k8sv1.ResourceList{
				k8sv1.ResourceStorage: storageSize,
			},
			AccessModes:                   []k8sv1.PersistentVolumeAccessMode{spec.AccessMode},
			PersistentVolumeReclaimPolicy: k8sv1.PersistentVolumeReclaimDelete,
			StorageClassName:              storageClass,
			MountOptions:                  spec.MountOptions,
			PersistentVolumeSource: k8sv1.PersistentVolumeSource{
				CSI: &k8sv1.CSIPersistentVolumeSource{
					Driver:       mountpointS3CSIDriver,
					VolumeHandle: spec.VolumeHandle,
					VolumeAttributes: map[string]string{
						"bucketName":           spec.BucketName,
						"authenticationSource": "pod",
						"stsRegion":            "us-east-1",
					},
				},
			},
			ClaimRef: &k8sv1.ObjectReference{
				Kind:      "PersistentVolumeClaim",
				Namespace: spec.Namespace,
				Name:      spec.PVCName,
			},
		},
	}

	if _, err := podClient.PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create shared PV %s: %w", spec.PVName, err)
	}

	pvc := &k8sv1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.PVCName,
			Namespace: spec.Namespace,
			Labels:    spec.Labels,
		},
		Spec: k8sv1.PersistentVolumeClaimSpec{
			AccessModes:      []k8sv1.PersistentVolumeAccessMode{spec.AccessMode},
			StorageClassName: &storageClass,
			VolumeName:       spec.PVName,
			Resources: k8sv1.VolumeResourceRequirements{
				Requests: k8sv1.ResourceList{
					k8sv1.ResourceStorage: storageSize,
				},
			},
		},
	}

	if _, err := podClient.PersistentVolumeClaims(spec.Namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
		_ = podClient.PersistentVolumes().Delete(ctx, spec.PVName, metav1.DeleteOptions{})
		return fmt.Errorf("failed to create shared PVC %s: %w", spec.PVCName, err)
	}

	return nil
}
