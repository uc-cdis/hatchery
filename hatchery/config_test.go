package hatchery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	defer SetupAndTeardownTest()()
	expectedContainers := 8

	config, err := LoadConfig("../testData/testConfig.json", nil)
	if nil != err {
		t.Errorf("failed to load config, got: %v", err)
		return
	}
	numContainers := len(config.Config.Containers)
	if numContainers != expectedContainers {
		t.Errorf("config did not load the expected number of containers: %v != %v", numContainers, expectedContainers)
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

func TestLoadConfigMissingGSI(t *testing.T) {
	defer SetupAndTeardownTest()()

	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	/* Act */
	config, err := LoadConfig("../testData/testConfigMissingGSI.json", logger)

	/* Assert */
	if config != nil {
		t.Errorf("Config load should have failed with config=nil. Got %v", config)
	}
	if err == nil {
		t.Errorf("failed to catch missing GSI. err should have message but got nil.")
	}
	expectedErrorMessage := "'license-user-maps-dynamodb-table' is present but missing 'license-user-maps-global-secondary-index'"
	expectedError := errors.New(expectedErrorMessage)
	if err.Error() != expectedError.Error() {
		t.Errorf("Unexpected error message: \nWant:\n\t %+v,\nGot:\n\t %+v",
			expectedError,
			err)
	}

	expectedLoggerMessage := fmt.Sprintf("Error in configuration: %v", expectedErrorMessage)
	logOutput := buf.String()
	logLines := strings.Split(logOutput, "\n")
	lastLine := logLines[len(logLines)-2]
	if lastLine != expectedLoggerMessage {
		t.Errorf("Unexpected logger message: \nWant:\n\t %+v,\nGot:\n\t%+v",
			expectedLoggerMessage,
			lastLine)
	}

}

func TestLoadConfigMissingLicenseTable(t *testing.T) {
	defer SetupAndTeardownTest()()

	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	/* Act */
	config, err := LoadConfig("../testData/testConfigMissingLicenseTable.json", logger)

	/* Assert */
	if config != nil {
		t.Errorf("Config load should have failed with config=nil. Got %v", config)
	}
	if err == nil {
		t.Errorf("failed to catch missing user license table. err should have message but got nil.")
	}
	containerName := "(Generic, Limited Gen3-licensed) Stata Notebook"
	expectedErrorMessage := fmt.Sprintf("no 'license-user-maps-dynamodb-table' in configuration but license is configured for container %s", containerName)
	expectedError := errors.New(expectedErrorMessage)
	if err.Error() != expectedError.Error() {
		t.Errorf("Unexpected error message: \nWant:\n\t %+v,\nGot:\n\t %+v",
			expectedError,
			err)
	}

	expectedLoggerMessage := fmt.Sprintf("Error in configuration: %v", expectedErrorMessage)
	logOutput := buf.String()
	logLines := strings.Split(logOutput, "\n")
	lastLine := logLines[len(logLines)-2]
	if lastLine != expectedLoggerMessage {
		t.Errorf("Unexpected logger message: \nWant:\n\t %+v,\nGot:\n\t %+v",
			expectedLoggerMessage,
			lastLine)
	}

}
