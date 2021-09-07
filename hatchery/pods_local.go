package hatchery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/uc-cdis/hatchery/hatchery/openapi"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	clientcmd "k8s.io/client-go/tools/clientcmd"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	kubernetestrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/k8s.io/client-go/kubernetes"
)


func getPodClient(ctx context.Context, userName string) (corev1.CoreV1Interface, bool, error) {
	if payModelExistsForUser(userName) {
		podClient, err := NewEKSClientset(ctx, userName)
		if err != nil {
			Config.Logger.Printf("Error fetching EKS kubeconfig: %v", err)
			return nil, true, err
		} else {
			return podClient, true, nil
		}
	} else {
		return getLocalPodClient(), false, nil
	}
}

func getLocalPodClient() corev1.CoreV1Interface {
	// attempt to create config using $HOME/.kube/config
	home, exists := os.LookupEnv("HOME")
  if !exists {
      home = "/root"
  }
  configPath := filepath.Join(home, ".kube", "config")
  config, err := clientcmd.BuildConfigFromFlags("", configPath)
  if err != nil {
		//if the kube config file is not avalible, use the InCluster config
		config, err = rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
	}
	config.WrapTransport = kubernetestrace.WrapRoundTripper
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	// Access jobs. We can't do it all in one line, since we need to receive the
	// errors and manage them appropriately
	podClient := clientset.CoreV1()
	return podClient
}


func containerState2ToOpenAPI(state k8sv1.ContainerState) openapi.ContainerStateDetail {
	out := openapi.ContainerStateDetail{}
	if state.Running != nil {
		out.Running = &openapi.ContainerStateDetailRunning{ StartedAt: state.Running.StartedAt.String() }
	} else if state.Terminated != nil {
		out.Terminated = &openapi.ContainerStateDetailTerminated{
			ExitCode: state.Terminated.ExitCode,
			Signal: state.Terminated.Signal,
			Reason: state.Terminated.Reason,
			Message: state.Terminated.Message,
			StartedAt: state.Terminated.StartedAt.String(),
			FinishedAt: state.Terminated.FinishedAt.String(),
			ContainerID: state.Terminated.ContainerID,
		}
	} else if state.Waiting != nil {
		out.Waiting = &openapi.ContainerStateDetailWaiting{
			Reason: state.Waiting.Reason,
			Message: state.Waiting.Message,
		}
	}
	return out
}


func listWorkspacePods(ctx context.Context, userName string ) ([]*openapi.WorkspaceStatus, error) {
	podClient, isExternalClient, err := getPodClient(ctx, userName)
	if err != nil {
		return nil, err
	}

	_ = isExternalClient

	serviceMapper, err := Config.Config.GetServiceMapper()
	if err != nil {
		return nil, err
	}

	labelSelect := fmt.Sprintf("%s=%s", LABEL_USER, escapism(userName))
	podList, err := podClient.Pods(Config.Config.UserNamespace).List(ctx, metav1.ListOptions{LabelSelector:labelSelect})
	if err != nil {
		return nil, err
	}
	out := []*openapi.WorkspaceStatus{}
	for _, pod := range podList.Items {
		status := openapi.WorkspaceStatus{}
		if pod.DeletionTimestamp != nil {
			status.Status = "Terminating"
		} else {
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
				allReady := checkPodReadiness(&pod)
				if allReady == true {
					status.Status = "Running"
				} else {
					status.Status = "Launching"
					conditions := make([]openapi.PodCondition, len(pod.Status.Conditions))
					for i, cond := range pod.Status.Conditions {
						conditions[i].Status = string(cond.Status)
						conditions[i].Type = string(cond.Type)
					}
					status.Conditions = conditions
					containerStates := make([]openapi.ContainerState, len(pod.Status.ContainerStatuses))
					for i, cs := range pod.Status.ContainerStatuses {
						containerStates[i].State = containerState2ToOpenAPI(cs.State)
						containerStates[i].Name = cs.Name
						containerStates[i].Ready = cs.Ready
					}
					status.ContainerStates = containerStates
				}
			default:
				fmt.Printf("Unknown pod status for %s: %s\n", pod.Name, string(pod.Status.Phase))
			}
			status.WorkspaceID = pod.Labels[LABEL_POD]
			status.AppID = pod.Labels[LABEL_APPID]
			if s, err := serviceMapper.GetURL(Config.Config.UserNamespace, status.WorkspaceID); err == nil {
				status.Url = s
			}
		}
		out = append(out, &status)
	}
	return out, nil
}

