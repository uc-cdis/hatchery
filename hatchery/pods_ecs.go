package hatchery

import (
  "os"
  "fmt"
  "context"
  "encoding/base64"

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

  awstrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/aws/aws-sdk-go/aws"
)

// Generate EKS kubeconfig using AWS role
func NewEKSClientset(ctx context.Context, userName string /*cluster *eks.Cluster, roleARN string*/) (corev1.CoreV1Interface, error) {
	pm := Config.PayModelMap[userName]
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := awstrace.WrapSession(session.Must(session.NewSession(&aws.Config{
		Region: aws.String(pm.Region),
	})))

	creds := stscreds.NewCredentials(sess, roleARN)
	eksSvc := eks.New(sess, &aws.Config{Credentials: creds})
	input := &eks.DescribeClusterInput{
		Name: aws.String(pm.Name),
	}
	result, err := eksSvc.DescribeClusterWithContext(ctx, input)
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


func scaleEKSNodes(ctx context.Context, userName string, scale int) {
	pm := Config.PayModelMap[userName]
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := awstrace.WrapSession(session.Must(session.NewSession(&aws.Config{
		Region: aws.String(pm.Region),
	})))

	creds := stscreds.NewCredentials(sess, roleARN)
	// ASG stuff
	asgSvc := autoscaling.New(sess, &aws.Config{Credentials: creds})

	asgInput := &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []*string{aws.String("eks-jupyterworker-node-" + pm.Name)},
	}
	asg, err := asgSvc.DescribeAutoScalingGroupsWithContext(ctx, asgInput)
	cap := *asg.AutoScalingGroups[0].DesiredCapacity
	Config.Logger.Printf("ASG capacity: %d", cap)

	Config.Logger.Printf("Scaling ASG from %d to %d..", cap, cap+1)

	asgScaleInput := &autoscaling.SetDesiredCapacityInput{
		AutoScalingGroupName: asg.AutoScalingGroups[0].AutoScalingGroupName,
		DesiredCapacity:      aws.Int64(cap + int64(scale)),
	}
	_, err = asgSvc.SetDesiredCapacityWithContext(ctx, asgScaleInput)
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


func createExternalK8sPod(ctx context.Context, hash string, accessToken string, userName string) error {
	hatchApp, ok := Config.ContainersMap[hash]
	if !ok {
		return fmt.Errorf("Container %s not found", hash)
	}

	podClient, err := NewEKSClientset(ctx, userName)
	if err != nil {
		Config.Logger.Printf("Failed to create pod client for user %v, Error: %v", userName, err)
		return err
	}

	apiKey, err := getAPIKeyWithContext(ctx, accessToken)
	if err != nil {
		Config.Logger.Printf("Failed to get API key for user %v, Error: %v", userName, err)
		return err
	}
	Config.Logger.Printf("Created API key for user %v, key ID: %v", userName, apiKey.KeyID)

	// Check if NS exists in external cluster, if not create it.
	ns, err := podClient.Namespaces().Get(ctx, Config.Config.UserNamespace, metav1.GetOptions{})
	if err != nil {
		nsName := &k8sv1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: Config.Config.UserNamespace,
			},
		}
		Config.Logger.Printf("Namespace created: %v", ns)
		podClient.Namespaces().Create(ctx, nsName, metav1.CreateOptions{})
	}

	var extraVars []k8sv1.EnvVar

	extraVars = append(extraVars, k8sv1.EnvVar{
		Name:  "WTS_OVERRIDE_URL",
		Value: "https://" + os.Getenv("HOSTNAME") + "/wts",
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

	baseName := getBaseName(userName, hash)
	pod, err := buildPod(Config, &hatchApp, baseName, hash, userName, extraVars)
	if err != nil {
		Config.Logger.Printf("Failed to configure pod for launch for user %v, Error: %v", userName, err)
		return err
	}
	podName := userToResourceName(baseName, "pod")
	// a null image indicates a dockstore app - always mount user volume
	mountUserVolume := hatchApp.UserVolumeLocation != ""
	if mountUserVolume {
		claimName := userToResourceName(baseName, "claim")

		_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Get(ctx, claimName, metav1.GetOptions{})
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

			_, err := podClient.PersistentVolumeClaims(Config.Config.UserNamespace).Create(ctx, pvc, metav1.CreateOptions{})
			if err != nil {
				Config.Logger.Printf("Failed to create PVC %s. Error: %s\n", claimName, err)
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

	serviceName := userToResourceName(baseName, "service")
	labelsService := make(map[string]string)
	labelsService[LABEL_POD] = podName
	labelsService[LABEL_USER] = escapism(userName)
	labelsService[LABEL_APPID] = hash
	annotationsService := make(map[string]string)
	//annotationsService["getambassador.io/config"] = fmt.Sprintf(ambassadorYaml, userToResourceName(userName, "mapping"), userName, serviceName, Config.Config.UserNamespace, hatchApp.PathRewrite, hatchApp.UseTLS)
	annotationsService["service.beta.kubernetes.io/aws-load-balancer-internal"] = "true"
	_, err = podClient.Services(Config.Config.UserNamespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err == nil {
		// This probably happened as the result of some error... there was no pod but was a service
		// Lets just clean it up and proceed
		policy := metav1.DeletePropagationBackground
		deleteOptions := metav1.DeleteOptions{
			PropagationPolicy: &policy,
		}
		podClient.Services(Config.Config.UserNamespace).Delete(ctx, serviceName, deleteOptions)

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
			Selector: map[string]string{LABEL_POD: podName},
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

	smap, err := Config.Config.GetServiceMapper()
	if err != nil {
		fmt.Printf("Failed set up mapping: %s\n", err)
		return err
	}

	err = smap.Start(userName, hatchApp.PathRewrite, hatchApp.UseTLS, service )
	if err != nil {
		fmt.Printf("Failed set up mapping: %s\n", err)
		return err
	}

	Config.Logger.Printf("Launched service %s for user %s forwarding port %d\n", serviceName, userName, hatchApp.TargetPort)

	nodes, _ := podClient.Nodes().List(context.TODO(), metav1.ListOptions{})
	NodeIP := nodes.Items[0].Status.Addresses[0].Address
	NodePort := service.Spec.Ports[0].NodePort
	createLocalService(ctx, userName, hash, NodeIP, NodePort)

	return nil
}



// Creates a local service that portal can reach
// and route traffic to pod in external cluster.
func createLocalService(ctx context.Context, userName string, hash string, serviceURL string, servicePort int32) error {

	hatchApp, ok := Config.ContainersMap[hash]
	if !ok {
		return fmt.Errorf("Container %s not found", hash)
	}

	localPodClient := getLocalPodClient()

	baseName := getBaseName(userName, hash)
	serviceName := userToResourceName(baseName, "service")
	podName := userToResourceName(baseName, "pod")

	labelsService := make(map[string]string)
	labelsService[LABEL_POD] = podName
	labelsService[LABEL_USER] = escapism(userName)
	labelsService[LABEL_APPID] = hash
	//annotationsService := make(map[string]string)
	//annotationsService["getambassador.io/config"] = fmt.Sprintf(localAmbassadorYaml, userToResourceName(userName, "mapping"), userName, serviceURL, servicePort, hatchApp.PathRewrite, hatchApp.UseTLS)

	_, err := localPodClient.Services(Config.Config.UserNamespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err == nil {
		// This probably happened as the result of some error... there was no pod but was a service
		// Lets just clean it up and proceed
		policy := metav1.DeletePropagationBackground
		deleteOptions := metav1.DeleteOptions{
			PropagationPolicy: &policy,
		}
		localPodClient.Services(Config.Config.UserNamespace).Delete(ctx, serviceName, deleteOptions)

	}

	localService := &k8sv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        serviceName,
			Namespace:   Config.Config.UserNamespace,
			Labels:      labelsService,
			Annotations: make(map[string]string),
		},
		Spec: k8sv1.ServiceSpec{
			Type:     k8sv1.ServiceTypeClusterIP,
			Selector: map[string]string{LABEL_POD: podName},
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

	smap, err := Config.Config.GetServiceMapper()
	if err != nil {
		fmt.Printf("Failed set up mapping: %s\n", err)
		return err
	}

	err = smap.Start(userName, hatchApp.PathRewrite, hatchApp.UseTLS, localService )
	if err != nil {
		fmt.Printf("Failed set up mapping: %s\n", err)
		return err
	}

	Config.Logger.Printf("Launched local service %s for user %s forwarding port %d\n", serviceName, userName, hatchApp.TargetPort)
	return nil
}
