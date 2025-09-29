package hatchery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

func Test_VerifyPath(t *testing.T) {

	cwd, _ := os.Getwd()
	defaultBaseDir := filepath.Dir(cwd)

	testCases := []struct {
		name             string
		configPath       string
		baseDir          string
		want             string
		wantError        bool
		wantErrorMessage string
	}{
		{
			name:             "validConfigPath",
			configPath:       "testData/testConfig.json",
			baseDir:          defaultBaseDir,
			want:             filepath.Join(defaultBaseDir, "testData/testConfig.json"),
			wantError:        false,
			wantErrorMessage: "",
		},
		{
			name:             "overlapInPath",
			configPath:       filepath.Join(defaultBaseDir, "testData/testConfig.json"),
			baseDir:          filepath.Join(defaultBaseDir, "testData/"),
			want:             filepath.Join(defaultBaseDir, "testData/testConfig.json"),
			wantError:        false,
			wantErrorMessage: "",
		},
		{
			name:             "missingConfig",
			configPath:       "testData/missing.json",
			baseDir:          defaultBaseDir,
			want:             filepath.Join(defaultBaseDir, "testData/missing.json"),
			wantError:        true,
			wantErrorMessage: "unsafe or invalid path specified",
		},
		{
			name:             "outsideOfBaseDir",
			configPath:       "../testConfig.json",
			baseDir:          "/var",
			want:             "/testConfig.json",
			wantError:        true,
			wantErrorMessage: "unsafe or invalid path specified",
		},
		{
			name:             "unallowedExtension",
			configPath:       "testData/testConfig.txt",
			baseDir:          defaultBaseDir,
			want:             filepath.Join(defaultBaseDir, "testData/testConfig.txt"),
			wantError:        true,
			wantErrorMessage: "config file must be json",
		},
	}

	for _, testcase := range testCases {
		t.Logf("Testing VerifyPath when %s", testcase.name)

		/* Act */
		got, err := VerifyPath(testcase.configPath, testcase.baseDir)

		/* Assert */
		if got != testcase.want {
			t.Errorf("assertion error in 'VerifyPath', : \nWant:%+v\nGot:%+v", testcase.want, got)
		}
		if testcase.wantError {
			if err == nil {
				t.Error("\nassertion error: Expected error but got nil")
			} else if !strings.Contains(err.Error(), testcase.wantErrorMessage) {
				t.Errorf("\nassertion error: Message does not contain %v", testcase.wantErrorMessage)
			}
		} else {
			if err != nil {
				t.Errorf("Got unexpected error %v", err.Error())
			}

		}
	}

}

func Test_VerifyPathOutsideBaseDir(t *testing.T) {

	cwd, _ := os.Getwd()
	baseDir := filepath.Dir(cwd)

	parentPath := "../../testConfig.json"
	want := filepath.Join(baseDir, parentPath)
	wantErrorMessage := "access denied: cannot read config files outside of the base directory"

	oldEvalSymlinks := evalSymLinks
	defer func() { evalSymLinks = oldEvalSymlinks }()
	// mock the filepath.EvalSymLinks as passing without error
	evalSymLinks = func(path string) (string, error) {
		return path, nil
	}

	/* Act */
	got, err := VerifyPath(parentPath, baseDir)

	/* Assert */
	if got != want {
		t.Errorf("assertion error in 'VerifyPath', : \nWant:%+v\nGot:%+v", want, got)
	}
	if err == nil {
		t.Errorf("\nassertion error: Expected error but got nil")
	}
	if !strings.Contains(err.Error(), wantErrorMessage) {
		t.Errorf("\nassertion error: Message does not contain %v", wantErrorMessage)
	}

}

func TestMissingConfigFile(t *testing.T) {
	defer SetupAndTeardownTest()()

	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	/* Act */
	_, err := LoadConfig("../testData/testDoesNotExist.json", logger)

	/* Assert */
	if err == nil {
		t.Errorf("failed to catch missing config file. err should have message but got nil.")
	}
	expectedSubString := "no such file or directory"
	if !strings.Contains(err.Error(), expectedSubString) {
		t.Errorf("Unexpected error message: \nWant substring :\n\t %+v,\n in actual error:\n\t %+v",
			expectedSubString,
			err)
	}

	logOutput := buf.String()
	logLines := strings.Split(logOutput, "\n")
	lastLine := logLines[len(logLines)-2]
	if !strings.Contains(lastLine, expectedSubString) {
		t.Errorf("Unexpected logger message: \nWant substring :\n\t %+v,\n in actual error:\n\t%+v",
			expectedSubString,
			lastLine)
	}

}

func TestInvalidConfigs(t *testing.T) {
	defer SetupAndTeardownTest()()

	testCases := []struct {
		name             string
		testData         string
		wantErrorMessage string
	}{
		{
			name:             "MissingLicenseGSI",
			testData:         "../testData/testConfigMissingGSI.json",
			wantErrorMessage: "'license-user-maps-dynamodb-table' is present but missing 'license-user-maps-global-secondary-index'",
		},
		{
			name:             "MissingLicenseTable",
			testData:         "../testData/testConfigMissingLicenseTable.json",
			wantErrorMessage: "no 'license-user-maps-dynamodb-table' in configuration but license is configured for container 'Test-missing-license-table'",
		},
		{
			name:             "InvalidLicenseInfo",
			testData:         "../testData/testConfigInvalidLicense.json",
			wantErrorMessage: "container 'Test-missing-license-type' has an invalid 'license' configuration",
		},
	}

	for _, testcase := range testCases {
		t.Logf("Testing getPayModelsForUser when %s", testcase.name)

		/* Set up */
		var buf bytes.Buffer
		logger := log.New(&buf, "", 0)

		/* Act */
		config, err := LoadConfig(testcase.testData, logger)

		/* Assert */
		if config != nil {
			t.Errorf("Config load should have failed with config=nil. Got %v", config)
		}
		if err == nil {
			t.Errorf("failed to catch invalid config. err should have message but got nil.")
		}
		expectedError := errors.New(testcase.wantErrorMessage)
		if err.Error() != expectedError.Error() {
			t.Errorf("Unexpected error message: \nWant:\n\t %+v,\nGot:\n\t %+v",
				expectedError,
				err)
		}

		expectedLoggerMessage := fmt.Sprintf("Error in configuration: %v", testcase.wantErrorMessage)
		logOutput := buf.String()
		logLines := strings.Split(logOutput, "\n")
		lastLine := logLines[len(logLines)-2]
		if lastLine != expectedLoggerMessage {
			t.Errorf("Unexpected logger message: \nWant:\n\t %+v,\nGot:\n\t%+v",
				expectedLoggerMessage,
				lastLine)
		}

	}

}
