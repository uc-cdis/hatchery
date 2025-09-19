package hatchery

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// PodLifecycle tracks the complete lifecycle of a pod for billing
type PodLifecycle struct {
	PodName           string     `json:"pod_name"`
	Namespace         string     `json:"namespace"`
	LaunchTime        time.Time  `json:"launch_time"`
	StopTime          *time.Time `json:"stop_time,omitempty"`
	NodeName          string     `json:"node_name"`
	Source            string     `json:"source"` // "watch", "event", "reconcile"
	CreationTimestamp time.Time  `json:"creation_timestamp"`
}

// PodCost represents the cost breakdown for a pod
type PodCost struct {
	CPUCost    float64 `json:"cpu_cost"`
	MemoryCost float64 `json:"memory_cost"`
	TotalCost  float64 `json:"total_cost"`
}

// PodTracker handles resilient pod lifecycle tracking for billing
type PodTracker struct {
	k8sClient kubernetes.Interface
	namespace string

	// In-memory state with thread safety
	podLifecycles map[string]*PodLifecycle // key: namespace/podname
	mu            sync.RWMutex

	// Resume state
	lastPodRV   string // Last resource version for pod watching
	lastEventRV string // Last resource version for event watching

	// Control channels
	stopCh chan struct{}
	doneCh chan struct{}
}

func GetConfig() (*rest.Config, error) {
	var config *rest.Config
	var err error

	// Attempt to use in-cluster config
	config, err = rest.InClusterConfig()
	if err != nil {
		// Check if KUBECONFIG env var is set, if so use the files from that
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig != "" {
			// Split the KUBECONFIG env var into a list of files with ':' as the delimiter
			kubeconfigPaths := strings.Split(kubeconfig, ":")
			// log.Debug().Msgf("Using KUBECONFIG files: %v", kubeconfigPaths)

			// Set up the loading rules with the list of kubeconfig files
			loadingRules := &clientcmd.ClientConfigLoadingRules{
				Precedence: kubeconfigPaths,
			}

			// Create the client config
			configOverrides := &clientcmd.ConfigOverrides{}
			kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

			// Get the merged config
			config, err = kubeConfig.ClientConfig()
			if err != nil {
				panic(err) // Handle error appropriately for your situation
			}
		} else {
			panic("Could not get Kubernetes config: neither in-cluster config nor KUBECONFIG is available")
		}
	}
	return config, nil
}

// NewPodTracker creates a new pod tracker instance
func NewPodTracker(namespace string) (*PodTracker, error) {
	// Create in-cluster config (or use kubeconfig for local testing)
	config, err := GetConfig()
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)

	if err != nil {
		return nil, fmt.Errorf("failed to create k8s client: %v", err)
	}

	return &PodTracker{
		k8sClient:     clientset,
		namespace:     namespace,
		podLifecycles: make(map[string]*PodLifecycle),
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}, nil
}

// Start begins all tracking goroutines
func (pt *PodTracker) Start(ctx context.Context) {
	log.Printf("Starting pod tracker for namespace: %s", pt.namespace)

	// Start all watchers concurrently
	go pt.startPodWatcher(ctx)

	// Wait for shutdown signal
	<-pt.stopCh
	close(pt.doneCh)
	log.Println("Pod tracker stopped")
}

// Stop gracefully shuts down the tracker
func (pt *PodTracker) Stop() {
	close(pt.stopCh)
	<-pt.doneCh
}

// PRIMARY: Pod Watch API with resume capability
func (pt *PodTracker) startPodWatcher(ctx context.Context) {
	log.Println("Starting primary pod watcher...")

	for {
		select {
		case <-pt.stopCh:
			return
		default:
			if err := pt.watchPods(ctx); err != nil {
				log.Printf("Pod watcher error: %v, retrying in 5s", err)
				time.Sleep(5 * time.Second)
			}
		}
	}
}

