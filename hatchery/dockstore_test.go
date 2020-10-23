package hatchery

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
)

func TestDockstoreComposeLoad(t *testing.T) {
	path := "../testData/dockstore/docker-compose.yml"
	composeModel, err := DockstoreComposeFromFile(path)
	if nil != err {
		t.Error(fmt.Sprintf("failed to load config from %v, got: %v", path, err))
		return
	}
	if nil == composeModel {
		t.Error("nil model from DockstoreComposeFromFile?")
		return
	}
	composeBytes, _ := yaml.Marshal(composeModel)
	dslog.Printf("loaded composes services: %v", string(composeBytes))
	if count := len(composeModel.Services); count != 5 {
		t.Error(fmt.Sprintf("Unexpected number of services: %v", count))
	}
	service, ok := composeModel.Services["viewer"]
	if !ok {
		t.Error("viewer service not loaded")
	}
	if len(service.Environment) != 2 {
		t.Error(fmt.Sprintf("viewer does not have expected environment: %v", service.Environment))
	}

	service, ok = composeModel.Services["mon_Go"]
	if !ok {
		t.Error("mon_Go service not loaded")
		return
	}
	if "" == service.Deploy.Resources.Limits.Memory || "" == service.Deploy.Resources.Limits.CPU {
		t.Error("mon_Go service failed to load resource limits")
	}
	if "" == service.Deploy.Resources.Requests.Memory || "" == service.Deploy.Resources.Requests.CPU {
		t.Error("mon_Go service failed to load resource limits")
	}
	if "mon-go" != service.Name { // DNS safe name
		t.Error(fmt.Sprintf("mon_Go service has wrong name: %v", service.Name))
	}

	service, ok = composeModel.Services["cloudtop"]
	if !ok {
		t.Error("cloudtop service not loaded")
	}
	if len(service.Healthcheck.Test) != 4 {
		t.Error(fmt.Sprintf("unexpected health check %v", strings.Join(service.Healthcheck.Test, " ")))
	}

	if composeModel.RootService != "viewer" {
		t.Error(fmt.Sprintf("expected viewer root service, got %v", composeModel.RootService))
	}
}

func TestDockstoreComposeTranslate(t *testing.T) {
	path := "../testData/dockstore/docker-compose.yml"
	composeModel, err := DockstoreComposeFromFile(path)
	if nil != err {
		t.Error(fmt.Sprintf("failed to load config from %v, got: %v", path, err))
		return
	}
	if nil == composeModel {
		t.Error("nil model from DockstoreComposeFromFile?")
		return
	}
	hatchApp, err := composeModel.BuildHatchApp()
	if nil != err {
		t.Error(fmt.Sprintf("failed to translate app, got: %v", err))
		return
	}
	if "" == hatchApp.UserVolumeLocation {
		t.Error(fmt.Sprintf("dockstore hatchApp should set UserVolumeLocation property"))
		return
	}
	hatchAppBytes, _ := yaml.Marshal(hatchApp)
	dslog.Printf("translated hatchery app: %v", string(hatchAppBytes))
}

func TestFirefoxAppTranslate(t *testing.T) {
	path := "../testData/dockstore/firefox-app.yml"
	composeModel, err := DockstoreComposeFromFile(path)
	if nil != err {
		t.Error(fmt.Sprintf("failed to load config from %v, got: %v", path, err))
		return
	}
	if nil == composeModel {
		t.Error("nil model from DockstoreComposeFromFile?")
		return
	}
	hatchApp, err := composeModel.BuildHatchApp()
	if nil != err {
		t.Error(fmt.Sprintf("failed to translate app, got: %v", err))
		return
	}
	if "true" != hatchApp.UseSharedMemory {
		t.Error(fmt.Sprintf("dockstore Firefox hatchApp should set UseSharedMemory property to \"true\""))
		return
	}
	hatchAppBytes, _ := yaml.Marshal(hatchApp)
	dslog.Printf("translated hatchery app: %v", string(hatchAppBytes))
}
