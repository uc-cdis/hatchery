package hatchery

import (
	"context"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

// Global test configuration
var testConfig *FullHatcheryConfig

// Test helper to create a complete config for testing
func setupTestConfig() {
	if testConfig == nil {
		testConfig = &FullHatcheryConfig{
			Config: HatcheryConfig{
				Pricing: Pricing{
					Cpu:    0.10,
					Memory: 0.05,
				},
				DefaultPayModel: PayModel{
					Id:              "default-id",
					Name:            "Default",
					User:            "default-user",
					Status:          "active",
					Local:           true,
					Region:          "us-east-1",
					Ecs:             false,
					Subnet:          0,
					HardLimit:       100,
					SoftLimit:       80,
					TotalUsage:      0.0,
					CurrentPayModel: true,
				},
			},
			Logger: log.New(os.Stdout, "[TEST] ", log.LstdFlags),
		}
	}
	Config = testConfig
}

// Test helper functions
func createTestPod(name, namespace, username, workspaceID string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				"gen3username":     username,
				"bmh_workspace_id": workspaceID,
			},
			CreationTimestamp: metav1.Now(),
		},
		Spec: v1.PodSpec{
			NodeName: "test-node",
			Containers: []v1.Container{
				{
					Name: "test-container",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("500m"),
							v1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
	}
}

func TestPodTracker_GetPodKey(t *testing.T) {
	tracker := &PodTracker{}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
	}

	expected := "test-namespace/test-pod"
	actual := tracker.getPodKey(pod)

	assert.Equal(t, expected, actual)
}

func TestPodTracker_ExtractUserNameFromPod(t *testing.T) {
	tracker := &PodTracker{}

	t.Run("should extract username when annotation exists", func(t *testing.T) {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"gen3username": "testuser@example.com",
				},
			},
		}

		username := tracker.extractUserNameFromPod(pod)
		assert.Equal(t, "testuser@example.com", username)
	})

	t.Run("should return empty string when annotation missing", func(t *testing.T) {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{},
			},
		}

		username := tracker.extractUserNameFromPod(pod)
		assert.Equal(t, "", username)
	})
}

func TestPodTracker_ExtractPaymodelIDFromPod(t *testing.T) {
	tracker := &PodTracker{}

	t.Run("should extract workspace ID when annotation exists", func(t *testing.T) {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					"bmh_workspace_id": "workspace-123",
				},
			},
		}

		workspaceID := tracker.extractPaymodelIDFromPod(pod)
		assert.Equal(t, "workspace-123", workspaceID)
	})

	t.Run("should return empty string when annotation missing", func(t *testing.T) {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{},
			},
		}

		workspaceID := tracker.extractPaymodelIDFromPod(pod)
		assert.Equal(t, "", workspaceID)
	})
}

func TestPodTracker_HandlePodCreated(t *testing.T) {
	setupTestConfig() // Ensure config is set up before tests

	tracker := &PodTracker{
		podLifecycles: make(map[string]*PodLifecycle),
		mu:            sync.RWMutex{},
	}

	pod := createTestPod("test-pod", "default", "user@example.com", "workspace-123")

	t.Run("should track new pod", func(t *testing.T) {
		tracker.handlePodCreated(pod, "test")

		key := tracker.getPodKey(pod)
		lifecycle, exists := tracker.podLifecycles[key]

		assert.True(t, exists, "Pod should be tracked after creation")
		assert.Equal(t, pod.Name, lifecycle.PodName)
		assert.Equal(t, pod.Namespace, lifecycle.Namespace)
		assert.Equal(t, "test", lifecycle.Source)
		assert.False(t, lifecycle.LaunchTime.IsZero(), "Launch time should be set")
		assert.Equal(t, pod.Spec.NodeName, lifecycle.NodeName)
	})

	t.Run("should not overwrite existing pod", func(t *testing.T) {
		// Clear previous state
		tracker.podLifecycles = make(map[string]*PodLifecycle)

		// Add the pod first
		tracker.handlePodCreated(pod, "test")

		// Try to add it again
		tracker.handlePodCreated(pod, "test2")

		key := tracker.getPodKey(pod)
		lifecycle := tracker.podLifecycles[key]

		// Should still have the original source
		assert.Equal(t, "test", lifecycle.Source, "Pod should not be overwritten")
	})
}

