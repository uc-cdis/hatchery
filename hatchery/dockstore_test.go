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

	service, ok = composeModel.Services["mongo"]
	if !ok {
		t.Error("mongo service not loaded")
	}
	if "" == service.Deploy.Resources.Limits.Memory || "" == service.Deploy.Resources.Limits.CPU {
		t.Error("mongo service failed to load resource limits")
	}
	if "" == service.Deploy.Resources.Requests.Memory || "" == service.Deploy.Resources.Requests.CPU {
		t.Error("mongo service failed to load resource limits")
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
