package hatchery

import (
	"fmt"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	config, err := LoadConfig("../testData/testConfig.json", nil)
	if nil != err {
		t.Error(fmt.Sprintf("failed to load config, got: %v", err))
	}
	if len(config.Config.Containers) != 3 {
		t.Error(fmt.Sprintf("config did not load the expected number of containers: %v != %v", len(config.Config.Containers), 3))
	}
}
