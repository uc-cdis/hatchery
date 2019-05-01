package handlers

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io/ioutil"
)

// Container Struct to hold the configuration for Pod Container
type Container struct {
	Name        string            `json:"name"`
	CPULimit    string            `json:"cpu-limit"`
	MemoryLimit string            `json:"memory-limit"`
	Image       string            `json:"image"`
	Env         map[string]string `json:"env"`
	TargetPort  int32             `json:"target-port"`
	Args        []string          `json:"args"`
	Command     []string          `json:"command"`
	PathRewrite string            `json:"path-rewrite"`
	UseTLS 		string            `json:"use-tls"`
}

// HatcheryConfig Struct to hold all the configuration
type HatcheryConfig struct {
	Name          string      `json:"name"`
	UserNamespace string      `json:"user-namespace"`
	SubDir        string      `json:"sub-dir"`
	Containers    []Container `json:"containers"`
}

type FullHatcheryConfig struct {
	Config        HatcheryConfig
	ContainersMap map[string]Container
}

func loadConfig(config string) FullHatcheryConfig {
	plan, _ := ioutil.ReadFile(config)
	var data FullHatcheryConfig
	data.ContainersMap = make(map[string]Container)
	_ = json.Unmarshal(plan, &data.Config)
	for _, container := range data.Config.Containers {
		toHash := container.Image + "-" + container.CPULimit + "-" + container.MemoryLimit
		hash := fmt.Sprintf("%x", md5.Sum([]byte(toHash)))
		data.ContainersMap[hash] = container
	}
	return data
}
