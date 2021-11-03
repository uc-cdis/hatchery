package hatchery

import (
	"github.com/uc-cdis/hatchery/hatchery/openapi"
	k8sv1 "k8s.io/api/core/v1"
)

func workspaceState2OpenAPI(in WorkspaceStatus) openapi.Status {
	out := openapi.Status{
		Id:              in.Id,
		Status:          in.Status,
		Conditions:      make([]openapi.PodCondition, len(in.Conditions)),
		ContainerStates: make([]openapi.ContainerState, len(in.ContainerStates)),
	}
	for i := range in.Conditions {
		out.Conditions[i].Status = in.Conditions[i].Status
		out.Conditions[i].Type = in.Conditions[i].Type
	}
	for i := range in.ContainerStates {
		out.ContainerStates[i].Name = in.ContainerStates[i].Name
		out.ContainerStates[i].Ready = in.ContainerStates[i].Ready
		out.ContainerStates[i].State = containerState2OpenAPI(in.ContainerStates[i].State)
	}
	return out
}

func containerState2OpenAPI(state k8sv1.ContainerState) openapi.ContainerStateDetail {
	out := openapi.ContainerStateDetail{}
	if state.Running != nil {
		out.Running = &openapi.ContainerStateDetailRunning{StartedAt: state.Running.StartedAt.String()}
	} else if state.Terminated != nil {
		out.Terminated = &openapi.ContainerStateDetailTerminated{
			ExitCode:    state.Terminated.ExitCode,
			Signal:      state.Terminated.Signal,
			Reason:      state.Terminated.Reason,
			Message:     state.Terminated.Message,
			StartedAt:   state.Terminated.StartedAt.String(),
			FinishedAt:  state.Terminated.FinishedAt.String(),
			ContainerID: state.Terminated.ContainerID,
		}
	} else if state.Waiting != nil {
		out.Waiting = &openapi.ContainerStateDetailWaiting{
			Reason:  state.Waiting.Reason,
			Message: state.Waiting.Message,
		}
	}
	return out
}
