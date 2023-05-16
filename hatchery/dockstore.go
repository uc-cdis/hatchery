package hatchery

import (
	"fmt"
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
	Image           string
	Name            string
	Environment     []string
	Entrypoint      []string
	Command         []string
	Volumes         []string
	Ports           []string
	UserUID         int64    `yaml:"user_uid"`
	GroupUID        int64    `yaml:"group_uid"`
	FSGID           int64    `yaml:"fs_gid"`
	SecurityContext []string `yaml:"security_context"`
	Deploy          ComposeDeployDetails
	Healthcheck     ComposeHealthCheck
}

// ComposeFull holds all the data harvested from
// a docker-compose.yaml file
type ComposeFull struct {
	// name of the root service mapped to the magic port
	RootService string `yaml:"-"`
	Services    map[string]ComposeService
}

var dslog = log.New(os.Stdout, "hatchery/dockstore", log.LstdFlags)

const userVolumePrefix = "${USER_VOLUME}"
const dataVolumePrefix = "${DATA_VOLUME}"
const sharedMemoryVolumePrefix = "${SHARED_MEMORY_VOLUME}"
const gen3VolumePrefix = "${GEN3_VOLUME}"
const magicPort = "${SERVICE_PORT}" // make it easy to test locally

// DockstoreComposeFromFile loads a hatchery application (container)
// config from a compose.yaml file
func DockstoreComposeFromFile(filePath string) (model *ComposeFull, err error) {
	fileBytes, err := os.ReadFile(filePath)

	if nil != err {
		return nil, err
	}
	return DockstoreComposeFromBytes(fileBytes)
}

// DockstoreComposeFromStr load and sanitize a compose app
// from a given yaml string
func DockstoreComposeFromStr(composeYaml string) (model *ComposeFull, err error) {
	return DockstoreComposeFromBytes([]byte(composeYaml))
}

