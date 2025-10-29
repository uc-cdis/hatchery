package hatchery

import (
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func createTestPodWithDeletion(name, namespace, username, workspaceID string) *v1.Pod {
	pod := createTestPod(name, namespace, username, workspaceID)
	deletionTime := metav1.NewTime(time.Now())
	pod.DeletionTimestamp = &deletionTime
	return pod
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
