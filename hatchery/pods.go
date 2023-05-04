package hatchery

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	// AWS modules
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/eks"

	"sigs.k8s.io/aws-iam-authenticator/pkg/token"

	awstrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/aws/aws-sdk-go/aws"
	kubernetestrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/k8s.io/client-go/kubernetes"
)

var (
	trueVal  = true
	falseVal = false
)

const ambassadorYaml = `---
apiVersion: ambassador/v1
kind:  Mapping
name:  %s
prefix: /
headers:
  remote_user: %s
service: %s.%s.svc.cluster.local:80
bypass_auth: true
timeout_ms: 300000
use_websocket: true
rewrite: %s
tls: %s
`

type PodConditions struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type ContainerStates struct {
	Name  string               `json:"name"`
	State k8sv1.ContainerState `json:"state"`
	Ready bool                 `json:"ready"`
}

type WorkspaceStatus struct {
	Status           string            `json:"status"`
	Conditions       []PodConditions   `json:"conditions"`
	ContainerStates  []ContainerStates `json:"containerStates"`
	IdleTimeLimit    int               `json:"idleTimeLimit"`
	LastActivityTime int64             `json:"lastActivityTime"`
}

func getPodClient(ctx context.Context, userName string, payModelPtr *PayModel) (corev1.CoreV1Interface, bool, error) {
	if payModelPtr != nil && !(*payModelPtr).Local {
		podClient, err := NewEKSClientset(ctx, userName, *payModelPtr)
		if err != nil {
			Config.Logger.Error("Error fetching EKS kubeconfig",
				"error", err,
				"userName", userName,
				"payModel", *payModelPtr,
			)
			return nil, true, err
		} else {
			return podClient, true, nil
		}
	} else {
		return getLocalPodClient(), false, nil
	}
}

