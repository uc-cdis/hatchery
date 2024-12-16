package hatchery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"
)

/*
to add a new `authz` version:
  - create a new `AuthzVersion_XXX` type, add it to `AuthzConfig` and update `UnmarshalJSON` to unmarshall
    the config to that type;
  - create a new `validateAuthzConfigVersion_XXX` function and call it in `ValidateAuthzConfig`;
  - create a new `isUserAuthorizedForContainerVersion_XXX` function and call it in `isUserAuthorizedForContainer`.
*/

type AuthzConfig struct {
	Version          float32 `json:"version"`
	AuthzVersion_0_1 AuthzVersion_0_1
}

type AuthzVersion_0_1 struct {
	And           []AuthzVersion_0_1 `json:"and"`
	Or            []AuthzVersion_0_1 `json:"or"`
	ResourcePaths []string           `json:"resource_paths"`
	PayModels     []string           `json:"pay_models"`
}

type AuthRequestResponse struct {
	Auth bool `json:"auth"`
}

/*
	Authorization configuration parsing and validation
*/

func (authzConfig *AuthzConfig) UnmarshalJSON(data []byte) error {
	/*
		This custom unmarshal function allows us to parse the json config into the appropriate struct
		depending on the `authz.version` value (eg struct AuthzVersion_0_1 when authz.version=0.1)
	*/
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("could not parse 'authz' config: %v", err)
	}
	configObject := v.(map[string]interface{})

	if configObject["version"] == nil {
		// When there is no `authz` field, `authz.version` is unmarshalled to the default int
		// value "0" which is interpreted as "no authz config".
		// Here there is an `authz` field but no `authz.version`: assume it's a misconfiguration.
		return fmt.Errorf("missing 'version' field in 'authz' config: %v", string(data))
	} else {
		authzConfig.Version = float32(configObject["version"].(float64))
	}

	// parse json data into the appropriate struct
	if authzConfig.Version == 0.1 {
		if err := json.Unmarshal([]byte(data), &authzConfig.AuthzVersion_0_1); err != nil {
			return fmt.Errorf("could not parse 'authz' config into AuthzVersion_0_1 struct: %v", err)
		}
	}

	return nil
}

func ValidateAuthzConfig(logger *log.Logger, authzConfig AuthzConfig) error {
	if authzConfig.Version == 0 { // default int value "0" is interpreted as "no authz config"
		return nil
	} else if authzConfig.Version == 0.1 {
		return validateAuthzConfigVersion_0_1(authzConfig.AuthzVersion_0_1)
	} else {
		return fmt.Errorf("Container authz config version '%v' is not valid", authzConfig.Version)
	}
}

func validateAuthzConfigVersion_0_1(authzConfig AuthzVersion_0_1) error {
	// check that only 1 of and/or/resource_paths/pay_models is set in the same block.
	// NOTE: if we implement support for nested rules, we should validate each nested level this way
	isOrStmt, isAndStmt, isResourcePathsStmt, isPayModelsStmt := 0, 0, 0, 0
	if len(authzConfig.Or) > 0 {
		isOrStmt = 1
	}
	if len(authzConfig.And) > 0 {
		isAndStmt = 1
	}
	if len(authzConfig.ResourcePaths) > 0 {
		isResourcePathsStmt = 1
	}
	if len(authzConfig.PayModels) > 0 {
		isPayModelsStmt = 1
	}
	sum := isOrStmt + isAndStmt + isResourcePathsStmt + isPayModelsStmt
	if sum != 1 {
		return fmt.Errorf("there should be exactly 1 key with non-null value on the 1st level of authz config, found %d", sum)
	}

	// although the `AuthzVersion_0_1` struct allows it, nesting and/or rules is not supported yet
	if isOrStmt == 1 || isAndStmt == 1 {
		for _, rule := range append(authzConfig.Or, authzConfig.And...) {
			if len(rule.Or) > 0 || len(rule.And) > 0 {
				return fmt.Errorf("nesting 'and' and 'or' authorization rules is not supported")
			}
		}
	}

	return nil
}

/*
	Container authorization checks
*/

var isUserAuthorizedForContainer = func(userName string, accessToken string, container Container) (bool, error) {
	if container.Authz.Version == 0 { // default int value "0" is interpreted as "no authz config"
		return true, nil
	}

	Config.Logger.Printf("DEBUG: Checking user '%s' access to container '%s'", userName, container.Name)
	if container.Authz.Version == 0.1 {
		return isUserAuthorizedForContainerVersion_0_1(userName, accessToken, container.Name, container.Authz.AuthzVersion_0_1)
	} else {
		// this should never happen, it would get caught by `ValidateAuthzConfig`
		return false, fmt.Errorf("Container authz config version '%v' is not valid", container.Authz.Version)
	}
}

