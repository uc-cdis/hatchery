package hatchery

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"testing"
)

func TestValidateAuthzConfigVersion0_1(t *testing.T) {
	Config = &FullHatcheryConfig{
		Logger: log.New(os.Stdout, "", log.LstdFlags),
	}

	testCases := []struct {
		name       string
		valid      bool
		jsonConfig string
	}{
		{
			name:  "Valid first level 'or' with 2 items",
			valid: true,
			jsonConfig: `{
				"version": 0.1,
				"rules": {
					"or": [
						{"resource_paths": ["/workspace/abc"]},
						{"pay_models": ["Direct Pay"]}
					]
				}
			}`,
		},
		{
			name:  "Valid first level 'and' with 1 item",
			valid: true,
			jsonConfig: `{
				"version": 0.1,
				"rules": {
					"and": [
						{"resource_paths": ["/workspace/abc"]}
					]
				}
			}`,
		},
		{
			name:  "Valid first level 'and' with 3 items",
			valid: true,
			jsonConfig: `{
				"version": 0.1,
				"rules": {
					"and": [
						{"resource_paths": ["/workspace/a"]},
						{"resource_paths": ["/workspace/b"]},
						{"resource_paths": ["/workspace/c"]}
					]
				}
			}`,
		},
		{
			name:  "Valid first level 'resource_paths'",
			valid: true,
			jsonConfig: `{
				"version": 0.1,
				"rules": { "resource_paths": ["/workspace/abc"] }
			}`,
		},
		{
			name:  "Valid first level 'pay_models'",
			valid: true,
			jsonConfig: `{
				"version": 0.1,
				"rules": { "pay_models": ["Direct Pay"] }
			}`,
		},
		{
			name:  "Invalid version",
			valid: false,
			jsonConfig: `{
				"version": 2,
				"rules": { "pay_models": ["Direct Pay"] }
			}`,
		},
		{
			name:  "Invalid 'resource_paths' with 0 items",
			valid: false,
			jsonConfig: `{
				"version": 0.1,
				"rules": { "resource_paths": [] }
			}`,
		},
		{
			name:  "Invalid 'pay_models' with 0 items",
			valid: false,
			jsonConfig: `{
				"version": 0.1,
				"rules": { "pay_models": [] }
			}`,
		},
		{
			name:  "Too many keys on first level",
			valid: false,
			jsonConfig: `{
				"version": 0.1,
				"rules": {
					"or": [
						{"resource_paths": ["/workspace/abc"]},
						{"pay_models": ["Direct Pay"]}
					],
					"pay_models": ["Direct Pay"]
				}
			}`,
		},
		{
			name:  "No keys on first level",
			valid: false,
			jsonConfig: `{
				"version": 0.1,
				"rules": {}
			}`,
		},
		{
			name:  "Too many nested levels ('or' includes 'and')",
			valid: false,
			jsonConfig: `{
				"version": 0.1,
				"rules": {
					"or": [
						{"resource_paths": ["/workspace/a"]},
						{
							"and": [
								{"resource_paths": ["/workspace/b"]},
								{"pay_models": ["Direct Pay"]}
							]
						}
					]
				}
			}`,
		},
		{
			name:  "Too many nested levels ('and' includes 'or')",
			valid: false,
			jsonConfig: `{
				"version": 0.1,
				"rules": {
					"and": [
						{"resource_paths": ["/workspace/a"]},
						{
							"or": [
								{"resource_paths": ["/workspace/b"]},
								{"pay_models": ["Direct Pay"]}
							]
						}
					]
				}
			}`,
		},
	}

	for _, testCase := range testCases {
		t.Logf("Running test case '%s'", testCase.name)
		config := AuthzConfig{}
		err := json.Unmarshal([]byte(testCase.jsonConfig), &config)
		if nil != err {
			t.Errorf("failed to load authz config: %v", err)
			return
		}
		err = ValidateAuthzConfig(config)
		if testCase.valid && nil != err {
			t.Errorf("config is valid but the validation did not accept it: %v", err)
			return
		}
		if !testCase.valid && nil == err {
			t.Error("config is not valid but the validation accepted it")
			return
		}
	}
}

// func TestIsUserAuthorizedForContainer(t *testing.T) {

// 	t.Errorf("DONE! TODO remove")
// }