func getLocalPodClient() corev1.CoreV1Interface {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	config.WrapTransport = kubernetestrace.WrapRoundTripper
	if err != nil {

		Config.Logger.Error("Error creating in-cluster kubeconfig",
			"error", err,
		)
		return nil
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {

		Config.Logger.Error("Error creating in-cluster clientset",
			"error", err,
		)
		return nil
	}
	// Access jobs. We can't do it all in one line, since we need to receive the
	// errors and manage them appropriately
	podClient := clientset.CoreV1()
	return podClient
}

// Generate EKS kubeconfig using AWS role
func NewEKSClientset(ctx context.Context, userName string, payModel PayModel) (corev1.CoreV1Interface, error) {
	roleARN := "arn:aws:iam::" + payModel.AWSAccountId + ":role/csoc_adminvm"
	sess := awstrace.WrapSession(session.Must(session.NewSession(&aws.Config{
		Region: aws.String(payModel.Region),
	})))

	creds := stscreds.NewCredentials(sess, roleARN)
	eksSvc := eks.New(sess, &aws.Config{Credentials: creds})
	input := &eks.DescribeClusterInput{
		Name: aws.String(payModel.Name),
	}
	result, err := eksSvc.DescribeClusterWithContext(ctx, input)
	if err != nil {

		Config.Logger.Error("Error calling DescribeCluster",
			"error", err,
			"userName", userName,
			"payModel", payModel,
		)
		return nil, err
	}
	cluster := result.Cluster
	gen, err := token.NewGenerator(true, false)

	if err != nil {
		return nil, err
	}
	opts := &token.GetTokenOptions{
		ClusterID:     aws.StringValue(result.Cluster.Name),
		AssumeRoleARN: roleARN,
	}
	tok, err := gen.GetWithOptions(opts)
	if err != nil {
		return nil, err
	}
	ca, err := base64.StdEncoding.DecodeString(aws.StringValue(cluster.CertificateAuthority.Data))
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(
		&rest.Config{
			Host:        aws.StringValue(cluster.Endpoint),
			BearerToken: tok.Token,
			TLSClientConfig: rest.TLSClientConfig{
				CAData: ca,
			},
		},
	)
	if err != nil {
		return nil, err
	}
	return clientset.CoreV1(), nil
}

func checkPodReadiness(pod *k8sv1.Pod) bool {
	if pod.Status.Phase == "Pending" {
		return false
	}
	for _, v := range pod.Status.Conditions {
		if (v.Type == "Ready" || v.Type == "PodScheduled") && v.Status != "True" {
			return false
		}
	}
	return true
}

func podStatus(ctx context.Context, userName string, accessToken string, payModelPtr *PayModel) (*WorkspaceStatus, error) {
	status := WorkspaceStatus{}
	podClient, isExternalClient, err := getPodClient(ctx, userName, payModelPtr)
	if err != nil {
		// Config.Logger.Panic("Error trying to fetch kubeConfig: %v", err)
		status.Status = fmt.Sprintf("%v", err)
		return &status, err
	}

	podName := userToResourceName(userName, "pod")

	serviceName := userToResourceName(userName, "service")

	pod, err := podClient.Pods(Config.Config.UserNamespace).Get(ctx, podName, metav1.GetOptions{})
	_, serviceErr := podClient.Services(Config.Config.UserNamespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		if isExternalClient && serviceErr == nil {
			// only worry about service if podClient is external EKS
			policy := metav1.DeletePropagationBackground
			deleteOptions := metav1.DeleteOptions{
				PropagationPolicy: &policy,
			}
			err := podClient.Services(Config.Config.UserNamespace).Delete(ctx, serviceName, deleteOptions)
			if err != nil {

				Config.Logger.Error("Error deleting service",
					"error", err,
					"userName", userName,
					"serviceName", serviceName,
				)
			}

			status.Status = "Terminating"
			return &status, nil
		} else {
			// not found
			status.Status = "Not Found"
			return &status, nil
		}
	}

	if pod.DeletionTimestamp != nil {
		status.Status = "Terminating"
		return &status, nil
	}

	switch pod.Status.Phase {
	case "Failed":
		fallthrough
	case "Succeeded":
		fallthrough
	case "Unknown":
		status.Status = "Stopped"
	case "Pending":
		fallthrough
	case "Running":
		allReady := checkPodReadiness(pod)
		if allReady {
			status.Status = "Running"
			for _, container := range pod.Spec.Containers {
				for _, arg := range container.Args {
					if strings.Contains(arg, "shutdown_no_activity_timeout=") {
						argSplit := strings.Split(arg, "=")
						idleTimeLimit, err := strconv.Atoi(argSplit[len(argSplit)-1])
						if err == nil {
							status.IdleTimeLimit = idleTimeLimit * 1000
							lastActivityTime, err := getKernelIdleTimeWithContext(ctx, accessToken)
							status.LastActivityTime = lastActivityTime
							if err != nil {
								log.Println(err.Error())
							}
						} else {
							log.Println(err.Error())
						}
						break
					}
				}
			}
		} else {
			status.Status = "Launching"
			conditions := make([]PodConditions, len(pod.Status.Conditions))
			for i, cond := range pod.Status.Conditions {
				conditions[i].Status = string(cond.Status)
				conditions[i].Type = string(cond.Type)
			}
			status.Conditions = conditions
			containerStates := make([]ContainerStates, len(pod.Status.ContainerStatuses))
			for i, cs := range pod.Status.ContainerStatuses {
				containerStates[i].State = cs.State
				containerStates[i].Name = cs.Name
				containerStates[i].Ready = cs.Ready
			}
			status.ContainerStates = containerStates
		}
	default:
		fmt.Printf("Unknown pod status for %s: %s\n", podName, string(pod.Status.Phase))
	}

	return &status, nil
}

func statusK8sPod(ctx context.Context, userName string, accessToken string, payModelPtr *PayModel) (*WorkspaceStatus, error) {
	status, err := podStatus(ctx, userName, accessToken, payModelPtr)
	if err != nil {
		status.Status = fmt.Sprintf("%v", err)

		Config.Logger.Error("Error getting status",
			"error", err,
			"userName", userName,
		)
	}
	return status, nil
}

func deleteK8sPod(ctx context.Context, userName string, accessToken string, payModelPtr *PayModel) error {
	podClient, _, err := getPodClient(ctx, userName, payModelPtr)
	if err != nil {
		return err
	}

	policy := metav1.DeletePropagationBackground
	var grace int64 = 20
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy:  &policy,
		GracePeriodSeconds: &grace,
	}

	podName := userToResourceName(userName, "pod")
	pod, err := podClient.Pods(Config.Config.UserNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("A workspace pod was not found: %s", err)
	}
	containers := pod.Spec.Containers
	var mountedAPIKeyID string
	for i := range containers {
		if containers[i].Name == "hatchery-container" {
			for j := range containers[i].Env {
				if containers[i].Env[j].Name == "API_KEY_ID" {
					mountedAPIKeyID = containers[i].Env[j].Value
					break
				}
			}
			break
		}
	}
	if mountedAPIKeyID != "" {
		fmt.Printf("Found mounted API key. Attempting to delete API Key with ID %s for user %s\n", mountedAPIKeyID, userName)
		err := deleteAPIKeyWithContext(ctx, accessToken, mountedAPIKeyID)
		if err != nil {
			fmt.Printf("Error occurred when deleting API Key with ID %s for user %s: %s\n", mountedAPIKeyID, userName, err.Error())
		} else {
			fmt.Printf("API Key with ID %s for user %s has been deleted\n", mountedAPIKeyID, userName)
		}
	}

	fmt.Printf("Attempting to delete pod %s for user %s\n", podName, userName)
	err = podClient.Pods(Config.Config.UserNamespace).Delete(ctx, podName, deleteOptions)
	if err != nil {
		fmt.Printf("Error occurred when deleting pod: %s", err)
	}

	serviceName := userToResourceName(userName, "service")
	_, err = podClient.Services(Config.Config.UserNamespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("A workspace service was not found: %s", err)
	}
	fmt.Printf("Attempting to delete service %s for user %s\n", serviceName, userName)
	err = podClient.Services(Config.Config.UserNamespace).Delete(ctx, serviceName, deleteOptions)
	if err != nil {
		fmt.Printf("Error occurred when deleting service: %s", err)
	}

	return nil
}

