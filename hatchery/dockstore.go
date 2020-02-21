package hatchery

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v2"
)

// ComposeResourceSpec holds the cpu and memory values
// for a resource request
type ComposeResourceSpec struct {
	Memory string
	CPU    string `yaml:"cpus,omitempty"`
}

// ComposeResources holds the resource requests and limits
// for a service (contianer)
type ComposeResources struct {
	Requests ComposeResourceSpec `yaml:"reservations,omitempty"`
	Limits   ComposeResourceSpec
}

// ComposeDeployDetails holds supplemental information useful
// for scheduling a service
type ComposeDeployDetails struct {
	Resources ComposeResources
}

// ComposeHealthCheck holds the healthcheck details for a service
type ComposeHealthCheck struct {
	Test     []string
	Interval string
	Timeout  string
	Retries  int
}

// ComposeService is an entry in the services
// block of docker-compose
type ComposeService struct {
	Image       string
	Name        string
	Environment []string
	Entrypoint  []string
	Command     []string
	Volumes     []string
	Ports       []string
	Deploy      ComposeDeployDetails
	Healthcheck ComposeHealthCheck
}

// ComposeFull holds all the data harvested from
// a docker-compose.yaml file
type ComposeFull struct {
	// name of the root service mapped to port 80
	RootService string `yaml:"-"`
	Services    map[string]ComposeService
}

var dslog = log.New(os.Stdout, "hatchery/dockstore", log.LstdFlags)

// DockstoreComposeFromFile loads a hatchery application (container)
// config from a compose.yaml file
func DockstoreComposeFromFile(filePath string) (composeModel *ComposeFull, err error) {
	fileBytes, err := ioutil.ReadFile(filePath)

	if nil != err {
		return nil, err
	}
	return DockstoreComposeFromBytes(fileBytes)
}

// DockstoreComposeFromStr load and sanitize a compose app
// from a given yaml string
func DockstoreComposeFromStr(composeYaml string) (composeModel *ComposeFull, err error) {
	return DockstoreComposeFromBytes([]byte(composeYaml))
}

// DockstoreComposeFromBytes load and sanitize a compose app
// from given yaml bytes
func DockstoreComposeFromBytes(yamlBytes []byte) (composeModel *ComposeFull, err error) {
	model := &ComposeFull{}
	err = yaml.Unmarshal(yamlBytes, model)
	if nil != err {
		return nil, err
	}
	return DockstoreComposeSanitize(model)
}

// DockstoreComposeSanitize scans, validates, and decorates a given ComposeFull model
func DockstoreComposeSanitize(model *ComposeFull) (*ComposeFull, error) {
	for key, service := range model.Services {
		service.Name = key
		// some basic validation ...
		if len(service.Image) == 0 {
			return nil, fmt.Errorf("must specify an Image for service %v", key)
		}
		for _, mount := range service.Volumes {
			if !strings.HasPrefix(mount, "user-volume/") && !strings.HasPrefix(mount, "data-volume/") {
				return nil, fmt.Errorf("illegal volume mount - only support user-volume/ and data-volume/ mounts: %v", mount)
			}
		}
		if model.RootService == "" {
			for _, portMap := range service.Ports {
				if strings.HasSuffix(portMap, ":80") {
					model.RootService = key
				}
			}
		}
	}
	if len(model.RootService) == 0 {
		return nil, fmt.Errorf("must map exactly one service to port :80")
	}
	return model, nil
}
