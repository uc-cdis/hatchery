package hatchery

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	config, err := LoadConfig("../testData/testConfig.json", nil)
	if nil != err {
		t.Error(fmt.Sprintf("failed to load config, got: %v", err))
	}
	numContainers := len(config.Config.Containers)
	if numContainers != 4 {
		t.Error(fmt.Sprintf("config did not load the expected number of containers: %v != %v", numContainers, 4))
	}
	jsBytes, err2 := json.Marshal(config.Config)
	if nil != err2 {
		t.Error(fmt.Sprintf("failed to re-marshall config to json: %v", err2))
	}
	numFriends := len(config.Config.Containers[numContainers-1].Friends)
	if numFriends != 2 {
		t.Error(fmt.Sprintf("config did not load the expected number of friends: %v != %v", numFriends, 2))
	}

	config.Logger.Printf("config_test marshalled config: %v", string(jsBytes))
}
