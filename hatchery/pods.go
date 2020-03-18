package hatchery

import (
	"fmt"
	"math/rand"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
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

type WorkspaceStatus struct {
	Status string `json:"status"`
}

func getPodClient() corev1.CoreV1Interface {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	// Access jobs. We can't do it all in one line, since we need to receive the
	// errors and manage thgem appropriately
	podClient := clientset.CoreV1()
	return podClient
}

func statusK8sPod(userName string) (*WorkspaceStatus, error) {
	podClient := getPodClient()

	safeUserName := escapism(userName)

	status := WorkspaceStatus{}

	podName := fmt.Sprintf("hatchery-%s", safeUserName)
	pod, err := podClient.Pods(Config.Config.UserNamespace).Get(podName, metav1.GetOptions{})
	if err != nil {
		// not found
		status.Status = "Not Found"
		return &status, nil
	}

	if pod.DeletionTimestamp != nil {
		status.Status = "Terminating"
		return &status, nil
	}

	switch pod.Status.Phase {
	case "Failed":
	case "Succeeded":
	case "Unknown":
		status.Status = "Stopped"
		return &status, nil
	case "Pending":
		status.Status = "Launching"
		return &status, nil
	case "Running":
		break
	default:
		fmt.Printf("Unknown pod status for %s: %s\n", podName, string(pod.Status.Phase))
	}

	var allReady = true
	for _, v := range pod.Status.Conditions {
		if v.Type == "Ready" {
			if v.Status != "True" {
				allReady = false
			}
		} else if v.Type == "PodScheduled" {
			if v.Status != "True" {
				allReady = false
			}
		}
	}

	if allReady == true {
		status.Status = "Running"
		return &status, nil
	} else {
		status.Status = "Launching"
		return &status, nil
	}

}

func deleteK8sPod(userName string) error {
	podClient := getPodClient()

	policy := metav1.DeletePropagationBackground
	var grace int64 = 20
	deleteOptions := &metav1.DeleteOptions{
		PropagationPolicy:  &policy,
		GracePeriodSeconds: &grace,
	}

	safeUserName := escapism(userName)

	podName := fmt.Sprintf("hatchery-%s", safeUserName)
	_, err := podClient.Pods(Config.Config.UserNamespace).Get(podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("A workspace pod was not found: %s", err)
	}
	fmt.Printf("Attempting to delete pod %s for user %s\n", podName, userName)
	podClient.Pods(Config.Config.UserNamespace).Delete(podName, deleteOptions)

	serviceName := fmt.Sprintf("h-%s-s", safeUserName)
	_, err = podClient.Services(Config.Config.UserNamespace).Get(serviceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("A workspace service was not found: %s", err)
	}
	fmt.Printf("Attempting to delete service %s for user %s\n", serviceName, userName)
	podClient.Services(Config.Config.UserNamespace).Delete(serviceName, deleteOptions)

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
func buildPod(hatchConfig *FullHatcheryConfig, hatchApp *Container, userName string) (pod *k8sv1.Pod, err error) {
	podName := userToResourceName(userName, "pod")
	labels := make(map[string]string)
	labels["app"] = podName
	annotations := make(map[string]string)
	annotations["gen3username"] = userName
	var sideCarRunAsUser int64 = 0
	var sideCarRunAsGroup int64 = 0
	var hostToContainer = k8sv1.MountPropagationHostToContainer
	var bidirectional = k8sv1.MountPropagationBidirectional
	var envVars []k8sv1.EnvVar
	// a null image indicates a dockstore app - always mount user volume
	mountUserVolume := hatchApp.UserVolumeLocation != "" || hatchApp.Image == ""

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

	var volumes = []k8sv1.Volume{
		{
			Name:         "shared-data",
			VolumeSource: k8sv1.VolumeSource{},
		},
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

	pod = &k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        podName,
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
					ImagePullPolicy: k8sv1.PullPolicy(k8sv1.PullAlways),
					Env:             sidecarEnvVars,
					Command:         hatchConfig.Config.Sidecar.Command,
					Args:            hatchConfig.Config.Sidecar.Args,
					VolumeMounts: []k8sv1.VolumeMount{
						{
							MountPath:        "/data",
							Name:             "shared-data",
							MountPropagation: &bidirectional,
						},
					},
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
	if "" != hatchApp.Image {
		var volumeMounts = []k8sv1.VolumeMount{
			{
				MountPath:        "/data",
				Name:             "shared-data",
				MountPropagation: &hostToContainer,
			},
		}

		if mountUserVolume {
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

func createK8sPod(hash string, accessToken string, userName string) error {
	hatchApp := Config.ContainersMap[hash]
	pod, err := buildPod(Config, &hatchApp, userName)
	if err != nil {
		Config.Logger.Printf("Failed to configure pod for launch for user %v, Error: %v", userName, err)
		return err
	}
	podName := userToResourceName(userName, "pod")
	podClient := getPodClient()
	// a null image indicates a dockstore app - always mount user volume
	mountUserVolume := hatchApp.UserVolumeLocation != "" || hatchApp.Image == ""

	if mountUserVolume {
		claimName := userToResourceName(userName, "claim")

		_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Get(claimName, metav1.GetOptions{})
		if err != nil {
			Config.Logger.Printf("Creating PersistentVolumeClaim %s.\n", claimName)
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
			_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Create(pvc)
			if err != nil {
				Config.Logger.Printf("Failed to create PVC %s. Error: %s\n", claimName, err)
				return err
			}
		}
	}

	_, err = podClient.Pods(Config.Config.UserNamespace).Create(pod)
	if err != nil {
		Config.Logger.Printf("Failed to launch pod %s for user %s. Image: %s, CPU %s, Memory %s. Error: %s\n", hatchApp.Name, userName, hatchApp.Image, hatchApp.CPULimit, hatchApp.MemoryLimit, err)
		return err
	}

	Config.Logger.Printf("Launched pod %s for user %s. Image: %s, CPU %s, Memory %s\n", hatchApp.Name, userName, hatchApp.Image, hatchApp.CPULimit, hatchApp.MemoryLimit)

	serviceName := userToResourceName(userName, "service")
	labelsService := make(map[string]string)
	labelsService["app"] = podName
	annotationsService := make(map[string]string)
	annotationsService["getambassador.io/config"] = fmt.Sprintf(ambassadorYaml, userToResourceName(userName, "mapping"), userName, serviceName, Config.Config.UserNamespace, hatchApp.PathRewrite, hatchApp.UseTLS)

	_, err = podClient.Services(Config.Config.UserNamespace).Get(serviceName, metav1.GetOptions{})
	if err == nil {
		// This probably happened as the result of some error... there was no pod but was a service
		// Lets just clean it up and proceed
		policy := metav1.DeletePropagationBackground
		deleteOptions := &metav1.DeleteOptions{
			PropagationPolicy: &policy,
		}
		podClient.Services(Config.Config.UserNamespace).Delete(serviceName, deleteOptions)
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

	_, err = podClient.Services(Config.Config.UserNamespace).Create(service)
	if err != nil {
		fmt.Printf("Failed to launch service %s for user %s forwarding port %d. Error: %s\n", serviceName, userName, hatchApp.TargetPort, err)
		return err
	}

	fmt.Printf("Launched service %s for user %s forwarding port %d\n", serviceName, userName, hatchApp.TargetPort)

	return nil
}

// GetRandString returns a random string of lenght N
func GetRandString(n int) string {
	letterBytes := "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}
	return string(b)
}

// Escapism escapes characters not allowed into hex with -
func escapism(input string) string {
	safeBytes := "abcdefghijklmnopqrstuvwxyz0123456789"
	var escaped string
	for _, v := range input {
		if !characterInString(v, safeBytes) {
			hexCode := fmt.Sprintf("%2x", v)
			escaped += "-" + hexCode
		} else {
			escaped += string(v)
		}
	}
	return escaped
}

func characterInString(a rune, list string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}
