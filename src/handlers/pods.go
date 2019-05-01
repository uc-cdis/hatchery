package handlers

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	batchtypev1 "k8s.io/client-go/kubernetes/typed/batch/v1"
	"k8s.io/client-go/rest"
)

var (
	trueVal  = true
	falseVal = false
)

type JobsArray struct {
	JobInfo []JobInfo `json:"jobs"`
}

type JobInfo struct {
	UID    string `json:"uid"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type JobOutput struct {
	Output string `json:"output"`
}

func getJobClient() batchtypev1.JobInterface {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	// Access jobs. We can't do it all in one line, since we need to receive the
	// errors and manage thgem appropriately
	batchClient := clientset.BatchV1()
	jobsClient := batchClient.Jobs("default")
	return jobsClient
}

func createK8sJob(inputData string, accessToken string, userName string) (*JobInfo, error) {
	jobsClient := getJobClient()
	randname := GetRandString(5)
	name := fmt.Sprintf("simu-%s", randname)
	fmt.Println("input data: ", inputData)
	var deadline int64 = 300
	var backoff int32 = 1
	labels := make(map[string]string)
	labels["app"] = "sowerjob"
	annotations := make(map[string]string)
	annotations["gen3username"] = userName
	// For an example of how to create jobs, see this file:
	// https://github.com/pachyderm/pachyderm/blob/805e63/src/server/pps/server/api_server.go#L2320-L2345
	batchJob := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Job",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: batchv1.JobSpec{
			// Optional: Parallelism:,
			// Optional: Completions:,
			// Optional: ActiveDeadlineSeconds:,
			// Optional: Selector:,
			// Optional: ManualSelector:,
			BackoffLimit:          &backoff,
			ActiveDeadlineSeconds: &deadline,
			Template: k8sv1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:   name,
					Labels: labels,
				},
				Spec: k8sv1.PodSpec{
					InitContainers: []k8sv1.Container{}, // Doesn't seem obligatory(?)...
					Containers: []k8sv1.Container{
						{
							Name:  "job-task",
							Image: "quay.io/cdis/mickey-demo:latest",
							SecurityContext: &k8sv1.SecurityContext{
								Privileged: &falseVal,
							},
							ImagePullPolicy: k8sv1.PullPolicy(k8sv1.PullAlways),
							Env: []k8sv1.EnvVar{
								{
									Name:  "INPUT_DATA",
									Value: inputData,
								},
								{
									Name:  "ACCESS_TOKEN",
									Value: accessToken,
								},
							},
							VolumeMounts: []k8sv1.VolumeMount{},
						},
					},
					RestartPolicy:    k8sv1.RestartPolicyNever,
					Volumes:          []k8sv1.Volume{},
					ImagePullSecrets: []k8sv1.LocalObjectReference{},
				},
			},
		},
		// Optional, not used by pach: JobStatus:,
	}

	newJob, err := jobsClient.Create(batchJob)
	if err != nil {
		return nil, err
	}
	fmt.Println("New job name: ", newJob.Name)
	ji := JobInfo{}
	ji.Name = newJob.Name
	ji.UID = string(newJob.GetUID())
	ji.Status = jobStatusToString(&newJob.Status)
	return &ji, nil
}

func getPodMatchingJob(jobname string) *k8sv1.Pod {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	pods, err := clientset.CoreV1().Pods("default").List(metav1.ListOptions{})
	for _, pod := range pods.Items {
		if strings.HasPrefix(pod.Name, jobname) {
			return &pod
		}
	}
	return nil
}

func getJobLogs(jobid string) (*JobOutput, error) {
	job, err := getJobByID(getJobClient(), jobid)
	if err != nil {
		return nil, err
	}
	if job.Labels["app"] != "sowerjob" {
		return nil, fmt.Errorf("job with jobid %s not found", jobid)
	}

	pod := getPodMatchingJob(job.Name)
	if pod == nil {
		return nil, fmt.Errorf("Pod not found")
	}

	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	podLogOptions := k8sv1.PodLogOptions{}
	req := clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &podLogOptions)
	podLogs, err := req.Stream()
	if err != nil {
		return nil, err
	}
	defer podLogs.Close()

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	if err != nil {
		return nil, fmt.Errorf("Error copying output")
	}
	str := buf.String()

	ji := JobOutput{}
	ji.Output = str
	return &ji, nil

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

func jobOlderThan(status *batchv1.JobStatus, cutoffSeconds int32) bool {
	then := time.Now().Add(time.Duration(-cutoffSeconds) * time.Second)
	return status.StartTime.Time.Before(then)
}

func StartMonitoringProcess() {
	jc := getJobClient()
	deleteOption := metav1.NewDeleteOptions(120)
	var deletionPropagation metav1.DeletionPropagation = "Background"
	deleteOption.PropagationPolicy = &deletionPropagation
	for {
		jobsList, err := jc.List(metav1.ListOptions{LabelSelector: "app=sowerjob"})

		if err != nil {
			fmt.Println("Monitoring error: ", err)
			time.Sleep(30 * time.Second)
			continue
		}

		for _, job := range jobsList.Items {
			k8sJob, err := getJobStatusByID(string(job.GetUID()))
			if err != nil {
				fmt.Println("Can't get job status by UID: ", job.Name, err)
			} else {
				if k8sJob.Status == "Unknown" || k8sJob.Status == "Running" {
					continue
				} else {
					if jobOlderThan(&job.Status, 1800) {
						fmt.Println("Deleting old job: ", job.Name)
						if err = jc.Delete(job.Name, deleteOption); err != nil {
							fmt.Println("Error deleting job : ", job.Name, err)
						}
					}
				}
			}

		}

		time.Sleep(30 * time.Second)
	}
}
