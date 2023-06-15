package hatchery

import (
	"encoding/json"
	"testing"

	"go.uber.org/zap"
)

func TestBuildPodFromJSON(t *testing.T) {
	zapLogger, _ := zap.NewProduction()
	logger := zapLogger.Sugar()
	config, err := LoadConfig("../testData/testConfig.json", logger)
	if nil != err {
		t.Errorf("failed to load config, got: %v", err)
		return
	}
	numApps := len(config.Config.Containers)
	if numApps != 7 {
		t.Errorf("did not load 7 apps, got: %v", numApps)
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

	config.Logger.Debugw("pod_test marshalled pod", "pod", string(jsBytes))
}

func TestBuildPodFromDockstore(t *testing.T) {
	zapLogger, _ := zap.NewProduction()
	logger := zapLogger.Sugar()
	config, err := LoadConfig("../testData/testConfig.json", logger)
	if nil != err {
		t.Errorf("failed to load config, got: %v", err)
		return
	}
	numApps := len(config.Config.Containers)
	if numApps != 7 {
		t.Errorf("did not load 7 apps, got: %v", numApps)
		return
	}
	app := &config.Config.Containers[numApps-2]
	pod, err := buildPod(config, app, "frickjack", nil)
	if nil != err {
		// Log error using suggared loggared from config
		config.Logger.Errorw("failed to build a pod",
			"error", err,
		)
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

	config.Logger.Debugw("pod_test marshalled pod", "pod", string(jsBytes))
}
