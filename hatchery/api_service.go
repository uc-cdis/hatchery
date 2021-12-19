package hatchery

import (
	"context"
	"fmt"
	"strings"

	"github.com/uc-cdis/hatchery/hatchery/openapi"
)

type HatcheryAPIService struct {
}

func readBearerToken(authHeader string) string {
	if authHeader == "" {
		return ""
	}
	s := strings.SplitN(authHeader, " ", 2)
	if len(s) == 2 && strings.ToLower(s[0]) == "bearer" {
		return s[1]
	}
	return ""
}

func doLaunchEcs(ctx context.Context, id string, userName string, authorization string) error {
	accessToken := readBearerToken(authorization)
	if payModel, err := getPayModelForUser(userName); err == nil {
		err := launchEcsWorkspace(ctx, userName, id, accessToken, *payModel)
		if err != nil {
			Config.Logger.Printf("Error: %s", err)
			return err
		}
	} else {
		return fmt.Errorf("Paymodel has not been setup for user")
	}
	return nil
}

func NewAPIService() (*HatcheryAPIService, error) {
	return &HatcheryAPIService{}, nil
}

func (s *HatcheryAPIService) Launch(ctx context.Context, id string, userName string, authorization string) (openapi.ImplResponse, error) {
	accessToken := readBearerToken(authorization)
	if userName == "" {
		return openapi.Response(500, openapi.Status{Status: "Missing REMOTE_USER header"}), nil
	}

	payModel, err := getPayModelForUser(userName)
	if err != nil {
		Config.Logger.Printf(err.Error())
	}
	if payModel == nil {
		err = createLocalK8sPod(ctx, id, userName, accessToken)
	} else if payModel.Ecs == "true" {
		err = launchEcsWorkspace(ctx, userName, id, accessToken, *payModel)
	} else {
		err = createExternalK8sPod(ctx, id, userName, accessToken, *payModel)
	}
	if err != nil {
		return openapi.Response(500, openapi.Status{Status: "Error"}), err
	}
	return openapi.Response(200, openapi.Status{Id: id, Status: "Launching"}), nil
}

func (s *HatcheryAPIService) Options(ctx context.Context, userName string, authorization string) (openapi.ImplResponse, error) {
	options := []openapi.Container{}
	for k, v := range Config.ContainersMap {
		c := openapi.Container{
			Name:        v.Name,
			CpuLimit:    v.CPULimit,
			MemoryLimit: v.MemoryLimit,
			Id:          k,
		}
		options = append(options, c)
	}
	return openapi.Response(200, options), nil
}

func (s *HatcheryAPIService) Status(ctx context.Context, userName string, authorization string) (openapi.ImplResponse, error) {
	if userName == "" {
		return openapi.Response(500, openapi.Status{Status: "Missing REMOTE_USER header"}), nil
	}
	accessToken := readBearerToken(authorization)
	payModel, err := getPayModelForUser(userName)

	if err != nil {
		Config.Logger.Printf(err.Error())
	}
	var result *WorkspaceStatus
	if payModel != nil && payModel.Ecs == "true" {
		result, err = statusEcs(ctx, userName, accessToken, payModel.AWSAccountId)
	} else {
		result, err = statusK8sPod(ctx, userName, accessToken, payModel)
	}
	if err != nil {
		return openapi.Response(500, openapi.Status{Status: err.Error()}), nil

	}
	out := workspaceState2OpenAPI(*result)
	return openapi.Response(200, out), nil
}

func (s *HatcheryAPIService) Paymodels(ctx context.Context, userName string) (openapi.ImplResponse, error) {
	payModel, err := getPayModelForUser(userName)
	if payModel == nil {
		return openapi.Response(404, openapi.Status{Status: "Not found"}), nil
	}
	if err != nil {
		return openapi.Response(500, openapi.Status{Status: "Error"}), err
	}
	return openapi.Response(200, openapi.PayModel{
		Name:         payModel.Name,
		User:         payModel.User,
		AwsAccountId: payModel.AWSAccountId,
		Region:       payModel.Region,
		Ecs:          payModel.Ecs,
	}), nil
}

func (s *HatcheryAPIService) Terminate(ctx context.Context, userName string, authorization string, workspaceID string) (openapi.ImplResponse, error) {
	accessToken := readBearerToken(authorization)
	payModel, err := getPayModelForUser(userName)
	if err != nil {
		Config.Logger.Printf(err.Error())
	}
	if payModel != nil && payModel.Ecs == "true" {
		svc, err := terminateEcsWorkspace(ctx, userName, accessToken, payModel.AWSAccountId)
		if err != nil {
			return openapi.Response(500, nil), nil
		} else {
			return openapi.Response(200, openapi.Status{Id: svc, Status: "Stopped"}), err
		}
	} else {
		err := deleteK8sPod(ctx, userName, accessToken, payModel)
		if err != nil {
			return openapi.Response(500, nil), nil
		}
	}
	return openapi.Response(200, nil), nil
}
