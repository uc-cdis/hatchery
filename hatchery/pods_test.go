package hatchery

import (
	"encoding/json"
	"testing"

	k8sv1 "k8s.io/api/core/v1"
)

func TestBuildPodFromJSON(t *testing.T) {
	defer SetupAndTeardownTest()()
	expectedApps := 8

	config, err := LoadConfig("../testData/testConfig.json", nil)
	if nil != err {
		t.Errorf("failed to load config, got: %v", err)
		return
	}
	numApps := len(config.Config.Containers)
	if numApps != expectedApps {
		t.Errorf("did not load %d apps, got: %v", expectedApps, numApps)
		return
	}
	app := &config.Config.Containers[numApps-3]
	pod, err := buildPod(config, app, "frickjack", nil)

	if nil != err {
		t.Errorf("failed to build a pod - %v", err)
	}

	numContainers := len(pod.Spec.Containers)
	if numContainers != len(app.Friends)+2 { // +2 b/c sidecar + main
		t.Errorf("unexpected number of containers in pod, desired value is %v but got %v", len(app.Friends)+2, numContainers)
	}
	jsBytes, err := json.MarshalIndent(pod, "", "  ")
	if nil != err {
		t.Errorf("failed to marshal JSON - %v", err)
	}

	config.Logger.Printf("pod_test marshalled pod: %v", string(jsBytes))
}

func TestBuildPodFromDockstore(t *testing.T) {
	defer SetupAndTeardownTest()()
	expectedApps := 8

	config, err := LoadConfig("../testData/testConfig.json", nil)
	if nil != err {
		t.Errorf("failed to load config, got: %v", err)
		return
	}
	numApps := len(config.Config.Containers)
	if numApps != expectedApps {
		t.Errorf("did not load %d apps, got: %v", expectedApps, numApps)
		return
	}
	app := &config.Config.Containers[numApps-2]
	pod, err := buildPod(config, app, "frickjack", nil)

	if nil != err {
		t.Errorf("failed to build a pod - %v", err)
	}

	numContainers := len(pod.Spec.Containers)
	if numContainers != len(app.Friends)+1 { // +1 b/c sidecar
		t.Errorf("unexpected number of containers in pod, desired value is %v but got %v", len(app.Friends), numContainers)
	}
	jsBytes, err := json.MarshalIndent(pod, "", "  ")
	if nil != err {
		t.Errorf("failed to marshal JSON - %v", err)
	}

	config.Logger.Printf("pod_test marshalled pod: %v", string(jsBytes))
}

func TestNewUserVolumePVCWithoutDefaultStorageClass(t *testing.T) {
	defer SetupAndTeardownTest()()

	config, err := LoadConfig("../testData/testConfig.json", nil)
	if nil != err {
		t.Errorf("failed to load config, got: %v", err)
		return
	}
	// newUserVolumePVC reads the global Config
	Config = config
	config.Config.DefaultStorageClass = ""

	pod := &k8sv1.Pod{}
	pvc := newUserVolumePVC("user-claim", pod)

	if pvc.Spec.StorageClassName != nil {
		t.Errorf("expected no storageClassName to be set when default-storage-class is empty, got: %v", *pvc.Spec.StorageClassName)
	}
	if pvc.Spec.Resources.Requests.Storage().String() != "10Gi" {
		t.Errorf("expected user volume size 10Gi, got: %v", pvc.Spec.Resources.Requests.Storage().String())
	}
}

func TestNewUserVolumePVCWithDefaultStorageClass(t *testing.T) {
	defer SetupAndTeardownTest()()

	config, err := LoadConfig("../testData/testConfig.json", nil)
	if nil != err {
		t.Errorf("failed to load config, got: %v", err)
		return
	}
	// newUserVolumePVC reads the global Config
	Config = config
	config.Config.DefaultStorageClass = "nfs-home"

	pod := &k8sv1.Pod{}
	pvc := newUserVolumePVC("user-claim", pod)

	if pvc.Spec.StorageClassName == nil {
		t.Errorf("expected storageClassName to be set when default-storage-class is configured")
		return
	}
	if *pvc.Spec.StorageClassName != "nfs-home" {
		t.Errorf("expected storageClassName 'nfs-home', got: %v", *pvc.Spec.StorageClassName)
	}
}
