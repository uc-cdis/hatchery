package hatchery

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"
	k8sv1 "k8s.io/api/core/v1"
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
			mountSlice := strings.SplitN(mount, ":", 2)
			if len(mountSlice) != 2 {
				return nil, fmt.Errorf("illegal volume mount: %v", mount)
			}
		}
		for i, rspec := range []*ComposeResourceSpec{&service.Deploy.Resources.Requests, &service.Deploy.Resources.Limits} {
			if rspec.Memory == "" {
				rspec.Memory = fmt.Sprintf("%vMi", (i+1)*256)
			}
			if rspec.CPU == "" {
				rspec.CPU = fmt.Sprintf("%v", float32(i+1)*0.8)
			}
		}
		for _, envEntry := range service.Environment {
			kvSlice := strings.SplitN(envEntry, "=", 2)
			if len(kvSlice) != 2 {
				return nil, fmt.Errorf("Could not parse environment entry: %v", envEntry)
			}
		}
		for _, portEntry := range service.Ports {
			portSlice := strings.SplitN(portEntry, ":", 2)
			if len(portSlice) != 2 {
				return nil, fmt.Errorf("Could not parse port entry: %v", portEntry)
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

// DockstoreComposeTranslate generates a hatchery container config
// from a dockstore compose application config
func DockstoreComposeTranslate(composeModel *ComposeFull) (*Container, error) {
	hatchApp := &Container{}
	service := composeModel.Services[composeModel.RootService]
	hatchApp.Name = service.Name
	hatchApp.CPULimit = service.Deploy.Resources.Limits.CPU
	hatchApp.MemoryLimit = service.Deploy.Resources.Limits.CPU
	hatchApp.Image = service.Image
	hatchApp.PullPolicy = "Always"
	hatchApp.Env = make(map[string]string)
	if nil != service.Environment {
		for _, envEntry := range service.Environment {
			kvSlice := strings.SplitN(envEntry, "=", 2)
			if len(kvSlice) != 2 {
				return nil, fmt.Errorf("Could not parse environment entry: %v", envEntry)
			}
			hatchApp.Env[kvSlice[0]] = kvSlice[1]
		}
	}

	for _, portEntry := range service.Ports {
		portSlice := strings.SplitN(portEntry, ":", 2)
		if len(portSlice) != 2 {
			return nil, fmt.Errorf("Could not parse port entry: %v", portEntry)
		}
		if portSlice[1] == "80" {
			portNum, err := strconv.Atoi(portSlice[0])
			if nil != err {
				return nil, fmt.Errorf("failed to parse port source as number: %v", portEntry)
			}
			hatchApp.TargetPort = int32(portNum)
			break
		}
	}

	if len(service.Entrypoint) > 0 {
		hatchApp.Command = make([]string, len(service.Entrypoint))
		copy(hatchApp.Command, service.Entrypoint)
	}
	if len(service.Command) > 0 {
		hatchApp.Args = make([]string, len(service.Command))
		copy(hatchApp.Args, service.Command)
	}
	for _, mount := range service.Volumes {
		if strings.HasPrefix(mount, "user-volume/") {
			mountSlice := strings.SplitN(mount, ":", 2)
			if len(mountSlice) != 2 {
				return nil, fmt.Errorf("failed to parse user-volume mapping: %v", mount)
			}
			hatchApp.UserVolumeLocation = mountSlice[1]
		}
	}
	hatchApp.PathRewrite = "/lw-workspace/proxy/"
	hatchApp.ReadyProbe = "/lw-workspace/proxy/"
	hatchApp.UseTLS = "false"

	numServices := len(composeModel.Services)
	if numServices > 1 {
		hatchApp.Friends = make([]k8sv1.Container, numServices-1)
		friendIndex := 0
		for k := range composeModel.Services {
			if k != composeModel.RootService {
				friendIndex++
			}
		}
	}
	return hatchApp, nil
}
