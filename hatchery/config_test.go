package hatchery

import (
	"encoding/json"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	defer SetupAndTeardownTest()()

	config, err := LoadConfig("../testData/testConfig.json", nil)
	if nil != err {
		t.Errorf("failed to load config, got: %v", err)
		return
	}
	numContainers := len(config.Config.Containers)
	if numContainers != 7 {
		t.Errorf("config did not load the expected number of containers: %v != %v", numContainers, 7)
		return
	}
	jsBytes, err2 := json.MarshalIndent(config.Config, "", "  ")
	if nil != err2 {
		t.Errorf("failed to re-marshall config to json: %v", err2)
	}
	numFriends := len(config.Config.Containers[numContainers-4].Friends)
	if numFriends != 2 {
		t.Errorf("config did not load the expected number of friends: %v != %v", numFriends, 2)
	}

	// last app should be the dockstore test app
	if config.Config.Containers[numContainers-1].Name != "DockstoreTest" {
		t.Errorf("unexpected more-info app name - expected DockstoreTest, got: %v", config.Config.Containers[numContainers-1].Name)
	}
	config.Logger.Printf("config_test marshalled config: %v", string(jsBytes))
}
