/*
 * Hatchery API
 *
 * Workspace service for launching and interacting with containers.
 *
 * API version: 1.0.0
 * Generated by: OpenAPI Generator (https://openapi-generator.tech)
 */

package openapi

type WorkspaceStatus struct {

	// External HTTP/HTTPS address where the service can be reached 
	Url string `json:"url,omitempty"`

	// Value:  * `Terminating` - The workspace is shutting down  * `Launching` - The workspace is starting up  * `Stopped` - The workspace is in a failed state and must be terminated  * `Running` - The workspace is running and ready to be used 
	Status string `json:"status,omitempty"`

	// The condition of the pod
	Conditions []PodCondition `json:"conditions,omitempty"`

	// The state of all the containers
	ContainerStates []ContainerState `json:"containerStates,omitempty"`
}

// AssertWorkspaceStatusRequired checks if the required fields are not zero-ed
func AssertWorkspaceStatusRequired(obj WorkspaceStatus) error {
	for _, el := range obj.Conditions {
		if err := AssertPodConditionRequired(el); err != nil {
			return err
		}
	}
	for _, el := range obj.ContainerStates {
		if err := AssertContainerStateRequired(el); err != nil {
			return err
		}
	}
	return nil
}

// AssertRecurseWorkspaceStatusRequired recursively checks if required fields are not zero-ed in a nested slice.
// Accepts only nested slice of WorkspaceStatus (e.g. [][]WorkspaceStatus), otherwise ErrTypeAssertionError is thrown.
func AssertRecurseWorkspaceStatusRequired(objSlice interface{}) error {
	return AssertRecurseInterfaceRequired(objSlice, func(obj interface{}) error {
		aWorkspaceStatus, ok := obj.(WorkspaceStatus)
		if !ok {
			return ErrTypeAssertionError
		}
		return AssertWorkspaceStatusRequired(aWorkspaceStatus)
	})
}