package hatchery

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestBuildPod(t *testing.T) {
	config, err := LoadConfig("../testData/testConfig.json", nil)
	if nil != err {
		t.Error(fmt.Sprintf("failed to load config, got: %v", err))
	}
	numApps := len(config.Config.Containers)
	if 5 != numApps {
		t.Error(fmt.Sprintf("did not load 5 apps, got: %v", numApps))
		return
	}
	app := &config.Config.Containers[numApps-2]
	pod, err := buildPod(config, app, "frickjack")

	if nil != err {
		t.Error(fmt.Sprintf("failed to build a pod - %v", err))
	}

	numContainers := len(pod.Spec.Containers)
	if numContainers != len(app.Friends)+2 { // +2 b/c sidecar + main
		t.Error(fmt.Sprintf("unexpected number of containers in pod - %v", numContainers))
	}
	jsBytes, err := json.MarshalIndent(pod, "", "  ")

	config.Logger.Printf("pod_test marshalled pod: %v", string(jsBytes))
}
