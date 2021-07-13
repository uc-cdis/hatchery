package hatchery

import (
	"context"
	"encoding/base64"
	"fmt"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	// AWS modules
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/eks"

	"sigs.k8s.io/aws-iam-authenticator/pkg/token"
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
	Status          string            `json:"status"`
	Conditions      []PodConditions   `json:"conditions"`
	ContainerStates []ContainerStates `json:"containerStates"`
}

func getPodClient(userName string) (corev1.CoreV1Interface, error) {
	if payModelExistsForUser(userName) {
		podClient, err := NewEKSClientset(userName)
		if err != nil {
			Config.Logger.Printf("Error fetching EKS kubeconfig: %v", err)
			return nil, err
		} else {
			return podClient, nil
		}
	} else {
		return getLocalPodClient(), nil
	}
}

func getLocalPodClient() corev1.CoreV1Interface {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	// Access jobs. We can't do it all in one line, since we need to receive the
	// errors and manage them appropriately
	podClient := clientset.CoreV1()
	return podClient
}

// Generate EKS kubeconfig using AWS role
func NewEKSClientset(userName string /*cluster *eks.Cluster, roleARN string*/) (corev1.CoreV1Interface, error) {
	pm := Config.PayModelMap[userName]
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String(pm.Region),
	}))

	creds := stscreds.NewCredentials(sess, roleARN)
	eksSvc := eks.New(sess, &aws.Config{Credentials: creds})
	input := &eks.DescribeClusterInput{
		Name: aws.String(pm.Name),
	}
	result, err := eksSvc.DescribeCluster(input)
	if err != nil {
		Config.Logger.Printf("Error calling DescribeCluster: %v", err)
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

func podStatus(userName string) (*WorkspaceStatus, error) {

	status := WorkspaceStatus{}
	podClient, err := getPodClient(userName)
	if err != nil {
		// Config.Logger.Panic("Error trying to fetch kubeConfig: %v", err)
		status.Status = fmt.Sprintf("%v", err)
		return &status, err
	}

	podName := userToResourceName(userName, "pod")

	serviceName := userToResourceName(userName, "service")

	pod, err := podClient.Pods(Config.Config.UserNamespace).Get(context.TODO(), podName, metav1.GetOptions{})
	_, serviceErr := podClient.Services(Config.Config.UserNamespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err != nil {
		if serviceErr == nil {
			Config.Logger.Printf("Pod has been terminated, but service is still being terminated. Wait for service to be killed. %v", serviceErr)
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

func statusK8sPod(userName string) (*WorkspaceStatus, error) {
	status, err := podStatus(userName)
	if err != nil {
		status.Status = fmt.Sprintf("%v", err)
		Config.Logger.Printf("Error getting status: %v", err)
	}
	return status, nil
}

func deleteK8sPod(userName string) error {
	podClient, err := getPodClient(userName)
	if err != nil {
		return err
	}

	policy := metav1.DeletePropagationBackground
	var grace int64 = 20
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy:  &policy,
		GracePeriodSeconds: &grace,
	}

	safeUserName := escapism(userName)

	podName := fmt.Sprintf("hatchery-%s", safeUserName)
	_, err = podClient.Pods(Config.Config.UserNamespace).Get(context.TODO(), podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("A workspace pod was not found: %s", err)
	}
	fmt.Printf("Attempting to delete pod %s for user %s\n", podName, userName)
	podClient.Pods(Config.Config.UserNamespace).Delete(context.TODO(), podName, deleteOptions)

	serviceName := fmt.Sprintf("h-%s-s", safeUserName)
	_, err = podClient.Services(Config.Config.UserNamespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("A workspace service was not found: %s", err)
	}
	fmt.Printf("Attempting to delete service %s for user %s\n", serviceName, userName)
	podClient.Services(Config.Config.UserNamespace).Delete(context.TODO(), serviceName, deleteOptions)

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

func scaleEKSNodes(userName string, scale int) {
	pm := Config.PayModelMap[userName]
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String(pm.Region),
	}))

	creds := stscreds.NewCredentials(sess, roleARN)
	// ASG stuff
	asgSvc := autoscaling.New(sess, &aws.Config{Credentials: creds})

	asgInput := &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []*string{aws.String("eks-jupyterworker-node-" + pm.Name)},
	}
	asg, err := asgSvc.DescribeAutoScalingGroups(asgInput)
	cap := *asg.AutoScalingGroups[0].DesiredCapacity
	Config.Logger.Printf("ASG capacity: %d", cap)

	Config.Logger.Printf("Scaling ASG from %d to %d..", cap, cap+1)

	asgScaleInput := &autoscaling.SetDesiredCapacityInput{
		AutoScalingGroupName: asg.AutoScalingGroups[0].AutoScalingGroupName,
		DesiredCapacity:      aws.Int64(cap + int64(scale)),
	}
	_, err = asgSvc.SetDesiredCapacity(asgScaleInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case autoscaling.ErrCodeScalingActivityInProgressFault:
				Config.Logger.Println(autoscaling.ErrCodeScalingActivityInProgressFault, aerr.Error())
			case autoscaling.ErrCodeResourceContentionFault:
				Config.Logger.Println(autoscaling.ErrCodeResourceContentionFault, aerr.Error())
			default:
				Config.Logger.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			Config.Logger.Println(err.Error())
		}
	}
}

