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

// TestSoftwareLibraryPVCSharedNameBlocksAllUsers verifies that a terminating PVC
// belonging to one user does not block another user's launch. Before the per-user
// PVC fix this test failed because both users resolved to the same shared PVC name
// ("software-library-pvc"), so user A's terminating PVC blocked user B. After the
// fix each user's PVC name is derived from their username, so user B's lookup hits
// a different name and succeeds.
func TestSoftwareLibraryPVCSharedNameBlocksAllUsers(t *testing.T) {
	defer SetupAndTeardownTest()()

	Config.Config.S3Config = S3Config{BucketName: "test-bucket", Region: "us-east-1"}

	clientset := fake.NewSimpleClientset()
	ns := "test-ns"
	ctx := context.Background()

	userA := "user-a@test.com"
	userB := "user-b@test.com"
	userAPVCName := softwareLibraryPVCName(userA)
	userBPVCName := softwareLibraryPVCName(userB)

	if userAPVCName == userBPVCName {
		t.Fatalf("per-user PVC names must be distinct: both resolved to %q", userAPVCName)
	}

	// Simulate user A's PVC stuck in Terminating (DeletionTimestamp set).
	// We use a reactor so the fake client reliably returns DeletionTimestamp on Get,
	// which the real API server sets and which the fake tracker may strip on Create.
	now := metav1.Now()
	clientset.PrependReactor("get", "persistentvolumeclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ga := action.(k8stesting.GetAction)
		if ga.GetName() == userAPVCName && ga.GetNamespace() == ns {
			return true, &k8sv1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:              userAPVCName,
					Namespace:         ns,
					DeletionTimestamp: &now,
					Finalizers:        []string{"kubernetes.io/pvc-protection"},
				},
			}, nil
		}
		return false, nil, nil
	})

	opts := SquashFSMountConfig{Enabled: true}

	// User A's own launch should be blocked while their PVC is terminating.
	errA := ensureSoftwareLibraryPVAndPVC(ctx, clientset.CoreV1(), ns, userA, opts)
	if errA == nil {
		t.Error("expected ensureSoftwareLibraryPVAndPVC to return an error for a terminating PVC, but got nil")
	}

	// User B's launch must succeed independently — their PVC name is different
	// from user A's and no terminating PVC exists at that name.
	errB := ensureSoftwareLibraryPVAndPVC(ctx, clientset.CoreV1(), ns, userB, opts)
	if errB != nil {
		t.Errorf("user B's launch must not be blocked by user A's terminating PVC: %v", errB)
	}

	// Verify user B's PVC was actually created under their own name.
	pvc, getErr := clientset.CoreV1().PersistentVolumeClaims(ns).Get(ctx, userBPVCName, metav1.GetOptions{})
	if getErr != nil {
		t.Errorf("expected PVC %q to exist for user B after successful ensure: %v", userBPVCName, getErr)
	} else if pvc.Labels[softwareLibraryLabelKey] != escapism(userB) {
		t.Errorf("expected PVC label %s=%q, got %q", softwareLibraryLabelKey, escapism(userB), pvc.Labels[softwareLibraryLabelKey])
	}
}
