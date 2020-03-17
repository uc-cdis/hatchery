package hatchery

import (
	k8sv1 "k8s.io/api/core/v1"

	"crypto/md5"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
)

// Container Struct to hold the configuration for Pod Container
type Container struct {
	Name               string            `json:"name"`
	CPULimit           string            `json:"cpu-limit"`
	MemoryLimit        string            `json:"memory-limit"`
	Image              string            `json:"image"`
	PullPolicy         string            `json:"pull_policy"`
	Env                map[string]string `json:"env"`
	TargetPort         int32             `json:"target-port"`
	Args               []string          `json:"args"`
	Command            []string          `json:"command"`
	PathRewrite        string            `json:"path-rewrite"`
	UseTLS             string            `json:"use-tls"`
	ReadyProbe         string            `json:"ready-probe"`
	LifecyclePreStop   []string          `json:"lifecycle-pre-stop"`
	LifecyclePostStart []string          `json:"lifecycle-post-start"`
	UserUID            int64             `json:"user-uid"`
	GroupUID           int64             `json:"group-uid"`
	FSGID              int64             `json:"fs-gid"`
	UserVolumeLocation string            `json:"user-volume-location"`
	Friends            []k8sv1.Container `json:"friends"`
}

// SidecarContainer holds fuse sidecar configuration
type SidecarContainer struct {
	CPULimit         string            `json:"cpu-limit"`
	MemoryLimit      string            `json:"memory-limit"`
	Image            string            `json:"image"`
	Env              map[string]string `json:"env"`
	Args             []string          `json:"args"`
	Command          []string          `json:"command"`
	LifecyclePreStop []string          `json:"lifecycle-pre-stop"`
}

// AppConfigInfo provides the type and path of a supplementary config path
type AppConfigInfo struct {
	AppType string `json:"type"`
	Path    string
	Name    string
}

// HatcheryConfig is the root of all the configuration
type HatcheryConfig struct {
	UserNamespace  string           `json:"user-namespace"`
	SubDir         string           `json:"sub-dir"`
	Containers     []Container      `json:"containers"`
	UserVolumeSize string           `json:"user-volume-size"`
	Sidecar        SidecarContainer `json:"sidecar"`
	MoreConfigs    []AppConfigInfo  `json:"more-configs"`
}

// FullHatcheryConfig bucket result from loadConfig
type FullHatcheryConfig struct {
	Config        HatcheryConfig
	ContainersMap map[string]Container
	Logger        *log.Logger
}

// LoadConfig from a json file
func LoadConfig(configFilePath string, loggerIn *log.Logger) (config *FullHatcheryConfig, err error) {
	logger := loggerIn
	if nil == loggerIn {
		logger = log.New(os.Stdout, "", log.LstdFlags)
	}
	plan, err := ioutil.ReadFile(configFilePath)

	data := &FullHatcheryConfig{
		Logger: logger,
	}

	if nil != err {
		cwd, _ := os.Getwd()
		data.Logger.Printf("failed to load %v from cwd %v got - %v", configFilePath, cwd, err)
		return data, err
	}
	data.Logger.Printf("loaded config: %v", string(plan))
	data.ContainersMap = make(map[string]Container)
	_ = json.Unmarshal(plan, &data.Config)
	if nil != data.Config.MoreConfigs && 0 < len(data.Config.MoreConfigs) {
		for _, info := range data.Config.MoreConfigs {
			if info.AppType == "dockstore-compose:1.0.0" {
				if "" == info.Name {
					return nil, fmt.Errorf("Empty name for more-configs app at: %v", info.Path)
				}
				data.Logger.Printf("loading config from %v", info.Path)
				composeModel, err := DockstoreComposeFromFile(info.Path)
				if nil != err {
					data.Logger.Printf("failed to load config from %v, got: %v", info.Path, err)
					return nil, err
				}
				hatchApp, err := composeModel.BuildHatchApp()
				hatchApp.Name = info.Name
				if nil != err {
					data.Logger.Printf("failed to translate app, got: %v", err)
					return nil, err
				}
				data.Config.Containers = append(data.Config.Containers, *hatchApp)
			} else {
				data.Logger.Printf("ignoring config of unsupported type: %v", info.AppType)
			}
		}
	}
	for _, container := range data.Config.Containers {
		toHash := container.Name + "-" + container.Image + "-" + container.CPULimit + "-" + container.MemoryLimit
		hash := fmt.Sprintf("%x", md5.Sum([]byte(toHash)))
		data.ContainersMap[hash] = container
	}
	return data, nil
}