// userToResourceName is a helper for generating names for
// different types of kubernetes resources given a user name
// and a resource type
func userToResourceName(userName string, resourceType string) string {
	safeUserName := escapism(userName)
	if resourceType == "pod" {
		return fmt.Sprintf("hatchery-%s", safeUserName)
	}
	if resourceType == "service" {
		return fmt.Sprintf("h-%s-s", safeUserName)
	}
	if resourceType == "mapping" { // ambassador mapping
		return fmt.Sprintf("%s-mapping", safeUserName)
	}

	return fmt.Sprintf("%s-%s", resourceType, safeUserName)
}

// buildPod returns a pod ready to pass to the k8s API given
// a hatchery Container instance, and the name of the user
// launching the app
func buildPod(hatchConfig *FullHatcheryConfig, hatchApp *Container, userName string, extraVars []k8sv1.EnvVar) (pod *k8sv1.Pod, err error) {
	podName := userToResourceName(userName, "pod")
	labels := make(map[string]string)
	labels["app"] = podName
	annotations := make(map[string]string)
	annotations["gen3username"] = userName
	var sideCarRunAsUser int64
	var sideCarRunAsGroup int64
	var hostToContainer = k8sv1.MountPropagationHostToContainer
	var bidirectional = k8sv1.MountPropagationBidirectional
	var envVars []k8sv1.EnvVar
	// a null image indicates a dockstore app - always mount user volume
	mountUserVolume := hatchApp.UserVolumeLocation != ""

	Config.Logger.Info("building pod",
		"podName", podName,
		"userName", userName,
		"appName", hatchApp.Name,
	)

	for key, value := range hatchApp.Env {
		envVar := k8sv1.EnvVar{
			Name:  key,
			Value: value,
		}
		envVars = append(envVars, envVar)
	}

	var sidecarEnvVars []k8sv1.EnvVar
	for key, value := range hatchConfig.Config.Sidecar.Env {
		envVar := k8sv1.EnvVar{
			Name:  key,
			Value: value,
		}
		sidecarEnvVars = append(sidecarEnvVars, envVar)
	}
	for _, value := range extraVars {
		sidecarEnvVars = append(sidecarEnvVars, value)
		envVars = append(envVars, value)
	}

	sidecarEnvVars = append(sidecarEnvVars, k8sv1.EnvVar{
		Name:  "GEN3_ENDPOINT",
		Value: os.Getenv("GEN3_ENDPOINT"),
	})
	envVars = append(envVars, k8sv1.EnvVar{
		Name:  "GEN3_ENDPOINT",
		Value: os.Getenv("GEN3_ENDPOINT"),
	})

	var lifeCycle = k8sv1.Lifecycle{}
	if hatchApp.LifecyclePreStop != nil && len(hatchApp.LifecyclePreStop) > 0 {
		lifeCycle.PreStop = &k8sv1.Handler{
			Exec: &k8sv1.ExecAction{
				Command: hatchApp.LifecyclePreStop,
			},
		}
	}
	if hatchApp.LifecyclePostStart != nil && len(hatchApp.LifecyclePostStart) > 0 {
		lifeCycle.PostStart = &k8sv1.Handler{
			Exec: &k8sv1.ExecAction{
				Command: hatchApp.LifecyclePostStart,
			},
		}
	}

	var securityContext = k8sv1.PodSecurityContext{}

	if hatchApp.UserUID != 0 {
		securityContext.RunAsUser = &hatchApp.UserUID
	}
	if hatchApp.GroupUID != 0 {
		securityContext.RunAsGroup = &hatchApp.GroupUID
	}
	if hatchApp.FSGID != 0 {
		securityContext.FSGroup = &hatchApp.FSGID
	}

	var mountSharedMemory = hatchApp.UseSharedMemory == "true"
	var volumes = []k8sv1.Volume{
		{
			Name:         "shared-data",
			VolumeSource: k8sv1.VolumeSource{},
		},
	}

	volumes = append(volumes, k8sv1.Volume{
		Name:         "gen3",
		VolumeSource: k8sv1.VolumeSource{},
	})

	if mountSharedMemory {
		volumes = append(volumes, k8sv1.Volume{
			Name: "dshm",
			VolumeSource: k8sv1.VolumeSource{
				EmptyDir: &k8sv1.EmptyDirVolumeSource{
					Medium: "Memory",
				},
			},
		})
	}

	if mountUserVolume {
		claimName := userToResourceName(userName, "claim")
		volumes = append(volumes, k8sv1.Volume{
			Name: "user-data",
			VolumeSource: k8sv1.VolumeSource{
				PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
					ClaimName: claimName,
				},
			},
		})
	}

	var pullPolicy k8sv1.PullPolicy
	switch hatchApp.PullPolicy {
	case "IfNotPresent":
		pullPolicy = k8sv1.PullPolicy(k8sv1.PullIfNotPresent)
	case "Always":
		pullPolicy = k8sv1.PullPolicy(k8sv1.PullAlways)
	case "Never":
		pullPolicy = k8sv1.PullPolicy(k8sv1.PullNever)
	default:
		pullPolicy = k8sv1.PullPolicy(k8sv1.PullIfNotPresent)
	}

	var volumeMounts = []k8sv1.VolumeMount{
		{
			MountPath:        "/data",
			Name:             "shared-data",
			MountPropagation: &bidirectional,
		},
	}

	volumeMounts = append(volumeMounts, k8sv1.VolumeMount{
		MountPath: "/.gen3",
		Name:      "gen3",
	})

	if mountSharedMemory {
		volumeMounts = append(volumeMounts, k8sv1.VolumeMount{
			MountPath: "/dev/shm",
			Name:      "dshm",
		})
	}

	pod = &k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        podName,
			Namespace:   hatchConfig.Config.UserNamespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: k8sv1.PodSpec{
			SecurityContext:    &securityContext,
			InitContainers:     []k8sv1.Container{},
			EnableServiceLinks: &falseVal,
			Containers: []k8sv1.Container{
				{
					Name:  "fuse-container",
					Image: hatchConfig.Config.Sidecar.Image,
					SecurityContext: &k8sv1.SecurityContext{
						Privileged: &trueVal,
						RunAsUser:  &sideCarRunAsUser,
						RunAsGroup: &sideCarRunAsGroup,
					},
					ImagePullPolicy: k8sv1.PullPolicy(k8sv1.PullAlways),
					Env:             sidecarEnvVars,
					Command:         hatchConfig.Config.Sidecar.Command,
					Args:            hatchConfig.Config.Sidecar.Args,
					VolumeMounts:    volumeMounts,
					Resources: k8sv1.ResourceRequirements{
						Limits: k8sv1.ResourceList{
							k8sv1.ResourceCPU:    resource.MustParse(hatchConfig.Config.Sidecar.CPULimit),
							k8sv1.ResourceMemory: resource.MustParse(hatchConfig.Config.Sidecar.MemoryLimit),
						},
						Requests: k8sv1.ResourceList{
							k8sv1.ResourceCPU:    resource.MustParse(hatchConfig.Config.Sidecar.CPULimit),
							k8sv1.ResourceMemory: resource.MustParse(hatchConfig.Config.Sidecar.MemoryLimit),
						},
					},
					Lifecycle: &k8sv1.Lifecycle{
						PreStop: &k8sv1.Handler{
							Exec: &k8sv1.ExecAction{
								Command: hatchConfig.Config.Sidecar.LifecyclePreStop,
							},
						},
					},
				},
			},
			RestartPolicy:    k8sv1.RestartPolicyNever,
			ImagePullSecrets: []k8sv1.LocalObjectReference{},
			NodeSelector: map[string]string{
				"role": "jupyter",
			},
			Tolerations: []k8sv1.Toleration{{Key: "role", Operator: "Equal", Value: "jupyter", Effect: "NoSchedule", TolerationSeconds: nil}},
			Volumes:     volumes,
		},
	}

	// some pods (ex - dockstore apps) only have "Friend" containers
	if hatchApp.Image != "" {
		var volumeMounts = []k8sv1.VolumeMount{
			{
				MountPath:        "/data",
				Name:             "shared-data",
				MountPropagation: &hostToContainer,
			},
		}

		if hatchApp.Gen3VolumeLocation != "" {
			volumeMounts = append(volumeMounts, k8sv1.VolumeMount{
				MountPath: hatchApp.Gen3VolumeLocation,
				Name:      "gen3",
			})
		} else {
			volumeMounts = append(volumeMounts, k8sv1.VolumeMount{
				MountPath: "/.gen3",
				Name:      "gen3",
			})
		}

		if hatchApp.UserVolumeLocation != "" {
			volumeMounts = append(volumeMounts, k8sv1.VolumeMount{
				MountPath: hatchApp.UserVolumeLocation,
				Name:      "user-data",
			})
		}

		pod.Spec.Containers = append(pod.Spec.Containers, k8sv1.Container{
			Name:  "hatchery-container",
			Image: hatchApp.Image,
			SecurityContext: &k8sv1.SecurityContext{
				Privileged: &falseVal,
			},
			ImagePullPolicy: pullPolicy,
			Env:             envVars,
			Command:         hatchApp.Command,
			Args:            hatchApp.Args,
			VolumeMounts:    volumeMounts,
			Resources: k8sv1.ResourceRequirements{
				Limits: k8sv1.ResourceList{
					k8sv1.ResourceCPU:    resource.MustParse(hatchApp.CPULimit),
					k8sv1.ResourceMemory: resource.MustParse(hatchApp.MemoryLimit),
				},
				Requests: k8sv1.ResourceList{
					k8sv1.ResourceCPU:    resource.MustParse(hatchApp.CPULimit),
					k8sv1.ResourceMemory: resource.MustParse(hatchApp.MemoryLimit),
				},
			},
			Lifecycle: &lifeCycle,
			ReadinessProbe: &k8sv1.Probe{
				Handler: k8sv1.Handler{
					HTTPGet: &k8sv1.HTTPGetAction{
						Path: hatchApp.ReadyProbe,
						Port: intstr.FromInt(int(hatchApp.TargetPort)),
					},
				},
			},
		})
	}

	pod.Spec.Containers = append(pod.Spec.Containers, hatchApp.Friends...)

	return pod, nil
}

