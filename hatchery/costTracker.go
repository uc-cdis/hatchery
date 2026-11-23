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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
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
	charged       map[types.UID]bool       // pods this replica has already billed
	mu            sync.RWMutex

	// chargeFn records a pod's cost; defaults to updatePayModelCost (overridden in tests)
	chargeFn func(eventKey, userName, podPaymodelID string, cost float64) error

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

	return newPodTracker(clientset, namespace), nil
}

func newPodTracker(client kubernetes.Interface, namespace string) *PodTracker {
	pt := &PodTracker{
		k8sClient:     client,
		namespace:     namespace,
		podLifecycles: make(map[string]*PodLifecycle),
		charged:       make(map[types.UID]bool),
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
	pt.chargeFn = pt.updatePayModelCost
	return pt
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

// startPodWatcher runs a pod informer for the namespace until the tracker is
// stopped. The informer lists and then watches pods, and re-lists on its own
// when the watch expires (410 Gone), so no events are lost for long.
func (pt *PodTracker) startPodWatcher(ctx context.Context) {
	log.Println("Starting pod informer...")

	stop := make(chan struct{})
	go func() {
		select {
		case <-pt.stopCh:
		case <-ctx.Done():
		}
		close(stop)
	}()

	factory := informers.NewSharedInformerFactoryWithOptions(pt.k8sClient, 0, informers.WithNamespace(pt.namespace))
	podInformer := factory.Core().V1().Pods().Informer()
	if _, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    pt.onPodAdd,
		UpdateFunc: pt.onPodUpdate,
		DeleteFunc: pt.onPodDelete,
	}); err != nil {
		Config.Logger.Printf("Failed to register pod informer handlers: %v", err)
		return
	}

	factory.Start(stop)
	if !cache.WaitForCacheSync(stop, podInformer.HasSynced) {
		Config.Logger.Printf("Pod informer stopped before its cache synced")
		return
	}
	log.Println("Pod informer synced")
	<-stop
	factory.Shutdown()
}

func (pt *PodTracker) onPodAdd(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		return
	}
	pt.handlePodCreated(pod, "watch")
	// A pod that already finished (e.g. hatchery restarted) is billed now.
	if isPodFinished(pod) {
		pt.handlePodFinished(pod, "watch")
	}
}

func (pt *PodTracker) onPodUpdate(_, newObj interface{}) {
	if pod, ok := newObj.(*v1.Pod); ok {
		pt.handlePodModified(pod, "watch")
	}
}

func (pt *PodTracker) onPodDelete(obj interface{}) {
	source := "watch"
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		// The delete happened while the watch was down: we get the last known state.
		obj = tombstone.Obj
		source = "tombstone"
	}
	if pod, ok := obj.(*v1.Pod); ok {
		pt.handlePodDeleted(pod, source)
	}
}

// isPodFinished reports whether the pod reached a terminal phase. With native
// sidecars this happens when the workspace container exits (e.g. idle timeout).
func isPodFinished(pod *v1.Pod) bool {
	return pod.Status.Phase == v1.PodSucceeded || pod.Status.Phase == v1.PodFailed
}

