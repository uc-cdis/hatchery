package hatchery

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	batchv1 "k8s.io/api/batch/v1"
	k8sv1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func LaunchEcsWsJob(userName string) {
	// creates the kubeconfig
	config, err := rest.InClusterConfig()
	if err != nil {
		// Use the kubeconfig file if runninig in local dev
		kubeConfig := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
		config, err = clientcmd.BuildConfigFromFlags("", kubeConfig)
		if err != nil {
			Config.Logger.Printf("Error: %s", err)
		}
	}
	clientset, err := kubernetes.NewForConfig(config)
	namespace := Config.Config.Sidecar.Env["NAMESPACE"]

	batchV1Client := clientset.BatchV1()
	coreV1Client := clientset.CoreV1()
	listOptions := metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=hatchery"}
	hatcheryPod, err := coreV1Client.Pods(namespace).List(context.TODO(), listOptions)
	if err != nil {
		Config.Logger.Printf("Error: %s", err)
	}

	jobName := userToResourceName(userName, "ecs") + "-" + strconv.FormatInt(time.Now().Unix(), 10)
	jobs := batchV1Client.Jobs(namespace)
	cmd := "/hatchery -job=LaunchEcsWorkspace"

	jobSpec := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: aws.Int32(100),
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers:    hatcheryPod.Items[0].Spec.Containers,
					RestartPolicy: "Never",
					Volumes:       hatcheryPod.Items[0].Spec.Volumes,
				},
			},
		},
	}

	var envVars []k8sv1.EnvVar

	jobSpec.Spec.Template.Spec.Containers[0].Command = strings.Split(cmd, " ")
	jobSpec.Spec.Template.Spec.Containers[0].Env = append(jobSpec.Spec.Template.Spec.Containers[0].Env, envVars)

	_, err = jobs.Create(context.TODO(), jobSpec, metav1.CreateOptions{})
	if err != nil {
		Config.Logger.Fatalf("Failed to create K8s job. %s\n", err)
	}

	//print job details
	Config.Logger.Println("Created K8s job successfully")

}
