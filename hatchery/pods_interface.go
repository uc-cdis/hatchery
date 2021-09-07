package hatchery

import (
  "fmt"
  "crypto/md5"
  "context"
  //k8sv1 "k8s.io/api/core/v1"
)


var (
	trueVal  = true
	falseVal = false
)

const (
	LABEL_USER  = "gen3username"
	LABEL_POD   = "app"
	LABEL_APPID = "app-id"
)

/*
type PodConditions struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type ContainerStates struct {
	Name  string               `json:"name"`
	State k8sv1.ContainerState `json:"state"`
	Ready bool                 `json:"ready"`
}

type WorkspaceStatus struct {
	Status          string            `json:"status"`
	Conditions      []PodConditions   `json:"conditions"`
	ContainerStates []ContainerStates `json:"containerStates"`
}
*/

type WorkspaceManager interface {
  Create(ctx context.Context, hash string, accessToken string, userName string)
}

func getBaseName(userName string, appID string) string {
	x := md5.Sum([]byte(userName))
	return fmt.Sprintf("%x-%s", x[0:8], []byte(appID)[0:8] )
}


// userToResourceName is a helper for generating names for
// different types of kubernetes resources given a user name
// and a resource type
func userToResourceName(userName string, resourceType string) string {
	safeUserName := escapism(userName)
	if resourceType == "pod" {
		return fmt.Sprintf("hatchery-%s", safeUserName)
	}
	if resourceType == "service" {
		return fmt.Sprintf("h-%s-s", safeUserName)
	}
	if resourceType == "mapping" { // ambassador mapping
		return fmt.Sprintf("%s-mapping", safeUserName)
	}

	return fmt.Sprintf("%s-%s", resourceType, safeUserName)
}
