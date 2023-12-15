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

// Configuration specific to Nextflow containers
type NextflowConfig struct {
	Enabled           bool     `json:"enabled"`
	JobImageWhitelist []string `json:"job-image-whitelist"`
	S3BucketWhitelist []string `json:"s3-bucket-whitelist"`
	InstanceAMI       string   `json:"instance-ami"`
	InstanceType      string   `json:"instance-type"`
	InstanceMinVCpus  int32    `json:"instance-min-vcpus"`
	InstanceMaxVCpus  int32    `json:"instance-max-vcpus"`
}

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
	Gen3VolumeLocation string            `json:"gen3-volume-location"`
	UseSharedMemory    string            `json:"use-shared-memory"`
	Friends            []k8sv1.Container `json:"friends"`
	NextflowConfig     NextflowConfig    `json:"nextflow"`
	Authz              AuthzConfig       `json:"authz"`
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

// TODO remove PayModel from config once DynamoDB contains all necessary data
type PayModel struct {
	Id              string  `json:"bmh_workspace_id"`
	Name            string  `json:"workspace_type"`
	User            string  `json:"user_id"`
	AWSAccountId    string  `json:"account_id"`
	Status          string  `json:"request_status"`
	Local           bool    `json:"local"`
	Region          string  `json:"region"`
	Ecs             bool    `json:"ecs"`
	Subnet          int     `json:"subnet"`
	HardLimit       float32 `json:"hard-limit"`
	SoftLimit       float32 `json:"soft-limit"`
	TotalUsage      float32 `json:"total-usage"`
	CurrentPayModel bool    `json:"current_pay_model"`
}

type AllPayModels struct {
	CurrentPayModel *PayModel  `json:"current_pay_model"`
	PayModels       []PayModel `json:"all_pay_models"`
}

// HatcheryConfig is the root of all the configuration
type HatcheryConfig struct {
	UserNamespace          string           `json:"user-namespace"`
	DefaultPayModel        PayModel         `json:"default-pay-model"`
	DisableLocalWS         bool             `json:"disable-local-ws"`
	PayModels              []PayModel       `json:"pay-models"`
	PayModelsDynamodbTable string           `json:"pay-models-dynamodb-table"`
	Gen3UserLicenseTable   string           `json:"gen3-user-license-dynamodb-table"`
	SubDir                 string           `json:"sub-dir"`
	Containers             []Container      `json:"containers"`
	UserVolumeSize         string           `json:"user-volume-size"`
	Sidecar                SidecarContainer `json:"sidecar"`
	MoreConfigs            []AppConfigInfo  `json:"more-configs"`
	PrismaConfig           PrismaConfig     `json:"prisma"`
}

// Config to allow for Prisma Agents
type PrismaConfig struct {
	ConsoleAddress string `json:"console-address"`
	Enable         bool   `json:"enable"`
}

// FullHatcheryConfig bucket result from loadConfig
type FullHatcheryConfig struct {
	Config        HatcheryConfig
	ContainersMap map[string]Container
	PayModelMap   map[string]PayModel
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
	data.PayModelMap = make(map[string]PayModel)
	err = json.Unmarshal(plan, &data.Config)
	if nil != err {
		data.Logger.Printf("Unable to unmarshal configuration: %v", err)
		return nil, err
	}
	if nil != data.Config.MoreConfigs && 0 < len(data.Config.MoreConfigs) {
		for _, info := range data.Config.MoreConfigs {
			if info.AppType == "dockstore-compose:1.0.0" {
				if info.Name == "" {
					return nil, fmt.Errorf("empty name for more-configs app at: %v", info.Path)
				}
				data.Logger.Printf("loading config from %v", info.Path)
				composeModel, err := DockstoreComposeFromFile(info.Path)
				if nil != err {
					data.Logger.Printf("failed to load config from %v, got: %v", info.Path, err)
					return nil, err
				}
				data.Logger.Printf("%v", composeModel)
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
		err = ValidateAuthzConfig(data.Logger, container.Authz)
		if nil != err {
			data.Logger.Printf("Container '%s' has an invalid 'authz' configuration: %v", container.Name, err)
			return nil, err
		}
		jsonBytes, _ := json.Marshal(container)
		hash := fmt.Sprintf("%x", md5.Sum([]byte(jsonBytes)))
		data.ContainersMap[hash] = container
	}

	if data.Config.PayModelsDynamodbTable == "" {
		data.Logger.Printf("Warning: no 'pay-models-dynamodb-table' in configuration: will be unable to query pay model data in DynamoDB")
	}

	for _, payModel := range data.Config.PayModels {
		user := payModel.User
		data.PayModelMap[user] = payModel
	}

	if data.Config.Gen3UserLicenseTable == "" {
		data.Logger.Printf("Warning: no 'gen3-user-license-dynamodb-table' in configuration: will be unable to store gen3-user-license data in DynamoDB")
	}

	return data, nil
}