func createK8sPod(hash string, accessToken string, userName string) error {
	if payModelExistsForUser(userName) {
		return createExternalK8sPod(hash, accessToken, userName)
	} else {
		return createLocalK8sPod(hash, accessToken, userName)
	}
}

func createLocalK8sPod(hash string, accessToken string, userName string) error {
	hatchApp := Config.ContainersMap[hash]

	var extraVars []k8sv1.EnvVar
	pod, err := buildPod(Config, &hatchApp, userName, extraVars)
	if err != nil {
		Config.Logger.Printf("Failed to configure pod for launch for user %v, Error: %v", userName, err)
		return err
	}
	podName := userToResourceName(userName, "pod")
	podClient, err := getPodClient(userName)
	if err != nil {
		Config.Logger.Panic("Error in createLocalK8sPod: %v", err)
		return err
	}
	// a null image indicates a dockstore app - always mount user volume
	mountUserVolume := hatchApp.UserVolumeLocation != ""
	if mountUserVolume {
		claimName := userToResourceName(userName, "claim")

		_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Get(context.TODO(), claimName, metav1.GetOptions{})
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
			_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Create(context.TODO(), pvc, metav1.CreateOptions{})
			if err != nil {
				Config.Logger.Printf("Failed to create PVC %s. Error: %s\n", claimName, err)
				return err
			}
		}
	}

	_, err = podClient.Pods(Config.Config.UserNamespace).Create(context.TODO(), pod, metav1.CreateOptions{})
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

	_, err = podClient.Services(Config.Config.UserNamespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err == nil {
		// This probably happened as the result of some error... there was no pod but was a service
		// Lets just clean it up and proceed
		policy := metav1.DeletePropagationBackground
		deleteOptions := metav1.DeleteOptions{
			PropagationPolicy: &policy,
		}
		podClient.Services(Config.Config.UserNamespace).Delete(context.TODO(), serviceName, deleteOptions)
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

	_, err = podClient.Services(Config.Config.UserNamespace).Create(context.TODO(), service, metav1.CreateOptions{})
	if err != nil {
		fmt.Printf("Failed to launch service %s for user %s forwarding port %d. Error: %s\n", serviceName, userName, hatchApp.TargetPort, err)
		return err
	}

	fmt.Printf("Launched service %s for user %s forwarding port %d\n", serviceName, userName, hatchApp.TargetPort)

	return nil
}

func createExternalK8sPod(hash string, accessToken string, userName string) error {
	hatchApp := Config.ContainersMap[hash]

	podClient, err := NewEKSClientset(userName)

	// Check if NS exists in external cluster, if not create it.
	ns, err := podClient.Namespaces().Get(context.TODO(), Config.Config.UserNamespace, metav1.GetOptions{})
	if err != nil {
		nsName := &k8sv1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: Config.Config.UserNamespace,
			},
		}
		Config.Logger.Printf("Namespace created: %v", ns)
		podClient.Namespaces().Create(context.Background(), nsName, metav1.CreateOptions{})
	}
	var extraVars []k8sv1.EnvVar

	extraVars = append(extraVars, k8sv1.EnvVar{
		Name:  "WTS_OVERRIDE_URL",
		Value: "https://" + Config.Config.Sidecar.Env["HOSTNAME"] + "/wts",
	})
	extraVars = append(extraVars, k8sv1.EnvVar{
		Name:  "ACCESS_TOKEN",
		Value: accessToken,
	})

	pod, err := buildPod(Config, &hatchApp, userName, extraVars)
	if err != nil {
		Config.Logger.Printf("Failed to configure pod for launch for user %v, Error: %v", userName, err)
		return err
	}
	podName := userToResourceName(userName, "pod")
	// a null image indicates a dockstore app - always mount user volume
	mountUserVolume := hatchApp.UserVolumeLocation != ""
	if mountUserVolume {
		claimName := userToResourceName(userName, "claim")

		_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Get(context.TODO(), claimName, metav1.GetOptions{})
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

			_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Create(context.TODO(), pvc, metav1.CreateOptions{})
			if err != nil {
				Config.Logger.Printf("Failed to create PVC %s. Error: %s\n", claimName, err)
				return err
			}
		}
	}

	_, err = podClient.Pods(Config.Config.UserNamespace).Create(context.TODO(), pod, metav1.CreateOptions{})
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

	_, err = podClient.Services(Config.Config.UserNamespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err == nil {
		// This probably happened as the result of some error... there was no pod but was a service
		// Lets just clean it up and proceed
		policy := metav1.DeletePropagationBackground
		deleteOptions := metav1.DeleteOptions{
			PropagationPolicy: &policy,
		}
		podClient.Services(Config.Config.UserNamespace).Delete(context.TODO(), serviceName, deleteOptions)

	}

	service := &k8sv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        serviceName,
			Namespace:   Config.Config.UserNamespace,
			Labels:      labelsService,
			Annotations: annotationsService,
		},
		Spec: k8sv1.ServiceSpec{
			Type:     k8sv1.ServiceTypeLoadBalancer,
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

	_, err = podClient.Services(Config.Config.UserNamespace).Create(context.TODO(), service, metav1.CreateOptions{})
	if err != nil {
		fmt.Printf("Failed to launch service %s for user %s forwarding port %d. Error: %s\n", serviceName, userName, hatchApp.TargetPort, err)
		return err
	}

	Config.Logger.Printf("Launched service %s for user %s forwarding port %d\n", serviceName, userName, hatchApp.TargetPort)

	createLocalService(userName, hash)
	scaleEKSNodes(userName, 0)

	return nil
}