func createLocalK8sPod(ctx context.Context, hash string, userName string, accessToken string) error {
	hatchApp := Config.ContainersMap[hash]

	Config.Logger.Info("Creating a Local kubernetes pod for workspace.",
		"userName", userName,
		"image", hatchApp.Image,
		"cpulimit", hatchApp.CPULimit,
		"memorylimit", hatchApp.MemoryLimit,
	)
	var extraVars []k8sv1.EnvVar
	apiKey, err := getAPIKeyWithContext(ctx, accessToken)
	if err != nil {

		Config.Logger.Error("Failed to get API key.",
			"userName", userName,
			"error", err,
		)
		return err
	}

	Config.Logger.Info("Created API key.",
		"userName", userName,
		"keyID", apiKey.KeyID,
	)

	extraVars = append(extraVars, k8sv1.EnvVar{
		Name:  "API_KEY",
		Value: apiKey.APIKey,
	})
	extraVars = append(extraVars, k8sv1.EnvVar{
		Name:  "API_KEY_ID",
		Value: apiKey.KeyID,
	})

	pod, err := buildPod(Config, &hatchApp, userName, extraVars)
	if err != nil {

		Config.Logger.Error("Failed to configure pod for launch.",
			"username", userName,
			"error", err,
		)
		return err
	}
	podName := userToResourceName(userName, "pod")
	podClient, _, err := getPodClient(ctx, userName, nil)
	if err != nil {
		Config.Logger.Error("Failed to get pod client.",
			"error", err,
			"username", userName,
		)
		return err
	}
	// a null image indicates a dockstore app - always mount user volume
	mountUserVolume := hatchApp.UserVolumeLocation != ""
	if mountUserVolume {
		claimName := userToResourceName(userName, "claim")

		_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Get(ctx, claimName, metav1.GetOptions{})
		if err != nil {
			Config.Logger.Debug("Creating PersistentVolumeClaim.",
				"claimName", claimName,
				"username", userName,
			)
			pvc := &k8sv1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:        claimName,
					Annotations: pod.Annotations,
					Labels:      pod.Labels,
				},
				Spec: k8sv1.PersistentVolumeClaimSpec{
					AccessModes: []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce},
					Resources: k8sv1.ResourceRequirements{
						Requests: k8sv1.ResourceList{
							k8sv1.ResourceStorage: resource.MustParse(Config.Config.UserVolumeSize),
						},
					},
				},
			}
			_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Create(ctx, pvc, metav1.CreateOptions{})
			if err != nil {

				Config.Logger.Error("Failed to create PVC.",
					"claimName", claimName,
					"error", err,
				)
				return err
			}
		}
	}

	_, err = podClient.Pods(Config.Config.UserNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {

		Config.Logger.Error("Failed to launch pod.",
			"podName", hatchApp.Name,
			"username", userName,
			"image", hatchApp.Image,
			"cpulimit", hatchApp.CPULimit,
			"memorylimit", hatchApp.MemoryLimit,
			"error", err,
		)
		return err
	}

	Config.Logger.Info("Launched pod.",
		"podName", hatchApp.Name,
		"username", userName,
		"image", hatchApp.Image,
		"cpulimit", hatchApp.CPULimit,
		"memorylimit", hatchApp.MemoryLimit,
	)

	serviceName := userToResourceName(userName, "service")
	labelsService := make(map[string]string)
	labelsService["app"] = podName
	annotationsService := make(map[string]string)
	annotationsService["getambassador.io/config"] = fmt.Sprintf(ambassadorYaml, userToResourceName(userName, "mapping"), userName, serviceName, Config.Config.UserNamespace, hatchApp.PathRewrite, hatchApp.UseTLS)

	_, err = podClient.Services(Config.Config.UserNamespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err == nil {
		policy := metav1.DeletePropagationBackground
		deleteOptions := metav1.DeleteOptions{
			PropagationPolicy: &policy,
		}
		err = podClient.Services(Config.Config.UserNamespace).Delete(ctx, serviceName, deleteOptions)
		if err != nil {
			fmt.Printf("Error occurred when deleting service: %s", err)
		}
	}

	service := &k8sv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        serviceName,
			Namespace:   Config.Config.UserNamespace,
			Labels:      labelsService,
			Annotations: annotationsService,
		},
		Spec: k8sv1.ServiceSpec{
			Type:     k8sv1.ServiceTypeClusterIP,
			Selector: map[string]string{"app": podName},
			Ports: []k8sv1.ServicePort{
				{
					Name:     podName,
					Protocol: k8sv1.ProtocolTCP,
					Port:     80,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: hatchApp.TargetPort,
					},
				},
			},
		},
	}

	_, err = podClient.Services(Config.Config.UserNamespace).Create(ctx, service, metav1.CreateOptions{})
	if err != nil {
		fmt.Printf("Failed to launch service %s for user %s forwarding port %d. Error: %s\n", serviceName, userName, hatchApp.TargetPort, err)
		return err
	}

	fmt.Printf("Launched service %s for user %s forwarding port %d\n", serviceName, userName, hatchApp.TargetPort)

	return nil
}

