package hatchery

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"

	"github.com/google/uuid"

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
				Config.Logger.Printf("Pod watcher error: %v, retrying in 5s", err)
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
		Config.Logger.Printf("⚠️  Pod already tracked: %s", pod.Name)
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

	Config.Logger.Printf("🚀 Pod launched: %s at %s (source: %s)",
		pod.Name, launchTime.Format(time.RFC3339), source)

	// Check if user has a pay model and create one if needed
	pt.ensureUserInPayModelTable(pod)
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

			Config.Logger.Printf("Container %s - CPU: %.3f cores, Cost: $%.4f",
				container.Name, cpuCores, cpuCost)
		}

		// Get Memory request (convert to GB)
		memRequest := container.Resources.Requests.Memory()
		if memRequest != nil {
			memoryGB := float64(memRequest.Value()) / (1024 * 1024 * 1024)
			memoryCost := memoryGB * memoryPrice * runtimeHours
			totalMemoryCost += memoryCost

			Config.Logger.Printf("Container %s - Memory: %.3f GB, Cost: $%.4f",
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

			Config.Logger.Printf("InitContainer %s - CPU: %.3f cores, Cost: $%.4f",
				initContainer.Name, cpuCores, cpuCost)
		}

		memRequest := initContainer.Resources.Requests.Memory()
		if memRequest != nil {
			memoryGB := float64(memRequest.Value()) / (1024 * 1024 * 1024)
			memoryCost := memoryGB * memoryPrice * runtimeHours
			totalMemoryCost += memoryCost

			Config.Logger.Printf("InitContainer %s - Memory: %.3f GB, Cost: $%.4f",
				initContainer.Name, memoryGB, memoryCost)
		}
	}

	totalCost := totalCPUCost + totalMemoryCost

	Config.Logger.Printf("💰 Pod %s total cost: CPU=$%.4f, Memory=$%.4f, Total=$%.4f (runtime: %.2f hours)",
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
	var cost *PodCost

	pt.mu.Lock()
	defer pt.mu.Unlock()

	// Extract user information from pod labels or annotations
	userName := pt.extractUserNameFromPod(pod)
	podPaymodelID := pt.extractPaymodelIDFromPod(pod)

	// Use DeletionTimestamp if available, fallback to now
	var terminationTime time.Time
	if pod.DeletionTimestamp != nil && !pod.DeletionTimestamp.IsZero() {
		terminationTime = pod.DeletionTimestamp.Time
		Config.Logger.Printf("Using DeletionTimestamp: %s", terminationTime.Format(time.RFC3339))
	} else {
		terminationTime = time.Now()
		Config.Logger.Printf("DeletionTimestamp not available, using current time: %s", terminationTime.Format(time.RFC3339))
	}

	lifecycle, exists := pt.podLifecycles[key]
	if !exists {
		// We don't have launch time - try to figure it out!
		Config.Logger.Printf("⚠️  Pod deleted but no launch time recorded: %s", pod.Name)

		// Use the pod's CreationTimestamp as fallback
		launchTime := pod.CreationTimestamp.Time
		if launchTime.IsZero() {
			// Last resort - estimate based on deletion time
			launchTime = now.Add(-time.Hour) // Assume it ran for 1 hour minimum
			Config.Logger.Printf("⚠️  Using estimated launch time for %s: %s", pod.Name, launchTime.Format(time.RFC3339))
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

		runtime := terminationTime.Sub(launchTime)
		cost = pt.calculatePodPrice(pod, runtime)

		Config.Logger.Printf("🛑 Pod deleted (recovered): %s, runtime: %s, cost: $%.4f (source: %s)",
			pod.Name, runtime.String(), cost.TotalCost, source+"_recovery")

		// remove it from the memory
		delete(pt.podLifecycles, key)
	} else {
		// Normal case - we have the launch time
		lifecycle.StopTime = &now
		runtime := terminationTime.Sub(lifecycle.LaunchTime)
		cost = pt.calculatePodPrice(pod, runtime)

		Config.Logger.Printf("🛑 Pod deleted: %s, runtime: %s, cost: $%.4f (source: %s)",
			pod.Name, runtime.String(), cost.TotalCost, source)

		// remove it from the memory
		delete(pt.podLifecycles, key)
	}
	Config.Logger.Printf("🧑‍💻 User and workpaceid info %v, %s", userName, podPaymodelID)
	// Update pay model cost if we have user info
	if userName != "" && podPaymodelID != "" {
		if err := UpdatePayModelCost(userName, podPaymodelID, cost.TotalCost); err != nil {
			Config.Logger.Printf("⚠️  Failed to update cost for user %s: %v", userName, err)
		}
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
		Config.Logger.Printf("📍 Updated node for pod %s: %s", pod.Name, pod.Spec.NodeName)
	}
}

// Get standardized pod key
func (pt *PodTracker) getPodKey(pod *v1.Pod) string {
	return fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
}

// Ensure user has a pay model when their workspace starts
func (pt *PodTracker) ensureUserInPayModelTable(pod *v1.Pod) {
	userName := pt.extractUserNameFromPod(pod)
	podPaymodelID := pt.extractPaymodelIDFromPod(pod)
	if userName == "" {
		Config.Logger.Printf("⚠️  Could not extract username from pod %s, skipping pay model check", pod.Name)
		return
	}
	if podPaymodelID == "" {
		Config.Logger.Printf("Could not extract paymodel_type from pod %s, skipping pay model check", pod.Name)
		return
	}

	Config.Logger.Printf("🔍 Checking pay models for user: %s", userName)
	payModels, err := payModelsFromDatabase(userName, false)
	if err != nil {
		Config.Logger.Printf("⚠️  Error checking pay models for user %s: %v", userName, err)
		return
	}

	if payModels == nil || len(*payModels) == 0 {
		Config.Logger.Printf("📝 No pay models found for user %s, creating default Trial Paymodel", userName)

		defaultPayModel, err := pt.createDefaultTrialPayPayModel(userName, podPaymodelID)

		if err != nil {
			Config.Logger.Printf("❌ Failed to create default pay model for user %s: %v", userName, err)
		} else {
			Config.Logger.Printf("✅ Created default Trial Paymodel for user %s with workspace ID: %s",
				userName, defaultPayModel.Id)
		}
	} else {
		var currentPayModelFromDb *PayModel

		for i := range *payModels {
			if (*payModels)[i].Id == podPaymodelID {
				currentPayModelFromDb = &(*payModels)[i]
				break
			}
		}

		if currentPayModelFromDb == nil {
			Config.Logger.Printf("Could not match pod workspace ID to database for user %v. Creating new record in database", userName)
			defaultPayModel, err := pt.createDefaultTrialPayPayModel(userName, podPaymodelID)
			if err != nil {
				Config.Logger.Printf("❌ Failed to create default pay model for user %s: %v", userName, err)
			} else {
				Config.Logger.Printf("✅ Created default Trial Paymodel for user %s with workspace ID: %s",
					userName, defaultPayModel.Id)
			}
		}
		Config.Logger.Printf("Workspace ID from pod matched with data base for user %v", userName)
		return
	}
}

// createDefaultTrialPayPayModel creates a default Trial Pay pay model for a new user
func (pt *PodTracker) createDefaultTrialPayPayModel(userName string, podPaymodelID ...string) (*PayModel, error) {

	if Config.Config.PayModelsDynamodbTable == "" {
		return nil, fmt.Errorf("no pay models DynamoDB table configured")
	}
	payModelID := uuid.New().String()
	if len(podPaymodelID) > 0 && podPaymodelID[0] != "" {
		payModelID = podPaymodelID[0]
	}

	sess, err := session.NewSession(&aws.Config{
		Region:                        aws.String("us-east-1"),
		CredentialsChainVerboseErrors: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session: %v", err)
	}

	dynamodbSvc := dynamodb.New(sess)

	defaultPayModel := Config.Config.DefaultPayModel
	defaultPayModel.Id = payModelID
	defaultPayModel.User = userName
	defaultPayModel.CurrentPayModel = true
	defaultPayModel.Status = "active"
	defaultPayModel.Local = true
	defaultPayModel.Ecs = false
	defaultPayModel.TotalUsage = 0.0

	item, err := dynamodbattribute.MarshalMap(defaultPayModel)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pay model: %v", err)
	}

	Config.Logger.Printf("Putting item into table")

	// Put the item in DynamoDB
	_, err = dynamodbSvc.PutItem(&dynamodb.PutItemInput{
		TableName: aws.String(Config.Config.PayModelsDynamodbTable),
		Item:      item,
	})
	if err != nil {
		if strings.Contains(err.Error(), "AccessDeniedException") ||
			strings.Contains(err.Error(), "is not authorized") {
			Config.Logger.Printf("WARNING: No DynamoDB write permissions. Creating in-memory pay model for user %s", userName)
			Config.Logger.Printf("DynamoDB error: %v", err)
			// Return the pay model anyway so the user can proceed
			return &defaultPayModel, nil
		}
		return nil, fmt.Errorf("failed to put pay model in DynamoDB: %v", err)
	}

	Config.Logger.Printf("Created default Direct Pay pay model for user %s", userName)
	return &defaultPayModel, nil
}

// UpdatePayModelCost adds cost to a user's pay model in DynamoDB
func UpdatePayModelCost(userName string, podPaymodelID string, additionalCost float64) error {
	if Config.Config.PayModelsDynamodbTable == "" {
		return fmt.Errorf("no pay models DynamoDB table configured")
	}

	sess, err := session.NewSession(&aws.Config{
		Region:                        aws.String("us-east-1"),
		CredentialsChainVerboseErrors: aws.Bool(true),
	})
	if err != nil {
		return fmt.Errorf("failed to create AWS session: %v", err)
	}

	dynamodbSvc := dynamodb.New(sess)
	// get the current cost
	input := &dynamodb.UpdateItemInput{
		TableName: aws.String(Config.Config.PayModelsDynamodbTable),
		Key: map[string]*dynamodb.AttributeValue{
			"user_id": {
				S: aws.String(userName),
			},
			"bmh_workspace_id": {
				S: aws.String(podPaymodelID),
			},
		},
		ExpressionAttributeNames: map[string]*string{
			"#C": aws.String("total-usage"),
		},
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":inc": {
				N: aws.String(fmt.Sprintf("%.4f", additionalCost)),
			},
			":zero": {
				N: aws.String("0"),
			},
		},
		UpdateExpression: aws.String("SET #C = if_not_exists(#C, :zero) + :inc"),
		ReturnValues:     aws.String("UPDATED_NEW"),
	}

	Config.Logger.Printf("Attempting to update item in table")
	result, err := dynamodbSvc.UpdateItem(input)
	if err != nil {
		if strings.Contains(err.Error(), "AccessDeniedException") ||
			strings.Contains(err.Error(), "is not authorized") {
			Config.Logger.Printf("WARNING: No DynamoDB update permissions. Cost tracking disabled for user %s", userName)
			Config.Logger.Printf("📝 Updated cost tracking: %v", input)
			return nil
		}
		return fmt.Errorf("failed to update pay model cost: %v", err)
	}

	if result.Attributes["total-usage"] != nil {
		Config.Logger.Printf("Updated cost for user %s, workspace %s: $%s (added: $%.4f)",
			userName, podPaymodelID, *result.Attributes["total-usage"].N, additionalCost)
	}

	return nil
}

// extractUserNameFromPod extracts the username from pod labels or annotations
func (pt *PodTracker) extractUserNameFromPod(pod *v1.Pod) string {
	if username, exists := pod.ObjectMeta.Annotations["gen3username"]; exists {
		return username
	} else {
		fmt.Println("gen3username key not found. Could not extract username from Pod")
		return ""
	}
}

// extractPaymodelIDFromPod extracts the username from pod labels or annotations
func (pt *PodTracker) extractPaymodelIDFromPod(pod *v1.Pod) string {
	if podPaymodelID, exists := pod.ObjectMeta.Annotations["bmh_workspace_id"]; exists {
		return podPaymodelID
	} else {
		fmt.Println("paymodel_type key not found. Could not extract username from Pod")
		return ""
	}
}