func isUserAuthorizedForContainerVersion_0_1(userName string, accessToken string, containerName string, containerAuthz AuthzVersion_0_1) (bool, error) {
	var err error
	var userIsAuthorized bool

	if len(containerAuthz.Or) > 0 {
		userIsAuthorized = false
		for _, rule := range containerAuthz.Or {
			authorized, err := isUserAuthorizedForRule(userName, accessToken, rule)
			if nil != err {
				return false, err
			}
			if authorized {
				userIsAuthorized = true
				break
			}
		}
	} else if len(containerAuthz.And) > 0 {
		userIsAuthorized = true
		for _, rule := range containerAuthz.And {
			authorized, err := isUserAuthorizedForRule(userName, accessToken, rule)
			if nil != err {
				return false, err
			}
			if !authorized {
				userIsAuthorized = false
				break
			}
		}
	} else if len(containerAuthz.ResourcePaths) > 0 {
		userIsAuthorized, err = isUserAuthorizedForRule(userName, accessToken, containerAuthz)
		if nil != err {
			return false, err
		}
	} else if len(containerAuthz.PayModels) > 0 {
		userIsAuthorized, err = isUserAuthorizedForRule(userName, accessToken, containerAuthz)
		if nil != err {
			return false, err
		}
	} else {
		// in this function we assume that the Authz block passed the `ValidateAuthzConfig` validation, so
		// there should be no other option than the ones above. We should never reach this `else` block.
		return false, fmt.Errorf("unexpected container Authz value")
	}

	logPartial := ""
	if !userIsAuthorized {
		logPartial = "not "
	}
	Config.Logger.Printf("INFO: User '%s' is %sauthorized to run container '%s'", userName, logPartial, containerName)
	return userIsAuthorized, nil
}

func isUserAuthorizedForRule(userName string, accessToken string, rule AuthzVersion_0_1) (bool, error) {
	if len(rule.ResourcePaths) > 0 {
		return isUserAuthorizedForResourcePaths(userName, accessToken, rule.ResourcePaths)
	} else if len(rule.PayModels) > 0 {
		return isUserAuthorizedForPayModels(userName, rule.PayModels)
	} else {
		// in this function we assume that the Authz block passed the `ValidateAuthzConfig` validation, so
		// there should be no other option than the ones above. We should never reach this `else` block.
		return false, fmt.Errorf("unexpected container Authz rule value")
	}
}

var isUserAuthorizedForPayModels = func(userName string, allowedPayModels []string) (bool, error) {
	/*
		If the user is using any of the pay models specified in `allowedPayModels`, return true.
		Otherwise, return false.
	*/
	Config.Logger.Printf("DEBUG: Checking user '%s' pay model against allowed pay models %v", userName, allowedPayModels)

	if len(allowedPayModels) == 0 {
		// no pay models are allowed => everyone is denied access (although we should never reach this block
		// if the Authz block passed the `ValidateAuthzConfig` validation)
		return false, nil
	}

	if userName == "" {
		Config.Logger.Print("User is not logged in, assume they are not allowed to run container")
		return false, nil
	}
	currentPayModel, err := getCurrentPayModel(userName)
	if err != nil {
		Config.Logger.Printf("Failed to get current pay model for user '%s', unable to check if user is authorized to launch container. Error: %v", userName, err)

		return false, nil
	}

	// "None" is a special `allowedPayModels` value that allows the absence of pay model (aka blanket billing)
	currentPayModelName := "None"
	if currentPayModel != nil {
		currentPayModelName = currentPayModel.Name
	}

	if !stringArrayContains(allowedPayModels, currentPayModelName) {
		Config.Logger.Printf("DEBUG: Pay model '%s' is not allowed for container", currentPayModelName)
		return false, nil // do not return this pay model as an option
	}

	return true, nil
}

var isUserAuthorizedForResourcePaths = func(userName string, accessToken string, resourcePaths []string) (bool, error) {
	Config.Logger.Printf("DEBUG: Checking user '%s' access to resource paths %v (service 'jupyterhub', method 'launch')", userName, resourcePaths)

	body := fmt.Sprintf("{\"user\": {\"token\": \"%s\"}, \"requests\": [", accessToken)
	for _, resource := range resourcePaths {
		body += fmt.Sprintf("{\"resource\": \"%s\", \"action\": {\"service\": \"jupyterhub\", \"method\": \"launch\"}},", resource)
	}
	body = body[:len(body)-1] // remove the last trailing comma
	body += "]}"

	authorized, err := arboristAuthRequest(body)
	if err != nil {
		Config.Logger.Printf("something went wrong when making a call to arborist's `/auth/request` endpoint. Denying access. Details: %v", err.Error())
		return false, nil
	}

	return authorized, nil
}

var arboristAuthRequest = func(body string) (bool, error) {
	arboristUrl := "http://arborist-service/auth/request"
	req, err := http.NewRequest("POST", arboristUrl, bytes.NewBufferString(body))
	if err != nil {
		return false, errors.New("Error occurred while generating HTTP request: " + err.Error())
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("error occurred while making HTTP request: %v", err.Error())
	}
	if resp.StatusCode != 200 {
		return false, fmt.Errorf("arborist returned non-200 code during authorization check: %v", resp.StatusCode)
	}
	defer resp.Body.Close()

	authRequestResponse := new(AuthRequestResponse)
	err = json.NewDecoder(resp.Body).Decode(authRequestResponse)
	if err != nil {
		return false, fmt.Errorf("unable to decode arborist response: %v", err.Error())
	}

	return authRequestResponse.Auth, nil
}
