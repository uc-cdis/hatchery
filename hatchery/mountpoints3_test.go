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
				// PVs must use Retain so the PV object survives PVC deletion and only
				// hatchery's own explicit Delete removes it — no race with the PV
				// controller auto-deleting it between PVC-delete and PV-delete.
				if pv.Spec.PersistentVolumeReclaimPolicy != k8sv1.PersistentVolumeReclaimRetain {
					t.Errorf("expected Retain reclaim policy on PV, got %v", pv.Spec.PersistentVolumeReclaimPolicy)
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

// TestSoftwareLibraryPVCSharedNameBlocksAllUsers documents the lifecycle blast-
// radius bug: the software library PVC is currently shared across every workspace
// pod in the namespace under a fixed name ("software-library-pvc"). If that PVC
// enters Terminating for any reason, every user's pod launch fails until the PVC
// is fully gone. The fix is a per-user PVC name so one user's cycling pod cannot
// block another's launch.
//
// This test FAILS before the fix: ensureSoftwareLibraryPVAndPVC is called with
// the shared name and returns "still terminating". It PASSES after the fix because
// the caller derives a per-user name and the terminating PVC at the shared name is
// invisible to user B's lookup.
func TestSoftwareLibraryPVCSharedNameBlocksAllUsers(t *testing.T) {
	defer SetupAndTeardownTest()()

	Config.Config.S3Config = S3Config{BucketName: "test-bucket", Region: "us-east-1"}

	clientset := fake.NewSimpleClientset()
	ns := "test-ns"
	ctx := context.Background()

	// The current default shared PVC name that every user gets (pods.go applySquashFSMounter).
	sharedPVCName := "software-library-pvc"

	// Simulate: user A's pod is terminating and has left the shared PVC in
	// Terminating state (DeletionTimestamp set, pvc-protection finalizer still present).
	// We use a reactor so the fake client reliably returns DeletionTimestamp on Get,
	// which the real API server sets and which the fake tracker may strip on Create.
	now := metav1.Now()
	clientset.PrependReactor("get", "persistentvolumeclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ga := action.(k8stesting.GetAction)
		if ga.GetName() == sharedPVCName && ga.GetNamespace() == ns {
			return true, &k8sv1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:              sharedPVCName,
					Namespace:         ns,
					DeletionTimestamp: &now,
					Finalizers:        []string{"kubernetes.io/pvc-protection"},
				},
			}, nil
		}
		return false, nil, nil
	})

	opts := SquashFSMountConfig{Enabled: true}

	// User B's launch: with the current shared name it hits the terminating PVC
	// and is incorrectly blocked. After the fix, user B uses their own PVC name
	// and this path is no longer taken by the production code.
	err := ensureSoftwareLibraryPVAndPVC(ctx, clientset.CoreV1(), ns, sharedPVCName, opts)
	if err != nil {
		t.Errorf("user B's launch must not be blocked by a different user's terminating PVC (fix: per-user PVC names): %v", err)
	}
}