func TestPodTracker_CalculatePodPrice(t *testing.T) {
	setupTestConfig()
	tracker := &PodTracker{}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pod",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "container-1",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("1"),   // 1 core
							v1.ResourceMemory: resource.MustParse("2Gi"), // 2 GB
						},
					},
				},
				{
					Name: "container-2",
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("500m"), // 0.5 cores
							v1.ResourceMemory: resource.MustParse("1Gi"),  // 1 GB
						},
					},
				},
			},
		},
	}

	t.Run("should calculate cost correctly for 2 hours", func(t *testing.T) {
		runtime := 2 * time.Hour

		cost := tracker.calculatePodPrice(pod, runtime)

		// Expected calculations:
		// CPU: (1 + 0.5) cores * $0.10/hour * 2 hours = $0.30
		// Memory: (2 + 1) GB * $0.05/GB-hour * 2 hours = $0.30
		// Total: $0.60
		assert.InEpsilon(t, 0.30, cost.CPUCost, 0.001)
		assert.InEpsilon(t, 0.30, cost.MemoryCost, 0.001)
		assert.InEpsilon(t, 0.60, cost.TotalCost, 0.001)
	})

	t.Run("should return zero cost for zero runtime", func(t *testing.T) {
		cost := tracker.calculatePodPrice(pod, 0)

		assert.Equal(t, 0.0, cost.TotalCost)
		assert.Equal(t, 0.0, cost.CPUCost)
		assert.Equal(t, 0.0, cost.MemoryCost)
	})
}

func TestPodTracker_HandlePodModified(t *testing.T) {
	setupTestConfig()

	tracker := &PodTracker{
		podLifecycles: make(map[string]*PodLifecycle),
		mu:            sync.RWMutex{},
	}

	pod := createTestPod("test-pod", "default", "user@example.com", "workspace-123")
	key := tracker.getPodKey(pod)

	t.Run("should update node name when pod gets scheduled", func(t *testing.T) {
		// Add pod without node name
		tracker.podLifecycles[key] = &PodLifecycle{
			PodName:    pod.Name,
			Namespace:  pod.Namespace,
			LaunchTime: time.Now(),
			NodeName:   "", // No node assigned yet
			Source:     "test",
		}

		// Create modified pod with node name
		modifiedPod := pod.DeepCopy()
		modifiedPod.Spec.NodeName = "assigned-node"

		tracker.handlePodModified(modifiedPod, "test")

		// Should update the node name
		lifecycle := tracker.podLifecycles[key]
		assert.Equal(t, "assigned-node", lifecycle.NodeName)
	})

	t.Run("should not modify if pod not tracked", func(t *testing.T) {
		untrackedPod := createTestPod("untracked-pod", "default", "user@example.com", "workspace-123")

		// This should not panic
		tracker.handlePodModified(untrackedPod, "test")
	})
}

func TestPodCost_Structure(t *testing.T) {
	cost := &PodCost{
		CPUCost:    10.5,
		MemoryCost: 5.25,
		TotalCost:  15.75,
	}

	assert.Equal(t, 10.5, cost.CPUCost)
	assert.Equal(t, 5.25, cost.MemoryCost)
	assert.Equal(t, 15.75, cost.TotalCost)
}

func TestPodLifecycle_Structure(t *testing.T) {
	now := time.Now()
	stopTime := now.Add(time.Hour)

	lifecycle := &PodLifecycle{
		PodName:           "test-pod",
		Namespace:         "default",
		LaunchTime:        now,
		StopTime:          &stopTime,
		NodeName:          "test-node",
		Source:            "test",
		CreationTimestamp: now,
	}

	assert.Equal(t, "test-pod", lifecycle.PodName)
	assert.Equal(t, "default", lifecycle.Namespace)
	assert.Equal(t, now, lifecycle.LaunchTime)
	assert.Equal(t, &stopTime, lifecycle.StopTime)
	assert.Equal(t, "test-node", lifecycle.NodeName)
	assert.Equal(t, "test", lifecycle.Source)
	assert.Equal(t, now, lifecycle.CreationTimestamp)
}