// DockstoreComposeFromBytes load and sanitize a compose app
// from given yaml bytes
func DockstoreComposeFromBytes(yamlBytes []byte) (model *ComposeFull, err error) {
	model = &ComposeFull{}
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
		// k8s wants DNS-safe container names - let's just do that here
		service.Name = strings.ToLower(key)
		for _, badChar := range [...]string{"_", "/", " "} {
			service.Name = strings.ReplaceAll(service.Name, badChar, "-")
		}
		// some basic validation ...
		if len(service.Image) == 0 {
			return fmt.Errorf("must specify an Image for service %v", key)
		}
		for _, mount := range service.Volumes {
			if !strings.HasPrefix(mount, userVolumePrefix) && !strings.HasPrefix(mount, dataVolumePrefix) && !strings.HasPrefix(mount, gen3VolumePrefix) && !strings.HasPrefix(mount, sharedMemoryVolumePrefix) {
				return fmt.Errorf("illegal volume mount - only support %s, %s, %s and %s mounts: %v", userVolumePrefix, dataVolumePrefix, gen3VolumePrefix, sharedMemoryVolumePrefix, mount)
			}
			mountSlice := strings.SplitN(mount, ":", 2)
			if len(mountSlice) != 2 && !strings.HasPrefix(mount, sharedMemoryVolumePrefix) {
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
		for _, securityContextEntry := range service.SecurityContext {
			kvSlice := strings.SplitN(securityContextEntry, "=", 2)
			if len(kvSlice) != 2 {
				return fmt.Errorf("Could not parse security_context entry: %v", securityContextEntry)
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
				if strings.HasPrefix(portMap, magicPort+":") {
					model.RootService = key
				}
			}
		}
		cleanServices[key] = service
	}
	model.Services = cleanServices
	if len(model.RootService) == 0 {
		return fmt.Errorf("must map exactly one service to port %s", magicPort)
	}
	return nil
}

// BuildK8sResource from a compose resource spec
func (rspec *ComposeResourceSpec) BuildK8sResource() map[k8sv1.ResourceName]resource.Quantity {
	result := make(map[k8sv1.ResourceName]resource.Quantity)
	if rspec.CPU != "" {
		result[k8sv1.ResourceCPU] = resource.MustParse(rspec.CPU)
	}
	if rspec.Memory != "" {
		result[k8sv1.ResourceMemory] = resource.MustParse(rspec.Memory)
	}
	return result
}

// ToK8sContainer copies data from the given service to the container friend
// Returns true if this container mounts the user volume.  We try to avoid
// mounting that thing if possible while it's still EBS based.
func (service *ComposeService) ToK8sContainer(friend *k8sv1.Container) (mountUserVolume bool, mountSharedMemory bool, err error) {
	friend.Name = service.Name
	//friend.CPULimit = service.Deploy.Resources.Limits.CPU
	//friend.MemoryLimit = service.Deploy.Resources.Limits.Memory
	friend.Image = service.Image
	friend.ImagePullPolicy = "Always"
	mountUserVolume = false
	mountSharedMemory = false
	{
		numVolumes := len(service.Volumes)
		if 0 < numVolumes {
			fuseDataPropagation := k8sv1.MountPropagationHostToContainer
			friend.VolumeMounts = make([]k8sv1.VolumeMount, numVolumes)
			volumeMountsIndex := 0
			for _, source := range service.Volumes {
				dest := &friend.VolumeMounts[volumeMountsIndex]
				mountSplit := strings.SplitN(source, ":", 2)
				sourceDrive := mountSplit[0]
				if strings.HasPrefix(sourceDrive, userVolumePrefix) {
					mountUserVolume = true
					dest.MountPath = mountSplit[1]
					if sourceDrive != userVolumePrefix {
						// +1 to trim leading /
						dest.SubPath = sourceDrive[len(userVolumePrefix)+1:]
					}
					dest.Name = "user-data"
					dest.ReadOnly = false
					volumeMountsIndex++
				} else if strings.HasPrefix(sourceDrive, dataVolumePrefix) {
					dest.MountPath = mountSplit[1]
					if sourceDrive != dataVolumePrefix {
						// +1 to trim leading /
						dest.SubPath = sourceDrive[len(dataVolumePrefix)+1:]
					}
					dest.Name = "shared-data"
					dest.ReadOnly = true
					dest.MountPropagation = &fuseDataPropagation
					volumeMountsIndex++
				} else if strings.HasPrefix(sourceDrive, gen3VolumePrefix) {
					dest.MountPath = mountSplit[1]
					if sourceDrive != gen3VolumePrefix {
						// +1 to trim leading /
						dest.SubPath = sourceDrive[len(gen3VolumePrefix)+1:]
					}
					dest.Name = "gen3"
					volumeMountsIndex++
				} else if strings.HasPrefix(sourceDrive, sharedMemoryVolumePrefix) {
					mountSharedMemory = true
				} else {
					return mountUserVolume, mountSharedMemory, fmt.Errorf("Unknown mount point: %v", source)
				}
			}
			friend.VolumeMounts = friend.VolumeMounts[:volumeMountsIndex]
		}
	}

	if nil != service.Environment {
		friend.Env = make([]k8sv1.EnvVar, len(service.Environment))
		for idx, envEntry := range service.Environment {
			kvSlice := strings.SplitN(envEntry, "=", 2)
			if len(kvSlice) != 2 {
				return mountUserVolume, mountSharedMemory, fmt.Errorf("Could not parse environment entry: %v", envEntry)
			}
			friend.Env[idx].Name = kvSlice[0]
			friend.Env[idx].Value = kvSlice[1]
		}
	}

	if nil != service.SecurityContext {
		friend.SecurityContext = &k8sv1.SecurityContext{}
		for _, securityContextEntry := range service.SecurityContext {
			kvSlice := strings.SplitN(securityContextEntry, "=", 2)
			if len(kvSlice) != 2 {
				return mountUserVolume, mountSharedMemory, fmt.Errorf("Could not parse security_context entry: %v", securityContextEntry)
			}
			if kvSlice[0] == "privileged" {
				priv := false
				if kvSlice[1] == "true" {
					priv = true
				}
				friend.SecurityContext.Privileged = &priv
			}
		}
	}

	// ignore service.Ports - only the magic port is mapped at the pod level
	if len(service.Entrypoint) > 0 {
		friend.Command = make([]string, len(service.Entrypoint))
		copy(friend.Command, service.Entrypoint)
	}
	if len(service.Command) > 0 {
		friend.Args = make([]string, len(service.Command))
		copy(friend.Args, service.Command)
	}

	friend.Resources.Limits = service.Deploy.Resources.Limits.BuildK8sResource()
	friend.Resources.Requests = service.Deploy.Resources.Requests.BuildK8sResource()

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

	return mountUserVolume, mountSharedMemory, nil
}

// BuildHatchApp generates a hatchery container config
// from a dockstore compose application config
func (model *ComposeFull) BuildHatchApp() (*Container, error) {
	hatchApp := &Container{}
	service := model.Services[model.RootService]
	hatchApp.Name = service.Name
	hatchApp.CPULimit = service.Deploy.Resources.Limits.CPU
	hatchApp.MemoryLimit = service.Deploy.Resources.Limits.Memory
	hatchApp.UserUID = service.UserUID
	hatchApp.GroupUID = service.GroupUID
	hatchApp.FSGID = service.FSGID
	hatchApp.Image = ""

	for _, portEntry := range service.Ports {
		portSlice := strings.SplitN(portEntry, ":", 2)
		if len(portSlice) != 2 {
			return nil, fmt.Errorf("Could not parse port entry: %v", portEntry)
		}
		if portSlice[0] == magicPort {
			portNum, err := strconv.Atoi(portSlice[1])
			if nil != err {
				return nil, fmt.Errorf("failed to parse port source as number: %v", portEntry)
			}
			hatchApp.TargetPort = int32(portNum)
			break
		}
	}

	//
	// nginx removes the /lw-workspace/proxy prefix,
	// this rewrite puts it back in
	//
	hatchApp.PathRewrite = "/lw-workspace/proxy/"
	hatchApp.ReadyProbe = "" // "/lw-workspace/proxy/"
	hatchApp.UseTLS = "false"

	numServices := len(model.Services)
	if numServices < 1 {
		return nil, fmt.Errorf("no services found in compose model")
	}
	hatchApp.Friends = make([]k8sv1.Container, numServices)
	friendIndex := 0
	mountUserVolume := false // does this app mount the user volume?
	mountSharedMemory := false
	for _, service := range model.Services {
		usesUserVolume, useSharedMemory, err := service.ToK8sContainer(&hatchApp.Friends[friendIndex])
		if nil != err {
			return nil, err
		}
		mountUserVolume = mountUserVolume || usesUserVolume
		mountSharedMemory = mountSharedMemory || useSharedMemory
		friendIndex++
	}
	if mountUserVolume {
		// pods.go defines the k8s volume for the user space if this variable is set ...
		hatchApp.UserVolumeLocation = "/dockstore/paceholder"
	}
	if mountSharedMemory {
		hatchApp.UseSharedMemory = "true"
	}
	if hatchApp.Gen3VolumeLocation == "" {
		hatchApp.Gen3VolumeLocation = "/.gen3"
	}
	return hatchApp, nil
}
