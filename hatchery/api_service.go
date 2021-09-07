package hatchery

import (
  "fmt"
  "strings"
  "context"
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

func doLaunchEcs(ctx context.Context, id string, userName string, authorization string) (error) {
	accessToken := readBearerToken(authorization)
	if payModelExistsForUser(userName) {
		_, err := launchEcsWorkspace(ctx, userName, id, accessToken)
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
    return openapi.Response(500, openapi.WorkspaceStatus{Status:"Missing REMOTE_USER header"}), nil
  }
  pm := Config.PayModelMap[userName]
  if pm.Ecs == "true" {
    doLaunchEcs(ctx, id, userName, authorization)
  } else {
    err := createK8sPod(ctx, id, accessToken, userName)
    if err != nil {
      return openapi.Response(500, openapi.WorkspaceStatus{Status:err.Error()}), nil
    }
  }
  return openapi.Response(200, openapi.WorkspaceStatus{Status:"Launching"}), nil
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
  stats := []openapi.WorkspaceStatus{}
  accessToken := readBearerToken(authorization)
  if userName == "" {
    return openapi.Response(500, openapi.WorkspaceStatus{Status:"Missing REMOTE_USER header"}), nil
  }
	pm := Config.PayModelMap[userName]
	if pm.Ecs == "true" {
    if payModelExistsForUser(userName) {
      _ = accessToken
    } else {
      return openapi.Response(404, nil), nil //Paymodel has not been setup for user
  	}
  } else {
    stats, err := listWorkspacePods(ctx, userName)
    if err != nil {
      return openapi.Response(500, nil), nil
    }
    return openapi.Response(200, stats), nil
  }
  return openapi.Response(200, stats), nil
}

func (s *HatcheryAPIService) Paymodels(ctx context.Context, userName string) (openapi.ImplResponse, error) {
  if payModelExistsForUser(userName) {
		out := Config.PayModelMap[userName]
		return openapi.Response(200, openapi.PayModel{
      Name:out.Name,
      User:out.User,
      AwsAccountId: out.AWSAccountId,
      Region: out.Region,
      Ecs:out.Ecs,
    }), nil
	}
  return openapi.Response(404, nil),nil
}

func (s *HatcheryAPIService) Terminate(ctx context.Context, userName string, authorization string, workspaceID string) (openapi.ImplResponse, error) {
  accessToken := readBearerToken(authorization)
  if userName == "" {
    return openapi.ImplResponse{}, fmt.Errorf("Missing REMOTE_USER header")
  }
	pm := Config.PayModelMap[userName]
	if pm.Ecs == "true" {
    if payModelExistsForUser(userName) {
  		_, err := terminateEcsWorkspace(ctx, userName, accessToken)
  		if err != nil {
  			return openapi.Response(500, nil), nil
  		}
  	} else {
      return openapi.Response(404, nil), nil //Paymodel has not been setup for user
  	}
	} else {
		err := deleteK8sPod(ctx, accessToken, userName, workspaceID)
		if err != nil {
      return openapi.Response(500, nil), nil
		}
	}
  return openapi.Response(200, nil),nil
}