// Test main function to set up global state
func TestMain(m *testing.M) {
	// Set up test configuration before running tests
	setupTestConfig()

	// Run tests
	code := m.Run()

	// Exit with the appropriate code
	os.Exit(code)
}

// finishedPod returns a copy of pod in the given terminal phase, with its
// workspace container exiting at finishedAt and a sidecar 2s later.
func finishedPod(pod *v1.Pod, phase v1.PodPhase, finishedAt time.Time) *v1.Pod {
	p := pod.DeepCopy()
	p.Status.Phase = phase
	p.Status.ContainerStatuses = []v1.ContainerStatus{{
		Name:  "test-container",
		State: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{FinishedAt: metav1.NewTime(finishedAt)}},
	}}
	p.Status.InitContainerStatuses = []v1.ContainerStatus{{
		Name:  "fuse-container",
		State: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{FinishedAt: metav1.NewTime(finishedAt.Add(2 * time.Second))}},
	}}
	return p
}

type recordedCharge struct {
	eventKey string
	user     string
	cost     float64
}

// newRecordingTracker returns a tracker whose charges are recorded instead of
// written to DynamoDB.
func newRecordingTracker(client *fake.Clientset) (*PodTracker, func() []recordedCharge) {
	pt := newPodTracker(client, "jupyter-pods")
	var mu sync.Mutex
	var charges []recordedCharge
	pt.chargeFn = func(eventKey, userName, _ string, cost float64) error {
		mu.Lock()
		defer mu.Unlock()
		charges = append(charges, recordedCharge{eventKey, userName, cost})
		return nil
	}
	return pt, func() []recordedCharge {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedCharge{}, charges...)
	}
}

func TestPodTracker_ChargesOnceWhenPodFinishes(t *testing.T) {
	setupTestConfig()
	pt, charges := newRecordingTracker(fake.NewClientset())

	launched := time.Now().Add(-2 * time.Hour)
	pod := createTestPod("hatchery-user", "jupyter-pods", "user@example.com", "workspace-123")
	pod.UID = "uid-1"
	pod.CreationTimestamp = metav1.NewTime(launched)
	pt.handlePodCreated(pod, "test")

	// Idle timeout: the pod finishes after 1h, then sits around before deletion.
	done := finishedPod(pod, v1.PodSucceeded, launched.Add(time.Hour))
	pt.handlePodModified(done, "test")
	pt.handlePodModified(done, "test") // repeated updates of a finished pod
	deleted := done.DeepCopy()
	deleted.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	pt.handlePodDeleted(deleted, "test")

	got := charges()
	if assert.Len(t, got, 1, "a finished pod is charged exactly once") {
		assert.Equal(t, "uid-1/charged", got[0].eventKey)
		assert.Equal(t, "user@example.com", got[0].user)
		// 0.5 CPU * $0.10 + 1 GiB * $0.05 = $0.10/h, for 1h + 2s (sidecar exit).
		assert.InDelta(t, 0.10, got[0].cost, 0.001, "billed until the containers exited, not until deletion")
	}
	assert.Empty(t, pt.podLifecycles, "deleted pod is no longer tracked")
}

func TestPodTracker_DeletedRunningPodChargedUntilDeletion(t *testing.T) {
	setupTestConfig()
	pt, charges := newRecordingTracker(fake.NewClientset())

	launched := time.Now().Add(-3 * time.Hour)
	pod := createTestPod("hatchery-user", "jupyter-pods", "user@example.com", "workspace-123")
	pod.UID = "uid-2"
	pod.CreationTimestamp = metav1.NewTime(launched)
	pod.Status.Phase = v1.PodRunning
	pt.handlePodCreated(pod, "test")

	deleted := pod.DeepCopy()
	deleted.DeletionTimestamp = &metav1.Time{Time: launched.Add(2 * time.Hour)}
	pt.handlePodDeleted(deleted, "test")

	got := charges()
	if assert.Len(t, got, 1) {
		assert.InDelta(t, 0.20, got[0].cost, 0.001, "terminated pod billed until its deletion time")
	}
}