func createExternalK8sPod(ctx context.Context, hash string, userName string, accessToken string, payModel PayModel) error {
	hatchApp := Config.ContainersMap[hash]

	Config.Logger.Info("Creating a workspace pod in external kubernetes cluster.",
		"username", userName,
		"image", hatchApp.Image,
		"cpulimit", hatchApp.CPULimit,
		"memorylimit", hatchApp.MemoryLimit,
	)
	podClient, err := NewEKSClientset(ctx, userName, payModel)
	if err != nil {

		Config.Logger.Error("Failed to create external pod client.",
			"username", userName,
			"error", err,
		)
		return err
	}

	apiKey, err := getAPIKeyWithContext(ctx, accessToken)
	if err != nil {

		Config.Logger.Error("Failed to get API key.",
			"username", userName,
			"error", err,
		)
		return err
	}

	Config.Logger.Info("Created API key.",
		"username", userName,
		"keyID", apiKey.KeyID,
	)

	// Check if NS exists in external cluster, if not create it.
	ns, err := podClient.Namespaces().Get(ctx, Config.Config.UserNamespace, metav1.GetOptions{})
	if err != nil {
		nsName := &k8sv1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: Config.Config.UserNamespace,
			},
		}
		_, err = podClient.Namespaces().Create(ctx, nsName, metav1.CreateOptions{})
		if err != nil {

			Config.Logger.Error("Failed to create namespace.",
				"namespace", Config.Config.UserNamespace,
				"username", userName,
				"error", err,
			)
		} else {

			Config.Logger.Info("Created namespace.",
				"namespace", ns,
				"username", userName,
			)
		}
	}

	var extraVars []k8sv1.EnvVar

	extraVars = append(extraVars, k8sv1.EnvVar{
		Name:  "WTS_OVERRIDE_URL",
		Value: "https://" + os.Getenv("GEN3_ENDPOINT") + "/wts",
	})
	extraVars = append(extraVars, k8sv1.EnvVar{
		Name:  "API_KEY",
		Value: apiKey.APIKey,
	})
	extraVars = append(extraVars, k8sv1.EnvVar{
		Name:  "API_KEY_ID",
		Value: apiKey.KeyID,
	})
	// TODO: still mounting access token for now, remove this when fully switched to use API key
	extraVars = append(extraVars, k8sv1.EnvVar{
		Name:  "ACCESS_TOKEN",
		Value: accessToken,
	})

	pod, err := buildPod(Config, &hatchApp, userName, extraVars)
	if err != nil {

		Config.Logger.Error("Failed to configure pod for launch.",
			"username", userName,
			"error", err,
		)
		return err
	}
	podName := userToResourceName(userName, "pod")
	// a null image indicates a dockstore app - always mount user volume
	mountUserVolume := hatchApp.UserVolumeLocation != ""
	if mountUserVolume {
		claimName := userToResourceName(userName, "claim")

		_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Get(ctx, claimName, metav1.GetOptions{})
		if err != nil {

			Config.Logger.Info("Creating PersistentVolumeClaim.",
				"claimName", claimName,
				"username", userName,
			)
			pvc := &k8sv1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:        claimName,
					Annotations: pod.Annotations,
					Labels:      pod.Labels,
				},
				Spec: k8sv1.PersistentVolumeClaimSpec{
					AccessModes: []k8sv1.PersistentVolumeAccessMode{k8sv1.ReadWriteOnce},
					Resources: k8sv1.ResourceRequirements{
						Requests: k8sv1.ResourceList{
							k8sv1.ResourceStorage: resource.MustParse(Config.Config.UserVolumeSize),
						},
					},
				},
			}

			_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Create(ctx, pvc, metav1.CreateOptions{})
			if err != nil {

				Config.Logger.Error("Failed to create PVC.",
					"claimName", claimName,
					"username", userName,
					"error", err,
				)
				return err
			}
		}
	}

	_, err = podClient.Pods(Config.Config.UserNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {

		Config.Logger.Error("Failed to launch pod.",
			"podName", podName,
			"username", userName,
			"image", hatchApp.Image,
			"cpu", hatchApp.CPULimit,
			"memory", hatchApp.MemoryLimit,
			"error", err,
		)
		return err
	}

	Config.Logger.Info("Launched pod.",
		"podName", podName,
		"username", userName,
		"image", hatchApp.Image,
		"cpu", hatchApp.CPULimit,
		"memory", hatchApp.MemoryLimit,
	)

	serviceName := userToResourceName(userName, "service")
	labelsService := make(map[string]string)
	labelsService["app"] = podName
	annotationsService := make(map[string]string)
	annotationsService["getambassador.io/config"] = fmt.Sprintf(ambassadorYaml, userToResourceName(userName, "mapping"), userName, serviceName, Config.Config.UserNamespace, hatchApp.PathRewrite, hatchApp.UseTLS)
	annotationsService["service.beta.kubernetes.io/aws-load-balancer-internal"] = "true"
	_, err = podClient.Services(Config.Config.UserNamespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err == nil {
		// This probably happened as the result of some error... there was no pod but was a service
		// Lets just clean it up and proceed
		policy := metav1.DeletePropagationBackground
		deleteOptions := metav1.DeleteOptions{
			PropagationPolicy: &policy,
		}
		err = podClient.Services(Config.Config.UserNamespace).Delete(ctx, serviceName, deleteOptions)
		if err != nil {
			fmt.Printf("Error occurred when deleting service: %s", err)
		}
	}

	service := &k8sv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        serviceName,
			Namespace:   Config.Config.UserNamespace,
			Labels:      labelsService,
			Annotations: annotationsService,
		},
		Spec: k8sv1.ServiceSpec{
			Type:     k8sv1.ServiceTypeNodePort,
			Selector: map[string]string{"app": podName},
			Ports: []k8sv1.ServicePort{
				{
					Name:     podName,
					Protocol: k8sv1.ProtocolTCP,
					Port:     80,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: hatchApp.TargetPort,
					},
				},
			},
		},
	}

	_, err = podClient.Services(Config.Config.UserNamespace).Create(ctx, service, metav1.CreateOptions{})
	if err != nil {
		fmt.Printf("Failed to launch service %s for user %s forwarding port %d. Error: %s\n", serviceName, userName, hatchApp.TargetPort, err)
		return err
	}

	Config.Logger.Info("Launched service.",
		"serviceName", serviceName,
		"username", userName,
		"port", hatchApp.TargetPort,
	)

	nodes, _ := podClient.Nodes().List(context.TODO(), metav1.ListOptions{})
	NodeIP := nodes.Items[0].Status.Addresses[0].Address

	err = createLocalService(ctx, userName, hash, NodeIP, payModel)
	if err != nil {
		fmt.Println(err.Error())
		return err
	}

	return nil
}