/*
func workspacePodStatus(ctx context.Context, userName string) (*openapi.WorkspaceStatus, error) {
	status := openapi.WorkspaceStatus{}
	podClient, isExternalClient, err := getPodClient(ctx, userName)
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
			// only worry for service if podClient is external EKS
			Config.Logger.Printf("Pod has been terminated, but service is still being terminated. Wait for service to be killed.")
			// Pod has been terminated, but service is still being terminated. Wait for service to be killed
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
		if allReady == true {
			status.Status = "Running"
		} else {
			status.Status = "Launching"
			conditions := make([]openapi.PodCondition, len(pod.Status.Conditions))
			for i, cond := range pod.Status.Conditions {
				conditions[i].Status = string(cond.Status)
				conditions[i].Type = string(cond.Type)
			}
			status.Conditions = conditions
			containerStates := make([]openapi.ContainerState, len(pod.Status.ContainerStatuses))
			for i, cs := range pod.Status.ContainerStatuses {
				containerStates[i].State = containerState2ToOpenAPI(cs.State)
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
*/

/*
func statusK8sPod(ctx context.Context, userName string) (*openapi.WorkspaceStatus, error) {
	status, err := workspacePodStatus(ctx, userName)
	if err != nil {
		status.Status = fmt.Sprintf("%v", err)
		Config.Logger.Printf("Error getting status: %v", err)
	}
	return status, nil
}
*/

func deleteK8sPod(ctx context.Context, accessToken string, userName string, workspaceID string) error {
	podClient, _, err := getPodClient(ctx, userName)
	if err != nil {
		return err
	}

	policy := metav1.DeletePropagationBackground
	var grace int64 = 20
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy:  &policy,
		GracePeriodSeconds: &grace,
	}

	//podName := userToResourceName(userName, "pod")
	pod, err := podClient.Pods(Config.Config.UserNamespace).Get(ctx, workspaceID, metav1.GetOptions{})
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

	smap, err := Config.Config.GetServiceMapper()
	if err != nil {
		fmt.Printf("Failed getting service mapper: %s\n", err)
	} else {
		err = smap.Stop(Config.Config.UserNamespace, workspaceID )
		fmt.Printf("Failed stopping service mapper: %s\n", err)
	}

	fmt.Printf("Attempting to delete pod %s for user %s\n", workspaceID, userName)
	podClient.Pods(Config.Config.UserNamespace).Delete(ctx, workspaceID, deleteOptions)

	//serviceName := userToResourceName(userName, "service")
	_, err = podClient.Services(Config.Config.UserNamespace).Get(ctx, workspaceID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("A workspace service was not found: %s", err)
	}
	fmt.Printf("Attempting to delete service %s for user %s\n", workspaceID, userName)
	podClient.Services(Config.Config.UserNamespace).Delete(ctx, workspaceID, deleteOptions)

	return nil
}