func TestPodTracker_TombstoneDeleteIsCharged(t *testing.T) {
	setupTestConfig()
	pt, charges := newRecordingTracker(fake.NewClientset())

	launched := time.Now().Add(-2 * time.Hour)
	pod := createTestPod("hatchery-user", "jupyter-pods", "user@example.com", "workspace-123")
	pod.UID = "uid-3"
	pod.CreationTimestamp = metav1.NewTime(launched)
	pt.onPodAdd(pod)

	// The pod finished and was deleted while the watch was down: the informer
	// only delivers a tombstone with the last state it saw.
	pt.onPodDelete(cache.DeletedFinalStateUnknown{
		Key: "jupyter-pods/hatchery-user",
		Obj: finishedPod(pod, v1.PodSucceeded, launched.Add(time.Hour)),
	})

	got := charges()
	if assert.Len(t, got, 1, "a delete missed during a watch gap is still charged") {
		assert.InDelta(t, 0.10, got[0].cost, 0.001)
	}
}

func TestPodTracker_InformerTracksAndChargesPods(t *testing.T) {
	setupTestConfig()
	client := fake.NewClientset()
	pt, charges := newRecordingTracker(client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pt.startPodWatcher(ctx)

	launched := time.Now().Add(-2 * time.Hour)
	pod := createTestPod("hatchery-user", "jupyter-pods", "user@example.com", "workspace-123")
	pod.UID = "uid-4"
	pod.CreationTimestamp = metav1.NewTime(launched)
	pods := client.CoreV1().Pods("jupyter-pods")
	_, err := pods.Create(ctx, pod, metav1.CreateOptions{})
	assert.NoError(t, err)

	assert.Eventually(t, func() bool {
		pt.mu.RLock()
		defer pt.mu.RUnlock()
		_, tracked := pt.podLifecycles["jupyter-pods/hatchery-user"]
		return tracked
	}, 10*time.Second, 50*time.Millisecond, "created pod is tracked")

	_, err = pods.UpdateStatus(ctx, finishedPod(pod, v1.PodSucceeded, launched.Add(time.Hour)), metav1.UpdateOptions{})
	assert.NoError(t, err)
	assert.Eventually(t, func() bool { return len(charges()) == 1 }, 10*time.Second, 50*time.Millisecond, "finished pod is charged")

	assert.NoError(t, pods.Delete(ctx, pod.Name, metav1.DeleteOptions{}))
	assert.Eventually(t, func() bool {
		pt.mu.RLock()
		defer pt.mu.RUnlock()
		return len(pt.podLifecycles) == 0
	}, 10*time.Second, 50*time.Millisecond, "deleted pod is untracked")
	assert.Len(t, charges(), 1, "deleting the finished pod does not charge it again")
}

func TestPodTracker_InformerRecoversFromExpiredWatch(t *testing.T) {
	setupTestConfig()
	client := fake.NewClientset()
	pt, _ := newRecordingTracker(client)

	var lists atomic.Int32
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		lists.Add(1)
		return false, nil, nil
	})

	// The first watch fails with 410 Gone, as when the resource version expired.
	var expired atomic.Bool
	client.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
		if expired.Swap(true) {
			return false, nil, nil
		}
		w := watch.NewFake()
		go w.Error(&metav1.Status{Status: metav1.StatusFailure, Code: http.StatusGone, Reason: metav1.StatusReasonExpired, Message: "too old resource version"})
		return true, w, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pt.startPodWatcher(ctx)

	assert.Eventually(t, expired.Load, 10*time.Second, 50*time.Millisecond, "expired watch was served")

	pod := createTestPod("hatchery-user", "jupyter-pods", "user@example.com", "workspace-123")
	pod.UID = "uid-5"
	_, err := client.CoreV1().Pods("jupyter-pods").Create(ctx, pod, metav1.CreateOptions{})
	assert.NoError(t, err)

	assert.Eventually(t, func() bool {
		pt.mu.RLock()
		defer pt.mu.RUnlock()
		_, tracked := pt.podLifecycles["jupyter-pods/hatchery-user"]
		return tracked
	}, 15*time.Second, 50*time.Millisecond, "tracking resumes after the 410")
	assert.GreaterOrEqual(t, lists.Load(), int32(2), "the informer re-listed pods after the 410")
}