// Creates a local service that portal can reach
// and route traffic to pod in external cluster.
func createLocalService(ctx context.Context, userName string, hash string, serviceURL string, payModel PayModel) error {
	const localAmbassadorYaml = `---
apiVersion: ambassador/v1
kind:  Mapping
name:  %s
prefix: /
headers:
  remote_user: %s
service: %s:%d
bypass_auth: true
timeout_ms: 300000
use_websocket: true
rewrite: %s
tls: %s
`
	hatchApp := Config.ContainersMap[hash]

	serviceName := userToResourceName(userName, "service")
	NodePort := int32(80)
	if !payModel.Ecs {
		externalPodClient, err := NewEKSClientset(ctx, userName, payModel)
		if err != nil {
			return err
		}
		service, err := externalPodClient.Services(Config.Config.UserNamespace).Get(ctx, serviceName, metav1.GetOptions{})
		NodePort = service.Spec.Ports[0].NodePort
		if err != nil {
			return err
		}
	}
	podName := userToResourceName(userName, "pod")

	labelsService := make(map[string]string)
	labelsService["app"] = podName
	annotationsService := make(map[string]string)
	annotationsService["getambassador.io/config"] = fmt.Sprintf(localAmbassadorYaml, userToResourceName(userName, "mapping"), userName, serviceURL, NodePort, hatchApp.PathRewrite, hatchApp.UseTLS)

	localPodClient := getLocalPodClient()
	_, err := localPodClient.Services(Config.Config.UserNamespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err == nil {
		// This probably happened as the result of some error... there was no pod but was a service
		// Lets just clean it up and proceed
		policy := metav1.DeletePropagationBackground
		deleteOptions := metav1.DeleteOptions{
			PropagationPolicy: &policy,
		}
		err = localPodClient.Services(Config.Config.UserNamespace).Delete(ctx, serviceName, deleteOptions)
		if err != nil {
			fmt.Printf("Error occurred when deleting service: %s", err)
		}
	}

	localService := &k8sv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        serviceName,
			Namespace:   Config.Config.UserNamespace,
			Labels:      labelsService,
			Annotations: annotationsService,
		},
		Spec: k8sv1.ServiceSpec{
			Type:     k8sv1.ServiceTypeClusterIP,
			Selector: map[string]string{"app": podName},
			Ports: []k8sv1.ServicePort{
				{
					Name:     podName,
					Protocol: k8sv1.ProtocolTCP,
					Port:     80,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: hatchApp.TargetPort,
					},
				},
			},
		},
	}

	_, err = localPodClient.Services(Config.Config.UserNamespace).Create(ctx, localService, metav1.CreateOptions{})
	if err != nil {
		fmt.Printf("Failed to launch local service %s for user %s forwarding port %d. Error: %s\n", serviceName, userName, hatchApp.TargetPort, err)
		return err
	}

	Config.Logger.Info("Launched local service.",
		"serviceName", serviceName,
		"username", userName,
		"port", hatchApp.TargetPort,
	)
	return nil
}
