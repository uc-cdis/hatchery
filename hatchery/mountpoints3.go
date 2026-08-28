package hatchery

import (
	"context"
	"fmt"
	"strings"

	k8sv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

// mountpointS3CSIDriver is the CSI driver name for AWS Mountpoint for S3.
const mountpointS3CSIDriver = "s3.csi.aws.com"

// softwareLibraryLabelKey marks the PV/PVC pair backing the shared squashfs
// software library, so it can be identified independently of user-scoped mounts.
const softwareLibraryLabelKey = "hatchery-software-library"

// defaultSTSRegion is used when no region is configured for a Mountpoint-S3 volume.
const defaultSTSRegion = "us-east-1"

// softwareLibraryBucket describes the resolved location of the squashfs software
// library image and how to read it.
type softwareLibraryBucket struct {
	Name      string
	Region    string
	Prefix    string // normalized with a trailing slash; "" means the bucket root
	KMSKeyARN string // empty unless the bucket uses a customer managed key
}

// resolveSoftwareLibraryBucket returns the bucket backing the squashfs software
// library. Every field comes from the commons-wide "s3-config" unless the
// container overrides it in its own "squashfs_mount" block.
//
// Both the PV and the IAM policy granting access to it are derived from this one
// function: if they disagreed on the bucket, the role would grant access to one
// bucket while the volume mounted another, which fails at mount time in a way
// that is hard to trace back to config.
func resolveSoftwareLibraryBucket(opts SquashFSMountConfig) (softwareLibraryBucket, error) {
	s3Cfg := Config.Config.S3Config

	resolved := softwareLibraryBucket{
		Name:      firstNonEmpty(opts.BucketName, s3Cfg.BucketName),
		Region:    firstNonEmpty(opts.Region, s3Cfg.Region, defaultSTSRegion),
		Prefix:    normalizeS3Prefix(firstNonEmpty(opts.BucketPrefix, s3Cfg.BucketPrefix)),
		KMSKeyARN: firstNonEmpty(opts.KMSKeyARN, s3Cfg.KMSKeyARN),
	}
	if resolved.Name == "" {
		return softwareLibraryBucket{}, fmt.Errorf("no S3 bucket configured for the squashfs software library: set s3-config.bucketName or squashfs_mount.bucket_name")
	}
	return resolved, nil
}

// firstNonEmpty returns the first non-empty value, or "" when there is none.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// mountpointS3PVSpec holds all parameters needed to create a statically-provisioned
// Mountpoint-S3 CSI PersistentVolume and its bound PersistentVolumeClaim.
type mountpointS3PVSpec struct {
	PVName       string
	PVCName      string
	Namespace    string
	Labels       map[string]string
	BucketName   string
	MountOptions []string
	AccessMode   k8sv1.PersistentVolumeAccessMode
	// Region is the STS region the CSI driver uses to assume the pod's role.
	// Defaults to "us-east-1" when empty.
	Region string
}

// createMountpointS3PVAndPVC creates a statically-provisioned PV backed by the
// Mountpoint S3 CSI driver and a PVC directly bound to it. If PVC creation fails,
// the PV is deleted to avoid orphaned resources.
func createMountpointS3PVAndPVC(ctx context.Context, podClient corev1.CoreV1Interface, spec mountpointS3PVSpec) error {
	storageSize := resource.MustParse("1Gi")
	storageClass := ""
	stsRegion := spec.Region
	if stsRegion == "" {
		stsRegion = defaultSTSRegion
	}

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
					Driver: mountpointS3CSIDriver,
					// Must equal the PV name. On teardown the driver parses the volume
					// ID out of the kubelet mount path, which is built from the PV
					// name, and refuses to unmount when that disagrees with the handle
					// it was passed. The pod then never finishes terminating.
					VolumeHandle: spec.PVName,
					VolumeAttributes: map[string]string{
						"bucketName":           spec.BucketName,
						"authenticationSource": "pod",
						"stsRegion":            stsRegion,
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

// ensureSoftwareLibraryPVAndPVC makes sure the read-only Mountpoint-S3 PV/PVC
// backing the squashfs software library exists in the given namespace. The PVC is
// shared by every workspace pod in the namespace rather than being per-user, so
// this is idempotent: an existing claim is left untouched and reported as ready.
func ensureSoftwareLibraryPVAndPVC(ctx context.Context, podClient corev1.CoreV1Interface, namespace, pvcName string, opts SquashFSMountConfig) error {
	bucket, err := resolveSoftwareLibraryBucket(opts)
	if err != nil {
		return err
	}

	// A PVC already bound in this namespace is reused as-is. Any other Get error
	// is surfaced so we do not attempt to create over a claim we cannot read.
	_, err = podClient.PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err == nil {
		Config.Logger.Printf("Software library PVC %s already exists in namespace %s, reusing it", pvcName, namespace)
		return nil
	}
	if !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to check for existing software library PVC %s: %w", pvcName, err)
	}

	// The PV is cluster-scoped, so its name is namespaced by hand to avoid
	// colliding with another namespace's claim of the same library.
	pvName := fmt.Sprintf("%s-%s-pv", pvcName, escapism(namespace))
	mountOptions := []string{"read-only", "uid=1010", "gid=100"}
	if bucket.Prefix != "" {
		mountOptions = append(mountOptions, fmt.Sprintf("prefix=%s", bucket.Prefix))
	}

	Config.Logger.Printf("Creating software library PV %s / PVC %s for bucket %s (region %s)", pvName, pvcName, bucket.Name, bucket.Region)

	// A stale PV from a previous run would block the new claim from binding, so
	// clear it before recreating the pair.
	if existing, getErr := podClient.PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{}); getErr == nil {
		Config.Logger.Printf("Deleting orphaned software library PV %s before recreating it", existing.Name)
		if delErr := podClient.PersistentVolumes().Delete(ctx, pvName, metav1.DeleteOptions{}); delErr != nil && !k8serrors.IsNotFound(delErr) {
			return fmt.Errorf("failed to delete orphaned software library PV %s: %w", pvName, delErr)
		}
	} else if !k8serrors.IsNotFound(getErr) {
		return fmt.Errorf("failed to check for existing software library PV %s: %w", pvName, getErr)
	}

	err = createMountpointS3PVAndPVC(ctx, podClient, mountpointS3PVSpec{
		PVName:       pvName,
		PVCName:      pvcName,
		Namespace:    namespace,
		Labels:       map[string]string{softwareLibraryLabelKey: "true"},
		BucketName:   bucket.Name,
		MountOptions: mountOptions,
		AccessMode:   k8sv1.ReadOnlyMany,
		Region:       bucket.Region,
	})
	// Another pod launching concurrently may have won the race; that is success.
	if err != nil && k8serrors.IsAlreadyExists(err) {
		Config.Logger.Printf("Software library PVC %s was created concurrently, reusing it", pvcName)
		return nil
	}
	return err
}

// normalizeS3Prefix trims leading slashes and ensures a single trailing slash so
// the value is usable as a Mountpoint-S3 "prefix=" mount option. Returns "" for
// an empty or root prefix, meaning the bucket root is mounted.
func normalizeS3Prefix(prefix string) string {
	trimmed := strings.Trim(prefix, "/")
	if trimmed == "" {
		return ""
	}
	return trimmed + "/"
}
