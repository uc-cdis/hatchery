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
	"k8s.io/apimachinery/pkg/api/resource"
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

const userVolumePrefix = "user-volume/"
const dataVolumePrefix = "data-volume/"

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
	return model, model.Sanitize()
}

// Sanitize scans, validates, and decorates a given ComposeFull model
func (model *ComposeFull) Sanitize() error {
	cleanServices := make(map[string]ComposeService, len(model.Services))
	for key, service := range model.Services {
		service.Name = key
		// some basic validation ...
		if len(service.Image) == 0 {
			return fmt.Errorf("must specify an Image for service %v", key)
		}
		for _, mount := range service.Volumes {
			if !strings.HasPrefix(mount, userVolumePrefix) && !strings.HasPrefix(mount, dataVolumePrefix) {
				return fmt.Errorf("illegal volume mount - only support user-volume/ and data-volume/ mounts: %v", mount)
			}
			mountSlice := strings.SplitN(mount, ":", 2)
			if len(mountSlice) != 2 {
				return fmt.Errorf("illegal volume mount: %v", mount)
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
				return fmt.Errorf("Could not parse environment entry: %v", envEntry)
			}
		}
		for _, portEntry := range service.Ports {
			portSlice := strings.SplitN(portEntry, ":", 2)
			if len(portSlice) != 2 {
				return fmt.Errorf("Could not parse port entry: %v", portEntry)
			}
		}
		if model.RootService == "" {
			for _, portMap := range service.Ports {
				if strings.HasSuffix(portMap, ":80") {
					model.RootService = key
				}
			}
		}
		cleanServices[key] = service
	}
	model.Services = cleanServices
	if len(model.RootService) == 0 {
		return fmt.Errorf("must map exactly one service to port :80")
	}
	return nil
}

// ToK8sContainer copies data from the given service to the container friend
func (service *ComposeService) ToK8sContainer(friend *k8sv1.Container) error {
	friend.Name = service.Name
	//friend.CPULimit = service.Deploy.Resources.Limits.CPU
	//friend.MemoryLimit = service.Deploy.Resources.Limits.Memory
	friend.Image = service.Image
	friend.ImagePullPolicy = "Always"
	{
		numVolumes := len(service.Volumes)
		if 0 < numVolumes {
			fuseDataPropogation := k8sv1.MountPropagationHostToContainer
			friend.VolumeMounts = make([]k8sv1.VolumeMount, numVolumes)
			for idx, source := range service.Volumes {
				dest := &friend.VolumeMounts[idx]
				mountSplit := strings.SplitN(source, ":", 2)
				sourceDrive := mountSplit[0]
				if strings.HasPrefix(sourceDrive, userVolumePrefix) {
					dest.MountPath = mountSplit[1]
					if sourceDrive != userVolumePrefix {
						dest.SubPath = sourceDrive[len(userVolumePrefix):]
					}
					dest.Name = "user-data"
					dest.ReadOnly = false
				} else if strings.HasPrefix(sourceDrive, dataVolumePrefix) {
					dest.MountPath = mountSplit[1]
					if sourceDrive != dataVolumePrefix {
						dest.SubPath = sourceDrive[len(dataVolumePrefix):]
					}
					dest.Name = "shared-data"
					dest.ReadOnly = true
					dest.MountPropagation = &fuseDataPropogation
				} else {
					return fmt.Errorf("Unknown mount point: %v", source)
				}
			}
		}
	}

	if nil != service.Environment {
		friend.Env = make([]k8sv1.EnvVar, len(service.Environment))
		for idx, envEntry := range service.Environment {
			kvSlice := strings.SplitN(envEntry, "=", 2)
			if len(kvSlice) != 2 {
				return fmt.Errorf("Could not parse environment entry: %v", envEntry)
			}
			friend.Env[idx].Name = kvSlice[0]
			friend.Env[idx].Value = kvSlice[1]
		}
	}

	// ignore service.Ports - only port 80 is mapped at the pod level
	if len(service.Entrypoint) > 0 {
		friend.Command = make([]string, len(service.Entrypoint))
		copy(friend.Command, service.Entrypoint)
	}
	if len(service.Command) > 0 {
		friend.Args = make([]string, len(service.Command))
		copy(friend.Args, service.Command)
	}

	friend.Resources.Limits = make(map[k8sv1.ResourceName]resource.Quantity)
	friend.Resources.Requests = make(map[k8sv1.ResourceName]resource.Quantity)
	if "" != service.Deploy.Resources.Limits.CPU {
		friend.Resources.Limits[k8sv1.ResourceCPU] = resource.MustParse(service.Deploy.Resources.Limits.CPU)
		friend.Resources.Requests[k8sv1.ResourceCPU] = resource.MustParse(service.Deploy.Resources.Limits.CPU)
	}
	if "" != service.Deploy.Resources.Limits.Memory {
		friend.Resources.Limits[k8sv1.ResourceMemory] = resource.MustParse(service.Deploy.Resources.Requests.Memory)
		friend.Resources.Requests[k8sv1.ResourceMemory] = resource.MustParse(service.Deploy.Resources.Requests.Memory)
	}
	if 1 < len(service.Healthcheck.Test) && service.Healthcheck.Test[0] == "CMD" {
		friend.ReadinessProbe = &k8sv1.Probe{
			Handler: k8sv1.Handler{
				Exec: &k8sv1.ExecAction{
					Command: service.Healthcheck.Test[1:],
				},
			},
			// too lazy to parse docker-compose time specs
			InitialDelaySeconds: int32(10),
			PeriodSeconds:       int32(30),
			TimeoutSeconds:      int32(10),
		}
		friend.LivenessProbe = friend.ReadinessProbe
	}

	return nil
}

// BuildHatchApp generates a hatchery container config
// from a dockstore compose application config
func (composeModel *ComposeFull) BuildHatchApp() (*Container, error) {
	hatchApp := &Container{}
	service := composeModel.Services[composeModel.RootService]
	hatchApp.Name = service.Name
	hatchApp.CPULimit = service.Deploy.Resources.Limits.CPU
	hatchApp.MemoryLimit = service.Deploy.Resources.Limits.Memory
	hatchApp.Image = ""

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

	hatchApp.PathRewrite = "/lw-workspace/proxy/"
	hatchApp.ReadyProbe = "/lw-workspace/proxy/"
	hatchApp.UseTLS = "false"

	numServices := len(composeModel.Services)
	if numServices < 1 {
		return nil, fmt.Errorf("no services found in compose model")
	}
	hatchApp.Friends = make([]k8sv1.Container, numServices)
	friendIndex := 0
	for _, service := range composeModel.Services {
		service.ToK8sContainer(&hatchApp.Friends[friendIndex])
		friendIndex++
	}

	return hatchApp, nil
}