func (pt *PodTracker) watchPods(ctx context.Context) error {
	// Resume from last known resource version
	opts := metav1.ListOptions{ResourceVersion: pt.lastPodRV}

	watcher, err := pt.k8sClient.CoreV1().Pods(pt.namespace).Watch(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to start pod watcher: %v", err)
	}
	defer watcher.Stop()

	for {
		select {
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("pod watch channel closed")
			}

			pod := event.Object.(*v1.Pod)
			pt.lastPodRV = pod.ResourceVersion

			switch event.Type {
			case watch.Added:
				pt.handlePodCreated(pod, "watch")
			case watch.Deleted:
				pt.handlePodDeleted(pod, "watch")
			case watch.Modified:
				pt.handlePodModified(pod, "watch")
			}

		case <-pt.stopCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Handle pod creation
func (pt *PodTracker) handlePodCreated(pod *v1.Pod, source string) {
	key := pt.getPodKey(pod)

	pt.mu.Lock()
	defer pt.mu.Unlock()

	// Don't overwrite if we already have it tracked
	if _, exists := pt.podLifecycles[key]; exists {
		log.Printf("⚠️  Pod already tracked: %s", pod.Name)
		return
	}

	launchTime := pod.CreationTimestamp.Time

	pt.podLifecycles[key] = &PodLifecycle{
		PodName:           pod.Name,
		Namespace:         pod.Namespace,
		LaunchTime:        launchTime,
		NodeName:          pod.Spec.NodeName,
		Source:            source,
		CreationTimestamp: pod.CreationTimestamp.Time,
	}

	log.Printf("🚀 Pod launched: %s at %s (source: %s)",
		pod.Name, launchTime.Format(time.RFC3339), source)
}

// calculatePodPrice calculates the cost of running a pod based on its resource requests and runtime
func (pt *PodTracker) calculatePodPrice(pod *v1.Pod, runtime time.Duration) *PodCost {
	if runtime <= 0 {
		return &PodCost{}
	}

	cpuPrice := Config.Config.Pricing.Cpu
	memoryPrice := Config.Config.Pricing.Memory

	// Convert runtime to hours (fractional)
	runtimeHours := runtime.Hours()

	var totalCPUCost, totalMemoryCost float64

	// Iterate through all containers in the pod
	for _, container := range pod.Spec.Containers {
		// Get CPU request (convert from millicores to cores)
		cpuRequest := container.Resources.Requests.Cpu()
		if cpuRequest != nil {
			cpuCores := float64(cpuRequest.MilliValue()) / 1000.0
			cpuCost := cpuCores * cpuPrice * runtimeHours
			totalCPUCost += cpuCost

			log.Printf("Container %s - CPU: %.3f cores, Cost: $%.4f",
				container.Name, cpuCores, cpuCost)
		}

		// Get Memory request (convert to GB)
		memRequest := container.Resources.Requests.Memory()
		if memRequest != nil {
			memoryGB := float64(memRequest.Value()) / (1024 * 1024 * 1024)
			memoryCost := memoryGB * memoryPrice * runtimeHours
			totalMemoryCost += memoryCost

			log.Printf("Container %s - Memory: %.3f GB, Cost: $%.4f",
				container.Name, memoryGB, memoryCost)
		}
	}

	// Also check init containers if they exist
	for _, initContainer := range pod.Spec.InitContainers {
		cpuRequest := initContainer.Resources.Requests.Cpu()
		if cpuRequest != nil {
			cpuCores := float64(cpuRequest.MilliValue()) / 1000.0
			cpuCost := cpuCores * cpuPrice * runtimeHours
			totalCPUCost += cpuCost

			log.Printf("InitContainer %s - CPU: %.3f cores, Cost: $%.4f",
				initContainer.Name, cpuCores, cpuCost)
		}

		memRequest := initContainer.Resources.Requests.Memory()
		if memRequest != nil {
			memoryGB := float64(memRequest.Value()) / (1024 * 1024 * 1024)
			memoryCost := memoryGB * memoryPrice * runtimeHours
			totalMemoryCost += memoryCost

			log.Printf("InitContainer %s - Memory: %.3f GB, Cost: $%.4f",
				initContainer.Name, memoryGB, memoryCost)
		}
	}

	totalCost := totalCPUCost + totalMemoryCost

	log.Printf("💰 Pod %s total cost: CPU=$%.4f, Memory=$%.4f, Total=$%.4f (runtime: %.2f hours)",
		pod.Name, totalCPUCost, totalMemoryCost, totalCost, runtimeHours)

	return &PodCost{
		CPUCost:    totalCPUCost,
		MemoryCost: totalMemoryCost,
		TotalCost:  totalCost,
	}
}

// Handle pod deletion
func (pt *PodTracker) handlePodDeleted(pod *v1.Pod, source string) {
	key := pt.getPodKey(pod)
	now := time.Now()

	pt.mu.Lock()
	defer pt.mu.Unlock()

	// Use DeletionTimestamp if available, fallback to now
	var terminationTime time.Time
	if pod.DeletionTimestamp != nil && !pod.DeletionTimestamp.IsZero() {
		terminationTime = pod.DeletionTimestamp.Time
		log.Printf("Using DeletionTimestamp: %s", terminationTime.Format(time.RFC3339))
	} else {
		terminationTime = time.Now()
		log.Printf("DeletionTimestamp not available, using current time: %s", terminationTime.Format(time.RFC3339))
	}

	lifecycle, exists := pt.podLifecycles[key]
	if !exists {
		// We don't have launch time - try to figure it out!
		log.Printf("⚠️  Pod deleted but no launch time recorded: %s", pod.Name)

		// Use the pod's CreationTimestamp as fallback
		launchTime := pod.CreationTimestamp.Time
		if launchTime.IsZero() {
			// Last resort - estimate based on deletion time
			launchTime = now.Add(-time.Hour) // Assume it ran for 1 hour minimum
			log.Printf("⚠️  Using estimated launch time for %s: %s", pod.Name, launchTime.Format(time.RFC3339))
		}

		pt.podLifecycles[key] = &PodLifecycle{
			PodName:           pod.Name,
			Namespace:         pod.Namespace,
			LaunchTime:        launchTime,
			StopTime:          &now,
			NodeName:          pod.Spec.NodeName,
			Source:            source + "_recovery",
			CreationTimestamp: launchTime,
		}

		log.Printf("🛑 Pod deleted (recovered): %s, runtime: %s (source: %s)",
			pod.Name, now.Sub(launchTime).String(), source+"_recovery")

		// remove it from the memory
		delete(pt.podLifecycles, key)
	} else {
		// Normal case - we have the launch time
		lifecycle.StopTime = &now
		runtime := terminationTime.Sub(lifecycle.LaunchTime)
		cost := pt.calculatePodPrice(pod, runtime)

		// remove it from the memory
		delete(pt.podLifecycles, key)

		log.Printf("🛑 Pod deleted: %s, runtime: %s, cost: $%.4f (source: %s)",
			pod.Name, runtime.String(), cost.TotalCost, source)
	}
}

// Handle pod modifications (phase changes, etc.)
func (pt *PodTracker) handlePodModified(pod *v1.Pod, source string) {
	// For billing, we mainly care about creation/deletion
	// But we can update metadata if needed
	key := pt.getPodKey(pod)

	pt.mu.RLock()
	lifecycle, exists := pt.podLifecycles[key]
	pt.mu.RUnlock()

	if exists && lifecycle.NodeName == "" && pod.Spec.NodeName != "" {
		pt.mu.Lock()
		lifecycle.NodeName = pod.Spec.NodeName
		pt.mu.Unlock()
		log.Printf("📍 Updated node for pod %s: %s", pod.Name, pod.Spec.NodeName)
	}
}

// Get standardized pod key
func (pt *PodTracker) getPodKey(pod *v1.Pod) string {
	return fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
}
