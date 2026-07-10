package hatchery

import (
	"context"
	"fmt"
	"testing"

	k8sv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCreateMountpointS3PVAndPVC(t *testing.T) {
	defer SetupAndTeardownTest()()

	spec := mountpointS3PVSpec{
		PVName:       "test-pv",
		PVCName:      "test-pvc",
		Namespace:    "test-ns",
		Labels:       map[string]string{"app": "test"},
		BucketName:   "my-bucket",
		VolumeHandle: "my-bucket",
		MountOptions: []string{"--read-only"},
		AccessMode:   k8sv1.ReadOnlyMany,
	}

	testCases := []struct {
		name        string
		failPV      bool
		failPVC     bool
		expectError bool
	}{
		{name: "success"},
		{name: "pvCreateFailure", failPV: true, expectError: true},
		{name: "pvcCreateFailure", failPVC: true, expectError: true},
	}

	for _, tc := range testCases {
		t.Logf("Testing createMountpointS3PVAndPVC when %s", tc.name)
		clientset := fake.NewSimpleClientset()

		if tc.failPV {
			clientset.PrependReactor("create", "persistentvolumes", func(action k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, fmt.Errorf("simulated PV error")
			})
		}
		if tc.failPVC {
			clientset.PrependReactor("create", "persistentvolumeclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, fmt.Errorf("simulated PVC error")
			})
		}

		err := createMountpointS3PVAndPVC(context.Background(), clientset.CoreV1(), spec)

		if tc.expectError && err == nil {
			t.Errorf("expected error but got nil")
		}
		if !tc.expectError && err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		// On success: verify PV and PVC were created with correct fields
		if !tc.expectError {
			pv, pvErr := clientset.CoreV1().PersistentVolumes().Get(context.Background(), spec.PVName, metav1.GetOptions{})
			if pvErr != nil {
				t.Errorf("expected PV to exist after success: %v", pvErr)
			} else {
				if pv.Spec.CSI.Driver != mountpointS3CSIDriver {
					t.Errorf("expected CSI driver %q, got %q", mountpointS3CSIDriver, pv.Spec.CSI.Driver)
				}
				if pv.Spec.CSI.VolumeAttributes["bucketName"] != spec.BucketName {
					t.Errorf("expected bucketName %q, got %q", spec.BucketName, pv.Spec.CSI.VolumeAttributes["bucketName"])
				}
			}

			pvc, pvcErr := clientset.CoreV1().PersistentVolumeClaims(spec.Namespace).Get(context.Background(), spec.PVCName, metav1.GetOptions{})
			if pvcErr != nil {
				t.Errorf("expected PVC to exist after success: %v", pvcErr)
			} else if pvc.Spec.VolumeName != spec.PVName {
				t.Errorf("expected PVC to be bound to PV %q, got %q", spec.PVName, pvc.Spec.VolumeName)
			}
		}

		// On PVC failure: verify PV was cleaned up
		if tc.failPVC {
			_, pvErr := clientset.CoreV1().PersistentVolumes().Get(context.Background(), spec.PVName, metav1.GetOptions{})
			if pvErr == nil || !k8serrors.IsNotFound(pvErr) {
				t.Errorf("expected PV to be deleted after PVC creation failure")
			}
		}
	}
}