// Creates a local service that portal can reach
// and route traffic to pod in external cluster.
func createLocalService(userName string, hash string) error {
	const localAmbassadorYaml = `---
apiVersion: ambassador/v1
kind:  Mapping
name:  %s
prefix: /
headers:
  remote_user: %s
service: %s:80
bypass_auth: true
timeout_ms: 300000
use_websocket: true
rewrite: %s
tls: %s
`
	hatchApp := Config.ContainersMap[hash]
	localPodClient := getLocalPodClient()
	externalPodClient, err := NewEKSClientset(userName)
	serviceName := userToResourceName(userName, "service")
	podName := userToResourceName(userName, "pod")
	service, err := externalPodClient.Services(Config.Config.UserNamespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err != nil {
		Config.Logger.Printf("Failed to find external service %+v", service)
	}
	LoadBalancer := service.Status.LoadBalancer.Ingress[0].Hostname

	labelsService := make(map[string]string)
	labelsService["app"] = podName
	annotationsService := make(map[string]string)
	annotationsService["getambassador.io/config"] = fmt.Sprintf(localAmbassadorYaml, userToResourceName(userName, "mapping"), userName, LoadBalancer, hatchApp.PathRewrite, hatchApp.UseTLS)

	_, err = localPodClient.Services(Config.Config.UserNamespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
	if err == nil {
		// This probably happened as the result of some error... there was no pod but was a service
		// Lets just clean it up and proceed
		policy := metav1.DeletePropagationBackground
		deleteOptions := metav1.DeleteOptions{
			PropagationPolicy: &policy,
		}
		localPodClient.Services(Config.Config.UserNamespace).Delete(context.TODO(), serviceName, deleteOptions)

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

	_, err = localPodClient.Services(Config.Config.UserNamespace).Create(context.TODO(), localService, metav1.CreateOptions{})
	if err != nil {
		fmt.Printf("Failed to launch local service %s for user %s forwarding port %d. Error: %s\n", serviceName, userName, hatchApp.TargetPort, err)
		return err
	}

	Config.Logger.Printf("Launched local service %s for user %s forwarding port %d\n", serviceName, userName, hatchApp.TargetPort)
	return nil
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