func TestIsUserAuthorizedForPayModels(t *testing.T) {
	Config = &FullHatcheryConfig{
		Logger: log.New(os.Stdout, "", log.LstdFlags),
	}

	testCases := []struct {
		authorized       bool
		userPayModel     *PayModel
		allowedPayModels []string
	}{
		{
			authorized:       true,
			userPayModel:     nil,
			allowedPayModels: []string{"None"},
		},
		{
			authorized:       false,
			userPayModel:     nil,
			allowedPayModels: []string{"Direct Pay"},
		},
		{
			authorized:       true,
			userPayModel:     &PayModel{Name: "Direct Pay"},
			allowedPayModels: []string{"None", "Direct Pay"},
		},
		{
			authorized:       false,
			userPayModel:     &PayModel{Name: "Direct Pay"},
			allowedPayModels: []string{"None"},
		},
		{
			authorized:       false,
			userPayModel:     &PayModel{Name: "ERROR"}, // unable to get the user's pay model
			allowedPayModels: []string{"Direct Pay"},
		},
	}

	originalGetCurrentPayModel := getCurrentPayModel
	for _, testCase := range testCases {
		userPayModelName := "nil"
		if testCase.userPayModel != nil {
			userPayModelName = testCase.userPayModel.Name
		}
		t.Logf("Running test case: userPayModel='%s'; allowedPayModels=%v", userPayModelName, testCase.allowedPayModels)

		// mock the user's pay model
		getCurrentPayModel = func(string) (*PayModel, error) {
			if testCase.userPayModel != nil && testCase.userPayModel.Name == "ERROR" {
				return nil, fmt.Errorf("unable to get the user's pay model")
			}
			return testCase.userPayModel, nil
		}

		authorized, err := isUserAuthorizedForPayModels("user1", testCase.allowedPayModels)
		if nil != err {
			t.Errorf("'isUserAuthorizedForPayModels' call failed: %v", err)
			return
		}
		if testCase.authorized && !authorized {
			t.Error("access should be granted, but it was not")
			return
		} else if !testCase.authorized && authorized {
			t.Error("access should not be granted, but it was")
			return
		}
	}

	getCurrentPayModel = originalGetCurrentPayModel // restore original function
}

func TestIsUserAuthorizedForResourcePaths(t *testing.T) {
	Config = &FullHatcheryConfig{
		Logger: log.New(os.Stdout, "", log.LstdFlags),
	}

	testCases := []struct {
		name                 string
		authorizedInArborist bool
		arboristError        bool
	}{
		{
			name:                 "User has access in Arborist",
			arboristError:        false,
			authorizedInArborist: true,
		},
		{
			name:                 "User does not have access in Arborist",
			arboristError:        false,
			authorizedInArborist: false,
		},
		{
			name:                 "Error while mking call to Arborist",
			arboristError:        true,
			authorizedInArborist: true,
		},
	}

	resourcePaths := []string{"/workspace/abc", "/workspace/xyz"}
	expectedRequestBody := "{ \"requests\": [{\"resource\": \"/workspace/abc\", \"action\": {\"service\": \"jupyterhub\", \"method\": \"launch\"}},{\"resource\": \"/workspace/xyz\", \"action\": {\"service\": \"jupyterhub\", \"method\": \"launch\"}}]}"

	originalArboristAuthRequest := arboristAuthRequest
	for _, testCase := range testCases {
		t.Logf("Running test case: '%s'", testCase.name)

		// mock the call to arborist
		arboristAuthRequest = func(accessToken string, body string) (bool, error) {
			if testCase.arboristError {
				return false, fmt.Errorf("mocking an error while making call to arborist")
			}
			// part of the test is to ensure that the right request body is generated and sent to arborist:
			if !json.Valid([]byte(body)) {
				return false, fmt.Errorf("request body generated by `isUserAuthorizedForResourcePaths` is not valid JSON: %s", body)
			}
			if body != expectedRequestBody {
				return false, fmt.Errorf("request body generated by `isUserAuthorizedForResourcePaths` is not the same as expected. Expected: '%s'. Received: '%s'", expectedRequestBody, body)
			}
			return testCase.authorizedInArborist, nil
		}

		authorized, err := isUserAuthorizedForResourcePaths("user1", "accessToken", resourcePaths)
		if nil != err {
			t.Errorf("'isUserAuthorizedForResourcePaths' call failed: %v", err)
			return
		}
		if testCase.arboristError {
			if authorized {
				t.Error("There was an error while making call to arborist, so user should not have been authorized")
				return
			}
		} else if authorized != testCase.authorizedInArborist {
			t.Errorf("User authorization in Arborist is '%v', but `isUserAuthorizedForResourcePaths` returned 'authorized='%v'", testCase.authorizedInArborist, authorized)
			return
		}
	}

	arboristAuthRequest = originalArboristAuthRequest // restore original function
}
