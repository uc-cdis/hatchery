package handlers

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
name:  %s-mapping
prefix: /
headers:
  remote_user: %s
service: h-%s-s.%s.svc.cluster.local:80
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
		fmt.Printf("Conditions are %s, %s\n", v.Type, v.Status)
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
		PropagationPolicy: &policy,
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

func createK8sPod(hash string, accessToken string, userName string) error {
	containerSettings := Config.ContainersMap[hash]

	podClient := getPodClient()
	safeUserName := escapism(userName)
	name := fmt.Sprintf("hatchery-%s", safeUserName)
	labels := make(map[string]string)
	labels["app"] = name
	annotations := make(map[string]string)
	annotations["gen3username"] = userName
	var sideCarRunAsUser int64 = 0
	var sideCarRunAsGroup int64 = 0
	var hostToContainer k8sv1.MountPropagationMode = k8sv1.MountPropagationHostToContainer
	var bidirectional k8sv1.MountPropagationMode = k8sv1.MountPropagationBidirectional

	_, err := podClient.Pods(Config.Config.UserNamespace).Get(name, metav1.GetOptions{})
	if err == nil {
		return fmt.Errorf("A workspace is already running")
	}

	var envVars []k8sv1.EnvVar
	for key, value := range containerSettings.Env {
		envVar := k8sv1.EnvVar{
			Name:  key,
			Value: value,
		}
		envVars = append(envVars, envVar)
	}

	var sidecarEnvVars []k8sv1.EnvVar
	for key, value := range Config.Config.Sidecar.Env {
		envVar := k8sv1.EnvVar{
			Name:  key,
			Value: value,
		}
		sidecarEnvVars = append(sidecarEnvVars, envVar)
	}

	var lifeCycle = k8sv1.Lifecycle{}
	if containerSettings.LifecyclePreStop != nil && len(containerSettings.LifecyclePreStop) > 0 {
		lifeCycle.PreStop = &k8sv1.Handler{
			Exec: &k8sv1.ExecAction{
				Command: containerSettings.LifecyclePreStop,
			},
		}
	}
	if containerSettings.LifecyclePostStart != nil && len(containerSettings.LifecyclePostStart) > 0 {
		lifeCycle.PostStart = &k8sv1.Handler{
			Exec: &k8sv1.ExecAction{
				Command: containerSettings.LifecyclePostStart,
			},
		}
	}

	var securityContext = k8sv1.PodSecurityContext{}

	if containerSettings.UserUID != 0 {
		securityContext.RunAsUser = &containerSettings.UserUID
	}
	if containerSettings.UserUID != 0 {
		securityContext.RunAsGroup = &containerSettings.GroupUID
	}
	if containerSettings.FSGID != 0 {
		securityContext.FSGroup = &containerSettings.FSGID
	}

	var volumes = []k8sv1.Volume{
		{
			Name:         "shared-data",
			VolumeSource: k8sv1.VolumeSource{},
		},
	}

	var volumeMounts = []k8sv1.VolumeMount{
		{
			MountPath:        "/data",
			Name:             "shared-data",
			MountPropagation: &hostToContainer,
		},
	}

	if containerSettings.UserVolumeLocation != "" {
		var claimName = fmt.Sprintf("claim-%s", safeUserName)

		_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Get(claimName, metav1.GetOptions{})
		if err != nil {
			fmt.Printf("Creating PersistentVolumeClaim %s.\n", claimName)
			pvc := &k8sv1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: claimName,
					Annotations: annotations,
					Labels: labels,
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
				fmt.Printf("Failed to create PVC %s. Error: %s\n", claimName, err)
				return err
			}
		}

		volumes = append(volumes, k8sv1.Volume{
			Name: "user-data",
			VolumeSource: k8sv1.VolumeSource{
				PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
					ClaimName: claimName,
				},
			},
		})

		volumeMounts = append(volumeMounts, k8sv1.VolumeMount{
				MountPath:        containerSettings.UserVolumeLocation,
				Name:             "user-data",
		})
		
	}

	pod := &k8sv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   Config.Config.UserNamespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: k8sv1.PodSpec{
			SecurityContext: &securityContext,
			InitContainers: []k8sv1.Container{},
			Containers: []k8sv1.Container{
				{
					Name:  "hatchery-container",
					Image: containerSettings.Image,
					SecurityContext: &k8sv1.SecurityContext{
						Privileged: &falseVal,
					},
					ImagePullPolicy: k8sv1.PullPolicy(k8sv1.PullIfNotPresent),
					Env:             envVars,
					Command:         containerSettings.Command,
					Args:            containerSettings.Args,
					VolumeMounts:    volumeMounts,
					Resources: k8sv1.ResourceRequirements{
						Limits: k8sv1.ResourceList{
							k8sv1.ResourceCPU:    resource.MustParse(containerSettings.CPULimit),
							k8sv1.ResourceMemory: resource.MustParse(containerSettings.MemoryLimit),
						},
						Requests: k8sv1.ResourceList{
							k8sv1.ResourceCPU:    resource.MustParse(containerSettings.CPULimit),
							k8sv1.ResourceMemory: resource.MustParse(containerSettings.MemoryLimit),
						},
					},
					Lifecycle: &lifeCycle,
					ReadinessProbe: &k8sv1.Probe{
						Handler: k8sv1.Handler{
							HTTPGet: &k8sv1.HTTPGetAction{
								Path: containerSettings.ReadyProbe,
								Port: intstr.FromInt(int(containerSettings.TargetPort)),
							},
						},
					},
				},
				{
					Name:  "fuse-container",
					Image: Config.Config.Sidecar.Image,
					SecurityContext: &k8sv1.SecurityContext{
						Privileged: &trueVal,
						RunAsUser:  &sideCarRunAsUser,
						RunAsGroup: &sideCarRunAsGroup,
					},
					ImagePullPolicy: k8sv1.PullPolicy(k8sv1.PullAlways),
					Env:             sidecarEnvVars,
					Command:         Config.Config.Sidecar.Command,
					Args:            Config.Config.Sidecar.Args,
					VolumeMounts: []k8sv1.VolumeMount{
						{
							MountPath:        "/data",
							Name:             "shared-data",
							MountPropagation: &bidirectional,
						},
					},
					Resources: k8sv1.ResourceRequirements{
						Limits: k8sv1.ResourceList{
							k8sv1.ResourceCPU:    resource.MustParse(Config.Config.Sidecar.CPULimit),
							k8sv1.ResourceMemory: resource.MustParse(Config.Config.Sidecar.MemoryLimit),
						},
						Requests: k8sv1.ResourceList{
							k8sv1.ResourceCPU:    resource.MustParse(Config.Config.Sidecar.CPULimit),
							k8sv1.ResourceMemory: resource.MustParse(Config.Config.Sidecar.MemoryLimit),
						},
					},
					Lifecycle: &k8sv1.Lifecycle{
						PreStop: &k8sv1.Handler{
							Exec: &k8sv1.ExecAction{
								Command: Config.Config.Sidecar.LifecyclePreStop,
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
			Volumes: volumes,
		},
	}
	_, err = podClient.Pods(Config.Config.UserNamespace).Create(pod)
	if err != nil {
		fmt.Printf("Failed to launch pod %s for user %s. Image: %s, CPU %s, Memory %s. Error: %s\n", name, userName, containerSettings.Image, containerSettings.CPULimit, containerSettings.MemoryLimit, err)
		return err
	}

	fmt.Printf("Launched pod %s for user %s. Image: %s, CPU %s, Memory %s\n", name, userName, containerSettings.Image, containerSettings.CPULimit, containerSettings.MemoryLimit)

	labelsService := make(map[string]string)
	labels["app"] = name
	annotationsService := make(map[string]string)
	annotationsService["getambassador.io/config"] = fmt.Sprintf(ambassadorYaml, safeUserName, userName, safeUserName, Config.Config.UserNamespace, containerSettings.PathRewrite, containerSettings.UseTLS)
	serviceName := fmt.Sprintf("h-%s-s", safeUserName)

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
			Selector: map[string]string{"app": name},
			Ports: []k8sv1.ServicePort{
				{
					Name:     name,
					Protocol: k8sv1.ProtocolTCP,
					Port:     80,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: containerSettings.TargetPort,
					},
				},
			},
		},
	}

	_, err = podClient.Services(Config.Config.UserNamespace).Create(service)
	if err != nil {
		fmt.Printf("Failed to launch service %s for user %s forwarding port %d. Error: %s\n", serviceName, userName, containerSettings.TargetPort, err)
		return err
	}

	fmt.Printf("Launched service %s for user %s forwarding port %d\n", serviceName, userName, containerSettings.TargetPort)

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