// buildPod returns a pod ready to pass to the k8s API given
// a hatchery Container instance, and the name of the user
// launching the app
func buildPod(hatchConfig *FullHatcheryConfig, hatchApp *Container, userName string, appID string, extraVars []k8sv1.EnvVar) (pod *k8sv1.Pod, err error) {
	workspaceID := getBaseName(userName, appID)

	labels := make(map[string]string)
	labels[LABEL_POD] = workspaceID
	labels[LABEL_USER] = escapism(userName)
	labels[LABEL_APPID] = appID

	annotations := make(map[string]string)
	annotations["gen3username"] = userName
	var sideCarRunAsUser int64
	var sideCarRunAsGroup int64
	var hostToContainer = k8sv1.MountPropagationHostToContainer
	var bidirectional = k8sv1.MountPropagationBidirectional
	var envVars []k8sv1.EnvVar
	// a null image indicates a dockstore app - always mount user volume
	mountUserVolume := hatchApp.UserVolumeLocation != ""
	hatchConfig.Logger.Printf("building pod %v for %v", hatchApp.Name, userName)

	for key, value := range hatchApp.Env {
		envVar := k8sv1.EnvVar{
			Name:  key,
			Value: value,
		}
		envVars = append(envVars, envVar)
	}

	//hatchConfig.Logger.Printf("environment configured")

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
	// scan if sidecarEnvVars has HOSTNAME, and add it if not
	sidecarEnvVarsCopy := sidecarEnvVars[:0]
	// this is the best we can do with golang
	for i, value := range sidecarEnvVarsCopy {
		if value.Name == "HOSTNAME" {
			break
		}
		if i == len(sidecarEnvVarsCopy)-1 {
			sidecarEnvVars = append(sidecarEnvVars, k8sv1.EnvVar{
				Name:  "HOSTNAME",
				Value: os.Getenv("HOSTNAME"),
			})
		}
	}
	// do the same thing for envVars
	envVarsCopy := envVars[:0]
	for i, value := range envVarsCopy {
		if value.Name == "HOSTNAME" {
			break
		}
		if i == len(envVarsCopy)-1 {
			envVars = append(envVars, k8sv1.EnvVar{
				Name:  "HOSTNAME",
				Value: os.Getenv("HOSTNAME"),
			})
		}
	}

	//hatchConfig.Logger.Printf("sidecar configured")

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

	//hatchConfig.Logger.Printf("lifecycle configured")

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
		//claimName := userToResourceName(baseName, "claim")
		volumes = append(volumes, k8sv1.Volume{
			Name: "user-data",
			VolumeSource: k8sv1.VolumeSource{
				PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
					ClaimName: workspaceID,
				},
			},
		})
	}

	//hatchConfig.Logger.Printf("volumes configured")

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

	if mountSharedMemory {
		volumeMounts = append(volumeMounts, k8sv1.VolumeMount{
			MountPath: "/dev/shm",
			Name:      "dshm",
		})
	}

	pod = &k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        workspaceID,
			Namespace:   hatchConfig.Config.UserNamespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: k8sv1.PodSpec{
			SecurityContext: &securityContext,
			InitContainers:  []k8sv1.Container{},
			Containers: []k8sv1.Container{
				{
					Name:  "fuse-container",
					Image: hatchConfig.Config.Sidecar.Image,
					SecurityContext: &k8sv1.SecurityContext{
						Privileged: &trueVal,
						RunAsUser:  &sideCarRunAsUser,
						RunAsGroup: &sideCarRunAsGroup,
					},
					ImagePullPolicy: k8sv1.PullPolicy(k8sv1.PullAlways), //TODO: make this configurable
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
	//			"role": "jupyter",  //TODO: need a 'node selector' config, so this isn't hard coded
			},
			Tolerations: []k8sv1.Toleration{{Key: "role", Operator: "Equal", Value: "jupyter", Effect: "NoSchedule", TolerationSeconds: nil}},
			Volumes:     volumes,
		},
	}

	// some pods (ex - dockstore apps) only have "Friend" containers
	if "" != hatchApp.Image {
		var volumeMounts = []k8sv1.VolumeMount{
			{
				MountPath:        "/data",
				Name:             "shared-data",
				MountPropagation: &hostToContainer,
			},
		}

		if "" != hatchApp.UserVolumeLocation {
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
	//hatchConfig.Logger.Printf("friends added")
	return pod, nil
}

func payModelExistsForUser(userName string) (result bool) {
	result = false
	for _, paymodel := range Config.PayModelMap {
		if paymodel.User == userName {
			result = true
		}
	}
	return result
}


func createK8sPod(ctx context.Context, appID string, accessToken string, userName string) error {
	if payModelExistsForUser(userName) {
		return createExternalK8sPod(ctx, appID, accessToken, userName)
	} else {
		return createLocalK8sPod(ctx, appID, accessToken, userName)
	}
}


func createLocalK8sPod(ctx context.Context, appID string, accessToken string, userName string) error {
	hatchApp, ok := Config.ContainersMap[appID]
	if !ok {
		return fmt.Errorf("Container %s not found", appID)
	}
	var extraVars []k8sv1.EnvVar

	workspaceID := getBaseName(userName, appID)

	pod, err := buildPod(Config, &hatchApp, userName, appID, extraVars)
	if err != nil {
		Config.Logger.Printf("Failed to configure pod for launch for user %v, Error: %v", userName, err)
		return err
	}
	//podName := userToResourceName(baseName, "pod")
	podClient, _, err := getPodClient(ctx, userName)
	if err != nil {
		Config.Logger.Panicf("Error in createLocalK8sPod: %v", err)
		return err
	}
	// a null image indicates a dockstore app - always mount user volume
	mountUserVolume := hatchApp.UserVolumeLocation != ""
	if mountUserVolume {
		//claimName := userToResourceName(baseName, "claim")

		_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Get(ctx, workspaceID, metav1.GetOptions{})
		if err != nil {
			Config.Logger.Printf("Creating PersistentVolumeClaim %s.\n", workspaceID)
			pvc := &k8sv1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:        workspaceID,
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
				Config.Logger.Printf("Failed to create PVC %s. Error: %s\n", workspaceID, err)
				return err
			}
		}
	}

	_, err = podClient.Pods(Config.Config.UserNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		Config.Logger.Printf("Failed to launch pod %s for user %s. Image: %s, CPU %s, Memory %s. Error: %s\n", hatchApp.Name, userName, hatchApp.Image, hatchApp.CPULimit, hatchApp.MemoryLimit, err)
		return err
	}

	Config.Logger.Printf("Launched pod %s for user %s. Image: %s, CPU %s, Memory %s\n", hatchApp.Name, userName, hatchApp.Image, hatchApp.CPULimit, hatchApp.MemoryLimit)

	//serviceName := userToResourceName(baseName, "service")
	labelsService := make(map[string]string)
	labelsService[LABEL_POD] = workspaceID
	labelsService[LABEL_USER] = escapism(userName)
	labelsService[LABEL_APPID] = appID

	_, err = podClient.Services(Config.Config.UserNamespace).Get(ctx, workspaceID, metav1.GetOptions{})
	if err == nil {
		// This probably happened as the result of some error... there was no pod but was a service
		// Lets just clean it up and proceed
		policy := metav1.DeletePropagationBackground
		deleteOptions := metav1.DeleteOptions{
			PropagationPolicy: &policy,
		}
		podClient.Services(Config.Config.UserNamespace).Delete(ctx, workspaceID, deleteOptions)
	}

	service := &k8sv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        workspaceID,
			Namespace:   Config.Config.UserNamespace,
			Labels:      labelsService,
			Annotations: make(map[string]string),
		},
		Spec: k8sv1.ServiceSpec{
			Type:     k8sv1.ServiceTypeClusterIP,
			Selector: map[string]string{LABEL_POD: workspaceID},
			Ports: []k8sv1.ServicePort{
				{
					Name:     workspaceID,
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

	smap, err := Config.Config.GetServiceMapper()
	if err != nil {
		fmt.Printf("Failed getting service mapper: %s\n", err)
		return err
	}
	err = smap.Start(Config.Config.UserNamespace, userName, appID, hatchApp.PathRewrite, hatchApp.UseTLS, service )
	if err != nil {
		fmt.Printf("Failed set up mapping: %s\n", err)
		return err
	}

	_, err = podClient.Services(Config.Config.UserNamespace).Create(ctx, service, metav1.CreateOptions{})
	if err != nil {
		fmt.Printf("Failed to launch service %s for user %s forwarding port %d. Error: %s\n", workspaceID, userName, hatchApp.TargetPort, err)
		return err
	}

	fmt.Printf("Launched service %s for user %s forwarding port %d\n", workspaceID, userName, hatchApp.TargetPort)

	return nil
}
