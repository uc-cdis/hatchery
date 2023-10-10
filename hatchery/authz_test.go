package hatchery

import (
	"encoding/json"
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
		jsonConfig string
		valid      bool
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

	for _, testcase := range testCases {
		t.Logf("Running test case '%s'", testcase.name)
		config := AuthzConfig{}
		err := json.Unmarshal([]byte(testcase.jsonConfig), &config)
		if nil != err {
			t.Errorf("failed to load authz config: %v", err)
			return
		}
		err = ValidateAuthzConfig(config)
		if testcase.valid && nil != err {
			t.Errorf("config is valid but the validation did not accept it: %v", err)
			return
		}
		if !testcase.valid && nil == err {
			t.Errorf("config is not valid but the validation accepted it")
			return
		}
	}
}