// podFinishedAt returns when the last container of a finished pod exited, or
// the zero time if no container reports a finish time.
func podFinishedAt(pod *v1.Pod) time.Time {
	var end time.Time
	statuses := append(append([]v1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
	for _, status := range statuses {
		if terminated := status.State.Terminated; terminated != nil && terminated.FinishedAt.After(end) {
			end = terminated.FinishedAt.Time
		}
	}
	return end
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
	if isPodFinished(pod) {
		// Billed up to when it finished, not when it was deleted. No-op if the
		// finish was already billed.
		pt.handlePodFinished(pod, source)
	} else {
		end := time.Now()
		if pod.DeletionTimestamp != nil && !pod.DeletionTimestamp.IsZero() {
			end = pod.DeletionTimestamp.Time
		}
		pt.chargePod(pod, end, "🛑 Pod deleted", source)
	}

	pt.mu.Lock()
	delete(pt.podLifecycles, pt.getPodKey(pod))
	delete(pt.charged, pod.UID)
	pt.mu.Unlock()
}

// handlePodFinished bills a pod that reached Succeeded/Failed, up to the time
// its last container exited. A finished pod can stay around until its node is
// removed or the user relaunches, and that time must not be billed.
func (pt *PodTracker) handlePodFinished(pod *v1.Pod, source string) {
	end := podFinishedAt(pod)
	if end.IsZero() {
		end = time.Now()
	}
	pt.chargePod(pod, end, "🏁 Pod finished", source)
}

// chargePod bills a pod once, for the time from its launch until end. The
// DynamoDB event key is derived from the pod UID only, so the charge is
// idempotent across replicas, hatchery restarts and finish-then-delete.
func (pt *PodTracker) chargePod(pod *v1.Pod, end time.Time, what string, source string) {
	key := pt.getPodKey(pod)

	pt.mu.Lock()
	if pt.charged == nil {
		pt.charged = make(map[types.UID]bool)
	}
	if pt.charged[pod.UID] {
		pt.mu.Unlock()
		return
	}
	pt.charged[pod.UID] = true
	launchTime := pod.CreationTimestamp.Time
	if lifecycle, exists := pt.podLifecycles[key]; exists {
		launchTime = lifecycle.LaunchTime
		lifecycle.StopTime = &end
	} else {
		Config.Logger.Printf("⚠️  No launch time recorded for %s, using its creation time", pod.Name)
	}
	pt.mu.Unlock()

	if launchTime.IsZero() {
		Config.Logger.Printf("⚠️  Pod %s has no creation time, not billing it", pod.Name)
		return
	}

	runtime := end.Sub(launchTime)
	cost := pt.calculatePodPrice(pod, runtime)
	Config.Logger.Printf("%s: %s, runtime: %s, cost: $%.4f (source: %s)",
		what, pod.Name, runtime.String(), cost.TotalCost, source)

	userName := pt.extractUserNameFromPod(pod)
	podPaymodelID := pt.extractPaymodelIDFromPod(pod)
	Config.Logger.Printf("🧑‍💻 User and workpaceid info %v, %s", userName, podPaymodelID)
	if userName == "" || podPaymodelID == "" {
		return
	}
	eventKey := fmt.Sprintf("%s/charged", pod.UID)
	charge := pt.chargeFn
	if charge == nil {
		charge = pt.updatePayModelCost
	}
	if err := charge(eventKey, userName, podPaymodelID, cost.TotalCost); err != nil {
		Config.Logger.Printf("⚠️  Failed to update cost for user %s: %v", userName, err)
	}
}

// Handle pod modifications (phase changes, etc.)
func (pt *PodTracker) handlePodModified(pod *v1.Pod, source string) {
	key := pt.getPodKey(pod)

	pt.mu.Lock()
	if lifecycle, exists := pt.podLifecycles[key]; exists && lifecycle.NodeName == "" && pod.Spec.NodeName != "" {
		lifecycle.NodeName = pod.Spec.NodeName
		Config.Logger.Printf("📍 Updated node for pod %s: %s", pod.Name, pod.Spec.NodeName)
	}
	pt.mu.Unlock()

	if isPodFinished(pod) {
		pt.handlePodFinished(pod, source)
	}
}

func (pt *PodTracker) cleanupProcessedEvents(userName, podPaymodelID string) error {
	sess, err := session.NewSession(&aws.Config{
		Region:                        aws.String("us-east-1"),
		CredentialsChainVerboseErrors: aws.Bool(true),
	})
	if err != nil {
		return fmt.Errorf("failed to create AWS session: %v", err)
	}
	payModelTableConfig := getPayModelTableCreds(sess)

	dynamodbSvc := dynamodb.New(sess, &payModelTableConfig)

	_, err = dynamodbSvc.UpdateItem(&dynamodb.UpdateItemInput{
		TableName: aws.String(Config.Config.PayModelsDynamodbTable),
		Key: map[string]*dynamodb.AttributeValue{
			"user_id":          {S: aws.String(userName)},
			"bmh_workspace_id": {S: aws.String(podPaymodelID)},
		},
		UpdateExpression: aws.String("REMOVE #PE"),
		ExpressionAttributeNames: map[string]*string{
			"#PE": aws.String("processed_events"),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to cleanup processed_events: %v", err)
	}
	return nil
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

	payModelTableConfig := getPayModelTableCreds(sess)

	dynamodbSvc := dynamodb.New(sess, &payModelTableConfig)

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

// updatePayModelCost adds cost to a user's pay model in DynamoDB
func (pt *PodTracker) updatePayModelCost(eventKey string, userName string, podPaymodelID string, additionalCost float64) error {
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

	payModelTableConfig := getPayModelTableCreds(sess)

	dynamodbSvc := dynamodb.New(sess, &payModelTableConfig)
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
		UpdateExpression:    aws.String("SET #C = if_not_exists(#C, :zero) + :inc ADD #PE :evt"),
		ConditionExpression: aws.String("NOT contains(#PE, :evtVal)"),
		ExpressionAttributeNames: map[string]*string{
			"#C":  aws.String("total-usage"),
			"#PE": aws.String("processed_events"),
		},
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":inc":    {N: aws.String(fmt.Sprintf("%.4f", additionalCost))},
			":zero":   {N: aws.String("0")},
			":evt":    {SS: []*string{aws.String(eventKey)}},
			":evtVal": {S: aws.String(eventKey)},
		},
		ReturnValues: aws.String("UPDATED_NEW"),
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
		if strings.Contains(err.Error(), "ConditionalCheckFailedException") {
			Config.Logger.Printf("Cost tracking update failed on idempotent condition check, DynamoDB has already been updated by another replica")
			return nil
		}
		return fmt.Errorf("failed to update pay model cost: %v", err)
	}

	if result.Attributes["total-usage"] != nil {
		Config.Logger.Printf("Updated cost for user %s, workspace %s: $%s (added: $%.4f)",
			userName, podPaymodelID, *result.Attributes["total-usage"].N, additionalCost)
	}

	if peAttr, ok := result.Attributes["processed_events"]; ok {
		if len(peAttr.SS) > 1000 { // threshold of number of PE attributes
			err = pt.cleanupProcessedEvents(userName, podPaymodelID)
			if err != nil {
				Config.Logger.Printf("Cost tracking PE attributes cleanup failed")
			}
		}
	}

	return nil
}

// extractUserNameFromPod extracts the username from pod labels or annotations
func (pt *PodTracker) extractUserNameFromPod(pod *v1.Pod) string {
	if username, exists := pod.Annotations["gen3username"]; exists {
		return username
	} else {
		fmt.Println("gen3username key not found. Could not extract username from Pod")
		return ""
	}
}

// extractPaymodelIDFromPod extracts the username from pod labels or annotations
func (pt *PodTracker) extractPaymodelIDFromPod(pod *v1.Pod) string {
	if podPaymodelID, exists := pod.Annotations["bmh_workspace_id"]; exists {
		return podPaymodelID
	} else {
		fmt.Println("paymodel_type key not found. Could not extract username from Pod")
		return ""
	}
}
