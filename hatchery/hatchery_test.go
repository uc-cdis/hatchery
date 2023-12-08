package hatchery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	k8sv1 "k8s.io/api/core/v1"
)

/*
* getWorkspaceStatus
	*getPayModelsForUser
		* returns nil
		* returns paymodels with current paymodel.ecs
		* returns paymodels with current paymodel -- nil
		* returns paymodels with current paymodel.ecs == false
*/
func Test_GetWorkspaceStatus(t *testing.T) {
	defer SetupAndTeardownTest()()

	mockStatusK8sPod := &WorkspaceStatus{
		Status: "Running K8s Pod",
	}
	mockStatusEcs := &WorkspaceStatus{
		Status: "Running Ecs Pod",
	}
	testCases := []struct {
		name                 string
		want                 *WorkspaceStatus
		mockPayModelsForUser *AllPayModels
	}{
		{
			name:                 "NoPayModelsForUser",
			want:                 mockStatusK8sPod,
			mockPayModelsForUser: nil,
		},
		{
			name: "NoCurrentPaymodel",
			want: mockStatusK8sPod,
			mockPayModelsForUser: &AllPayModels{
				PayModels: []PayModel{
					{
						Name:            "random_pay_model",
						Ecs:             true,
						CurrentPayModel: false,
					},
				},
			},
		},
		{
			name: "EcsCurrentPaymodel",
			want: mockStatusEcs,
			mockPayModelsForUser: &AllPayModels{
				CurrentPayModel: &PayModel{
					Name:            "random_pay_model",
					Ecs:             true,
					CurrentPayModel: true,
				},
				PayModels: []PayModel{
					{
						Name:            "random_pay_model",
						Ecs:             true,
						CurrentPayModel: true,
					},
				},
			},
		},
		{
			name: "NonEcsCurrentPayModel",
			want: mockStatusK8sPod,
			mockPayModelsForUser: &AllPayModels{
				CurrentPayModel: &PayModel{
					Name:            "random_pay_model",
					Ecs:             false,
					CurrentPayModel: true,
				},
				PayModels: []PayModel{
					{
						Name:            "random_pay_model",
						Ecs:             false,
						CurrentPayModel: true,
					},
				},
			},
		},
	}
	// Backing up original functions before mocking
	original_getPayModelsForUser := getPayModelsForUser
	original_statusK8sPod := statusK8sPod
	original_statusEcs := statusEcs
	defer func() {
		// restore original functions
		getPayModelsForUser = original_getPayModelsForUser
		statusK8sPod = original_statusK8sPod
		statusEcs = original_statusEcs
	}()

	for _, testcase := range testCases {
		t.Logf("Testing GetWorkspaceStatus when %s", testcase.name)
		/* Setup */

		statusK8sPod = func(context.Context, string, string, *PayModel) (*WorkspaceStatus, error) {
			return mockStatusK8sPod, nil
		}

		statusEcs = func(context.Context, string, string, string) (*WorkspaceStatus, error) {
			return mockStatusEcs, nil
		}
		getPayModelsForUser = func(string) (*AllPayModels, error) {
			return testcase.mockPayModelsForUser, nil
		}
		/* Act */
		ctx := context.Background()
		got, err := getWorkspaceStatus(ctx, "testUser", "access_token")
		if nil != err {
			t.Errorf("failed to load workspace status, got: %v", err)
			return
		}

		/* Assert */
		if !reflect.DeepEqual(got, testcase.want) {
			t.Errorf("\nassertion error while testing `GetCurrentPayModel` when %s : \nWant:%+v\nGot:%+v", testcase.name, testcase.want, got)
		}
	}
}

/*
* SetPaymodels
	* mock w, r to provide userName and id
		* id being empty should return in an error
	* mock getWorkspoaceStatus, setCurrentPayModel
		* status with status.Status == "Running" return error
		* status with status.Status == "Not Found" should call setCurrentPayModel and return the mock currentPayModel
*/
func Test_SetpaymodelEndpoint(t *testing.T) {
	defer SetupAndTeardownTest()()

	type RequestBody struct {
		Method string
		id     string
	}
	testCases := []struct {
		name                string
		mockWorkspaceStatus *WorkspaceStatus
		want                string
		wantStatus          int
		mockRequest         *RequestBody
		currentStatus       *WorkspaceStatus
	}{
		{
			name:       "MethodIsNotPost",
			want:       "Method Not Allowed",
			wantStatus: http.StatusMethodNotAllowed,
			mockRequest: &RequestBody{
				Method: "GET",
			},
			currentStatus: nil,
		},
		{
			name:       "NoPayModelIdExists",
			want:       "Missing ID argument",
			wantStatus: http.StatusBadRequest,
			mockRequest: &RequestBody{
				Method: "POST",
			},
			currentStatus: nil,
		},
		{
			name:       "StatusAsRunning",
			want:       "Can not update paymodel when workspace is running",
			wantStatus: http.StatusInternalServerError,
			mockRequest: &RequestBody{
				Method: "POST",
				id:     "random_id",
			},
			currentStatus: &WorkspaceStatus{Status: "Running"},
		},
		{
			name:       "StatusAsNotFound",
			want:       "{\"bmh_workspace_id\":\"mock_current_paymodel\",\"workspace_type\":\"\",\"user_id\":\"\",\"account_id\":\"\",\"request_status\":\"\",\"local\":false,\"region\":\"\",\"ecs\":false,\"subnet\":0,\"hard-limit\":0,\"soft-limit\":0,\"total-usage\":0,\"current_pay_model\":true}",
			wantStatus: http.StatusOK,
			mockRequest: &RequestBody{
				Method: "POST",
				id:     "random_id",
			},
			currentStatus: &WorkspaceStatus{Status: "Not Found"},
		},
	}

	// Backing up original functions before mocking
	original_getWorkspaceStatus := getWorkspaceStatus
	original_setCurrentPaymodel := setCurrentPaymodel
	defer func() {
		// restore original functions
		getWorkspaceStatus = original_getWorkspaceStatus
		setCurrentPaymodel = original_setCurrentPaymodel
	}()

	for _, testcase := range testCases {
		t.Logf("Testing SetPaymodels when %s", testcase.name)

		/* Setup */
		getWorkspaceStatus = func(context.Context, string, string) (*WorkspaceStatus, error) {
			return testcase.currentStatus, nil
		}
		setCurrentPaymodel = func(string, string) (*PayModel, error) {
			return &PayModel{
				Id:              "mock_current_paymodel",
				CurrentPayModel: true,
			}, nil
		}
		url := "/setpaymodel"
		if testcase.mockRequest.id != "" {
			url = "/setpaymodel?id=" + testcase.mockRequest.id
		}
		req, err := http.NewRequest(testcase.mockRequest.Method, url, nil)

		if err != nil {
			t.Fatal(err)
		}

		w := httptest.NewRecorder()

		/* Act */
		handler := http.HandlerFunc(setpaymodel)
		handler.ServeHTTP(w, req)

		/* Assert */
		if testcase.wantStatus != w.Code {
			t.Errorf("handler returned wrong status code:\ngot: '%v'\nwant: '%v'",
				w.Code, testcase.wantStatus)
		}

		if testcase.want != strings.TrimSpace(w.Body.String()) {
			t.Errorf("handler returned wrong response:\ngot: '%v'\nwant: '%v'",
				w.Body.String(), testcase.want)
		}
	}
}

/*
* resetPayModels
	* mock w,r
		* r.Method not being post must return an error
	* mock getWorkspoaceStatus, setCurrentPayModel
		* status with status.Status == "Running" return error
		* status with status.Status == "Not Found" should call "resetCurrentPayModel" and return the mock currentPayModel
*/
func Test_ResetpaymodelsEndpoint(t *testing.T) {
	defer SetupAndTeardownTest()()

	type RequestBody struct {
		Method string
		id     string
	}
	testCases := []struct {
		name                string
		mockWorkspaceStatus *WorkspaceStatus
		want                string
		wantStatus          int
		mockRequest         *RequestBody
		throwError          bool
		currentStatus       *WorkspaceStatus
	}{
		{
			name:       "MethodIsNotPost",
			want:       "Method Not Allowed",
			wantStatus: http.StatusMethodNotAllowed,
			mockRequest: &RequestBody{
				Method: "GET",
			},
			currentStatus: nil,
		},
		{
			name:       "StatusAsRunning",
			want:       "Can not reset paymodels when workspace is running",
			wantStatus: http.StatusInternalServerError,
			mockRequest: &RequestBody{
				Method: "POST",
				id:     "random_id",
			},
			currentStatus: &WorkspaceStatus{Status: "Running"},
		},
		{
			name:       "StatusAsNotFound",
			want:       "Current Paymodel has been reset",
			wantStatus: http.StatusOK,
			mockRequest: &RequestBody{
				Method: "POST",
			},
			currentStatus: &WorkspaceStatus{Status: "Not Found"},
		},
		{
			name:       "SetCurrentPayModelFailure",
			want:       "unable to set paymodel",
			wantStatus: http.StatusInternalServerError,
			throwError: true,
			mockRequest: &RequestBody{
				Method: "POST",
			},
			currentStatus: &WorkspaceStatus{Status: "Not Found"},
		},
	}

	// Backing up original functions before mocking
	original_getWorkspaceStatus := getWorkspaceStatus
	original_resetCurrentPaymodel := resetCurrentPaymodel
	defer func() {
		// restore original functions
		getWorkspaceStatus = original_getWorkspaceStatus
		resetCurrentPaymodel = original_resetCurrentPaymodel
	}()

	for _, testcase := range testCases {
		t.Logf("Testing ResetPaymodels when %s", testcase.name)

		/* Setup */
		getWorkspaceStatus = func(ctx context.Context, userName string, accessToken string) (*WorkspaceStatus, error) {
			return testcase.currentStatus, nil
		}
		resetCurrentPaymodel = func(string) error {
			if testcase.throwError {
				return errors.New("unable to set paymodel")
			}
			return nil
		}
		url := "/resetpaymodels"
		req, err := http.NewRequest(testcase.mockRequest.Method, url, nil)

		if err != nil {
			t.Fatal(err)
		}

		w := httptest.NewRecorder()

		/* Act */
		handler := http.HandlerFunc(resetPaymodels)
		handler.ServeHTTP(w, req)

		/* Assert */
		if testcase.wantStatus != w.Code {
			t.Errorf("handler returned wrong status code:\ngot: '%v'\nwant: '%v'",
				w.Code, testcase.wantStatus)
		}

		if testcase.want != strings.TrimSpace(w.Body.String()) {
			t.Errorf("handler returned wrong response:\ngot: '%v'\nwant: '%v'",
				w.Body.String(), testcase.want)
		}
	}
}

/*
* launch
** mimics .calls() function in python
	* mock w,r
		* r.Method not being post must return an error
		* id being empty should return in an error
	* mock getPayModelsForUser
		* allPayModels = nil, createLocalK8sPod must be called once
		* allPayModels.CurrentPayModel = nil, InternalServerError is thrown
		* allPayModels.CurrentPayModel.Local = true, createLocalK8sPod must be called once
		* allPayModels.CurrentPayModel.Ecs = true, and status != active InternalServerError is thrown
		* allPayModels.CurrentPayModel.Ecs = true, and status == active launchEcsWorkspaceWrapper must be called once
		* allPayModels.CurrentPayModel.Ecs = false  and allPayModels.CurrentPayModel.Local = true createExternalK8sPod must be called once
*/
func Test_LaunchEndpoint(t *testing.T) {
	defer SetupAndTeardownTest()()

	type RequestBody struct {
		Method   string
		id       string
		username string
	}
	testCases := []struct {
		name                string
		mockWorkspaceStatus *WorkspaceStatus
		want                string
		wantStatus          int
		mockRequest         *RequestBody
		throwError          bool
		payModelsForUser    *AllPayModels
		calledFunctionName  string
	}{
		{
			name:       "MethodIsNotPost",
			want:       "Method Not Allowed",
			wantStatus: http.StatusMethodNotAllowed,
			mockRequest: &RequestBody{
				Method: "GET",
			},
			payModelsForUser: nil,
		},
		{
			name:       "MissingLaunchID",
			want:       "Missing 'id' parameter",
			wantStatus: http.StatusBadRequest,
			mockRequest: &RequestBody{
				Method: "POST",
			},
		},
		{
			name:       "MissingUsername",
			want:       "No username found. Launch forbidden",
			wantStatus: http.StatusBadRequest,
			mockRequest: &RequestBody{
				Method: "POST",
				id:     "random_id",
			},
		},
		{
			name:       "NoPayModelsForUser",
			want:       "Success",
			wantStatus: http.StatusOK,
			mockRequest: &RequestBody{
				Method:   "POST",
				id:       "random_id",
				username: "testUser",
			},
			payModelsForUser:   nil,
			calledFunctionName: "createLocalK8sPod",
		},
		{
			name:       "NoCurrentPayModelExists",
			want:       "Current Paymodel is not set. Launch forbidden",
			wantStatus: http.StatusInternalServerError,
			mockRequest: &RequestBody{
				Method:   "POST",
				id:       "random_id",
				username: "testUser",
			},
			payModelsForUser: &AllPayModels{
				CurrentPayModel: nil,
			},
		},
		{
			name:       "LocalCurrentPayModelExists",
			want:       "Success",
			wantStatus: http.StatusOK,
			mockRequest: &RequestBody{
				Method:   "POST",
				id:       "random_id",
				username: "testUser",
			},
			payModelsForUser: &AllPayModels{
				CurrentPayModel: &PayModel{Local: true},
			},
			calledFunctionName: "createLocalK8sPod",
		},
		{
			name:       "NonActiveEcsPayModelExists",
			want:       "Paymodel is not active. Launch forbidden",
			wantStatus: http.StatusInternalServerError,
			mockRequest: &RequestBody{
				Method:   "POST",
				id:       "random_id",
				username: "testUser",
			},
			payModelsForUser: &AllPayModels{
				CurrentPayModel: &PayModel{Ecs: true, Status: "Some status which is not Active"},
			},
		},
		{
			name:       "ActiveEcsCurrentPayModelExists",
			want:       "Launch accepted",
			wantStatus: http.StatusOK,
			mockRequest: &RequestBody{
				Method:   "POST",
				id:       "random_id",
				username: "testUser",
			},
			payModelsForUser: &AllPayModels{
				CurrentPayModel: &PayModel{Ecs: true, Status: "active"},
			},
			calledFunctionName: "launchEcsWorkspaceWrapper",
		},
		{
			name:       "NeitherLocalNorEcsPaymodelExists",
			want:       "Success",
			wantStatus: http.StatusOK,
			mockRequest: &RequestBody{
				Method:   "POST",
				id:       "random_id",
				username: "testUser",
			},
			payModelsForUser: &AllPayModels{
				CurrentPayModel: &PayModel{Ecs: false, Local: false},
			},
			calledFunctionName: "createExternalK8sPod",
		},
		{
			name:       "createLocalK8sPodFailure",
			want:       "error creating local k8s pod",
			wantStatus: http.StatusInternalServerError,
			mockRequest: &RequestBody{
				Method:   "POST",
				id:       "random_id",
				username: "testUser",
			},
			throwError:         true,
			payModelsForUser:   nil,
			calledFunctionName: "createLocalK8sPod",
		},
		{
			name:       "createExternalK8sPodFailure",
			want:       "error creating external k8s pod",
			wantStatus: http.StatusInternalServerError,
			mockRequest: &RequestBody{
				Method:   "POST",
				id:       "random_id",
				username: "testUser",
			},
			throwError: true,
			payModelsForUser: &AllPayModels{
				CurrentPayModel: &PayModel{Ecs: false, Local: false},
			},
			calledFunctionName: "createExternalK8sPod",
		},
	}

	// Backing up original functions before patching
	original_createLocalK8sPod := createLocalK8sPod
	original_launchEcsWorkspaceWrapper := launchEcsWorkspaceWrapper
	original_createExternalK8sPod := createExternalK8sPod
	original_getPayModelsForUser := getPayModelsForUser
	defer func() {
		// restore original functions
		createLocalK8sPod = original_createLocalK8sPod
		launchEcsWorkspaceWrapper = original_launchEcsWorkspaceWrapper
		createExternalK8sPod = original_createExternalK8sPod
		getPayModelsForUser = original_getPayModelsForUser
	}()

	Config.ContainersMap = map[string]Container{
		"random_id": {
			Name: "Hatchery test container",
		},
	}

	for _, testcase := range testCases {
		t.Logf("Testing Launch Endpoint when %s", testcase.name)

		// waitGroup is needed since one of the mocked methods is called as a go routine internally
		var waitGroup sync.WaitGroup

		FuncCounter := map[string]int{
			"createLocalK8sPod":         0,
			"launchEcsWorkspaceWrapper": 0,
			"createExternalK8sPod":      0,
		}

		createLocalK8sPod = func(ctx context.Context, hash, userName, accessToken string, envVars []k8sv1.EnvVar) error {
			FuncCounter["createLocalK8sPod"] += 1
			if testcase.throwError {
				return errors.New("error creating local k8s pod")
			}
			return nil
		}
		launchEcsWorkspaceWrapper = func(userName, hash, accessToken string, payModel PayModel, envVars []EnvVar) {
			FuncCounter["launchEcsWorkspaceWrapper"] += 1
			waitGroup.Done() // Assertions are blocked until this line is completed
		}
		createExternalK8sPod = func(ctx context.Context, hash, userName, accessToken string, payModel PayModel, envVars []k8sv1.EnvVar) error {
			FuncCounter["createExternalK8sPod"] += 1
			if testcase.throwError {
				return errors.New("error creating external k8s pod")
			}
			return nil
		}

		getPayModelsForUser = func(userName string) (result *AllPayModels, err error) {
			return testcase.payModelsForUser, nil
		}
		url := "/launch"
		if testcase.mockRequest.id != "" {
			url = "/launch?id=" + testcase.mockRequest.id
		}
		req, err := http.NewRequest(testcase.mockRequest.Method, url, nil)
		if testcase.mockRequest.username != "" {
			req.Header.Set("REMOTE_USER", testcase.mockRequest.username)
		}
		if err != nil {
			t.Fatal(err)
		}

		w := httptest.NewRecorder()

		/* Act */
		if testcase.calledFunctionName == "launchEcsWorkspaceWrapper" {
			// Since launchEcsWorkspaceWrapper is called as a go routine internally, we say
			// we say there is, in this test there is one goroutine that we'd like to wait for during execution
			waitGroup.Add(1)
		}
		handler := http.HandlerFunc(launch)
		handler.ServeHTTP(w, req)

		/* Assert */
		waitGroup.Wait() // we wait for any go routines to finish before making assertions
		if testcase.wantStatus != w.Code {
			t.Errorf("handler returned wrong status code:\ngot: '%v'\nwant: '%v'",
				w.Code, testcase.wantStatus)
		}

		if testcase.want != strings.TrimSpace(w.Body.String()) {
			t.Errorf("handler returned wrong response:\ngot: '%v'\nwant: '%v'",
				w.Body.String(), testcase.want)
		}

		for functionName, functionCallCounter := range FuncCounter {
			if functionName == testcase.calledFunctionName && functionCallCounter != 1 {
				t.Errorf("Expected to call %s exactly once , but is called %d time(s)",
					functionName, functionCallCounter)
			}
			if functionName != testcase.calledFunctionName && functionCallCounter != 0 {
				t.Errorf("Expected to not call %s even once , but is called %d time(s)",
					functionName, functionCallCounter)
			}
		}
	}
}

func TestLaunchEndpointAuthorization(t *testing.T) {
	defer SetupAndTeardownTest()()

	Config.ContainersMap = map[string]Container{
		"container_a": {
			Name: "Container without authz (accessible by default)",
		},
		"container_b": {
			Name: "Container with authz the user can access",
			Authz: AuthzConfig{
				Version: 0.1,
				AuthzVersion_0_1: AuthzVersion_0_1{
					ResourcePaths: []string{"/my-container"},
				},
			},
		},
		"container_c": {
			Name: "Container with authz the user cannot access",
			Authz: AuthzConfig{
				Version: 0.1,
				AuthzVersion_0_1: AuthzVersion_0_1{
					ResourcePaths: []string{"/my-container"},
				},
			},
		},
	}

	// mock the actual authorization checks (tested in `authz_test.go`)
	originalIsUserAuthorizedForContainer := isUserAuthorizedForContainer
	isUserAuthorizedForContainer = func(userName string, accessToken string, container Container) (bool, error) {
		if strings.Contains(container.Name, "cannot") {
			return false, nil
		}
		return true, nil
	}
	// mock the pod launch
	originalCreateLocalK8sPod := createLocalK8sPod
	createLocalK8sPod = func(ctx context.Context, hash, userName, accessToken string, envVars []k8sv1.EnvVar) error {
		return nil
	}
	defer func() {
		// restore original functions
		isUserAuthorizedForContainer = originalIsUserAuthorizedForContainer
		createLocalK8sPod = originalCreateLocalK8sPod
	}()

	for containerId, container := range Config.ContainersMap {
		t.Logf("Running test case: '%s'", container.Name)

		url := fmt.Sprintf("/launch?id=%s", containerId)
		req, err := http.NewRequest("POST", url, nil)
		req.Header.Set("REMOTE_USER", "testUser")
		if err != nil {
			t.Errorf("Error creating request: %v", err.Error())
			return
		}
		w := httptest.NewRecorder()
		handler := http.HandlerFunc(launch)
		handler.ServeHTTP(w, req)

		if !strings.Contains(container.Name, "cannot") && (w.Code != 200 || w.Body.String() != "Success") {
			t.Errorf("The /launch endpoint should have allowed launching an authorized container, but it didn't: %v %v", w.Code, w.Body)
			return
		}
		if strings.Contains(container.Name, "cannot") && (w.Code != 403 || strings.TrimSuffix(w.Body.String(), "\n") != "You do not have authorization to run this container") {
			t.Errorf("The /launch endpoint should not have allowed launching an unauthorized container, but it did: %v %v", w.Code, w.Body)
			return
		}
	}
}

/*
* terminate
** mimic .calls() function in python
	* mock w,r
		* r.Method not being post must return an error
		* id being empty should return in an error
	* mock getCurrentPaymodel
		* CurrentPayModel = nil, deleteLocalK8sPod must be called once
		* CurrentPayModel.Ecs = true, terminateEcsWorkspace must be called once
		* CurrentPayModel.Ecs = false  deleteLocalK8sPod must be called once
	* mock getWorkspaceStatus
		* to always return Not Found to ensure resetCurrentPaymodel is called exactly once
		*to return
			* a value other than Not found in the first run
				* wait for resetCurrentPaymodel being called zero times
			* Not found the second time to see resetCurrentPaymodel being called exactly once.
*/
func Test_TerminateEndpoint(t *testing.T) {
	defer SetupAndTeardownTest()()

	type RequestBody struct {
		Method   string
		username string
	}
	testCases := []struct {
		name                string
		want                string
		wantStatus          int
		mockRequest         *RequestBody
		mockCurrentPayModel *PayModel
		waitToTerminate     bool
		throwError          bool
		calledFunctionName  string
	}{
		{
			name:       "MethodIsNotPost",
			want:       "Method Not Allowed",
			wantStatus: http.StatusMethodNotAllowed,
			mockRequest: &RequestBody{
				Method: "GET",
			},
		},
		{
			name:       "MissingUsername",
			want:       "No username found. Unable to terminate",
			wantStatus: http.StatusBadRequest,
			mockRequest: &RequestBody{
				Method: "POST",
			},
		},
		{
			name:       "NoCurrentPayModelExists",
			want:       "Terminated workspace",
			wantStatus: http.StatusOK,
			mockRequest: &RequestBody{
				Method:   "POST",
				username: "testUser",
			},
			mockCurrentPayModel: nil,
			calledFunctionName:  "deleteK8sPod",
		},
		{
			name:       "NonEcsPayModelExists",
			want:       "Terminated workspace",
			wantStatus: http.StatusOK,
			mockRequest: &RequestBody{
				Method:   "POST",
				username: "testUser",
			},
			mockCurrentPayModel: &PayModel{Ecs: false},
			calledFunctionName:  "deleteK8sPod",
		},
		{
			name:       "EcsCurrentPayModelExists",
			want:       "Terminated ECS workspace",
			wantStatus: http.StatusOK,
			mockRequest: &RequestBody{
				Method:   "POST",
				username: "testUser",
			},
			mockCurrentPayModel: &PayModel{Ecs: true},
			calledFunctionName:  "terminateEcsWorkspace",
		},
		{
			name:       "deleteK8sPodFailure",
			want:       "error deleting k8s pod",
			wantStatus: http.StatusInternalServerError,
			mockRequest: &RequestBody{
				Method:   "POST",
				username: "testUser",
			},
			throwError:          true,
			mockCurrentPayModel: &PayModel{Ecs: false},
			calledFunctionName:  "deleteK8sPod",
		},
		{
			name:       "terminateEcsWorkspaceFailure",
			want:       "error terminating ecs workspace",
			wantStatus: http.StatusInternalServerError,
			mockRequest: &RequestBody{
				Method:   "POST",
				username: "testUser",
			},
			throwError:          true,
			mockCurrentPayModel: &PayModel{Ecs: true},
			calledFunctionName:  "terminateEcsWorkspace",
		},
		{
			name:       "NonEcsPayModelExistsWithSlowTermination",
			want:       "Terminated workspace",
			wantStatus: http.StatusOK,
			mockRequest: &RequestBody{
				Method:   "POST",
				username: "testUser",
			},
			mockCurrentPayModel: &PayModel{Ecs: false},
			waitToTerminate:     true,
			calledFunctionName:  "deleteK8sPod",
		},
		{
			name:       "EcsCurrentPayModelExistsWithSlowTermination",
			want:       "Terminated ECS workspace",
			wantStatus: http.StatusOK,
			mockRequest: &RequestBody{
				Method:   "POST",
				username: "testUser",
			},
			mockCurrentPayModel: &PayModel{Ecs: true},
			waitToTerminate:     true,
			calledFunctionName:  "terminateEcsWorkspace",
		},
	}

	// Backing up original functions before patching
	original_deleteK8sPod := deleteK8sPod
	original_terminateEcsWorkspace := terminateEcsWorkspace
	original_getCurrentPayModel := getCurrentPayModel
	original_getWorkspaceStatus := getWorkspaceStatus
	original_resetCurrentPaymodel := resetCurrentPaymodel
	defer func() {
		// restore original functions
		deleteK8sPod = original_deleteK8sPod
		terminateEcsWorkspace = original_terminateEcsWorkspace
		getCurrentPayModel = original_getCurrentPayModel
		getWorkspaceStatus = original_getWorkspaceStatus
		resetCurrentPaymodel = original_resetCurrentPaymodel
	}()

	for _, testcase := range testCases {
		t.Logf("Testing Terminate Endpoint when %s", testcase.name)

		/* Setup */
		workspaceTerminationPending := testcase.waitToTerminate
		workspaceStatusCallCounter := 0
		goRoutineCalled := testcase.calledFunctionName != "" && !testcase.throwError

		// waitGroup is needed since one of the mocked methods is called as a go routine internally
		var waitGroup sync.WaitGroup

		FuncCounter := map[string]int{
			"deleteK8sPod":          0,
			"terminateEcsWorkspace": 0,
		}
		deleteK8sPod = func(ctx context.Context, userName, accessToken string, payModelPtr *PayModel) error {
			FuncCounter["deleteK8sPod"] += 1
			if testcase.throwError {
				return errors.New("error deleting k8s pod")
			}
			return nil
		}
		terminateEcsWorkspace = func(ctx context.Context, userName, accessToken, awsAcctID string) (string, error) {

			FuncCounter["terminateEcsWorkspace"] += 1
			if testcase.throwError {
				return "", errors.New("error terminating ecs workspace")
			}
			return "", nil
		}

		getCurrentPayModel = func(string) (*PayModel, error) {
			return testcase.mockCurrentPayModel, nil
		}

		getWorkspaceStatus = func(context.Context, string, string) (*WorkspaceStatus, error) {
			workspaceStatusCallCounter += 1
			if workspaceTerminationPending {
				// we assume that the workspace is terminated by the time this fucntion is called again
				workspaceTerminationPending = false

				return &WorkspaceStatus{Status: "Terminating"}, nil
			}
			return &WorkspaceStatus{Status: "Not Found"}, nil
		}

		resetCurrentPaymodel = func(string) error {
			waitGroup.Done()
			return nil
		}

		url := "/terminate"
		req, err := http.NewRequest(testcase.mockRequest.Method, url, nil)
		if testcase.mockRequest.username != "" {
			req.Header.Set("REMOTE_USER", testcase.mockRequest.username)
		}
		if err != nil {
			t.Fatal(err)
		}

		w := httptest.NewRecorder()

		/* Act */
		if goRoutineCalled {
			waitGroup.Add(1)
		}
		handler := http.HandlerFunc(terminate)
		handler.ServeHTTP(w, req)

		/* Assert */
		waitGroup.Wait() // we wait for any go routines to finish before making assertions
		if testcase.wantStatus != w.Code {
			t.Errorf("handler returned wrong status code:\ngot: '%v'\nwant: '%v'",
				w.Code, testcase.wantStatus)
		}

		if testcase.want != strings.TrimSpace(w.Body.String()) {
			t.Errorf("handler returned wrong response:\ngot: '%v'\nwant: '%v'",
				w.Body.String(), testcase.want)
		}

		for functionName, functionCallCounter := range FuncCounter {
			if functionName == testcase.calledFunctionName && functionCallCounter != 1 {
				t.Errorf("Expected to call %s exactly once , but is called %d time(s)",
					functionName, functionCallCounter)
			}
			if functionName != testcase.calledFunctionName && functionCallCounter != 0 {
				t.Errorf("Expected to not call %s even once , but is called %d time(s)",
					functionName, functionCallCounter)
			}
		}

		if goRoutineCalled {
			if testcase.waitToTerminate && workspaceStatusCallCounter < 2 {
				t.Errorf("Expected to call workspaceStatus more than once , but is called %d time(s)",
					workspaceStatusCallCounter)
			}

			if !testcase.waitToTerminate && workspaceStatusCallCounter != 1 {
				t.Errorf("Expected to call workspaceStatus exactly once , but is called %d time(s)",
					workspaceStatusCallCounter)
			}
		}
	}
}

func TestOptionsEndpointAuthorization(t *testing.T) {
	defer SetupAndTeardownTest()()

	Config.ContainersMap = map[string]Container{
		"container_a": {
			Name: "Container without authz (accessible by default)",
		},
		"container_b": {
			Name: "Container with authz the user can access",
			Authz: AuthzConfig{
				Version: 0.1,
				AuthzVersion_0_1: AuthzVersion_0_1{
					ResourcePaths: []string{"/my-container"},
				},
			},
		},
		"container_c": {
			Name: "Container with authz the user cannot access",
			Authz: AuthzConfig{
				Version: 0.1,
				AuthzVersion_0_1: AuthzVersion_0_1{
					ResourcePaths: []string{"/my-container"},
				},
			},
		},
	}

	// mock the actual authorization checks (tested in `authz_test.go`)
	originalIsUserAuthorizedForContainer := isUserAuthorizedForContainer
	isUserAuthorizedForContainer = func(userName string, accessToken string, container Container) (bool, error) {
		if strings.Contains(container.Name, "cannot") {
			return false, nil
		}
		return true, nil
	}
	defer func() {
		isUserAuthorizedForContainer = originalIsUserAuthorizedForContainer // restore original function
	}()

	url := "/options"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Errorf("Error creating request: %v", err.Error())
		return
	}
	w := httptest.NewRecorder()
	handler := http.HandlerFunc(options)
	handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("Error when hitting /options endpoint: got status code %v", w.Code)
		return
	}

	if !strings.Contains(w.Body.String(), "container_a") || !strings.Contains(w.Body.String(), "container_b") {
		t.Errorf("The /options endpoint should have returned authorized containers, but it didn't: %v", w.Body)
		return
	}
	if strings.Contains(w.Body.String(), "container_c") {
		t.Errorf("The /options endpoint should not have returned unauthorized containers, but it did: %v", w.Body)
		return
	}
}

func TestMountFilesEndpoint(t *testing.T) {
	defer SetupAndTeardownTest()()

	// mock the nextflow config generation, which makes calls to AWS
	fileContents := "here's the output"
	originalGenerateNextflowConfig := generateNextflowConfig
	generateNextflowConfig = func(userName string) (string, error) {
		return fileContents, nil
	}
	defer func() {
		generateNextflowConfig = originalGenerateNextflowConfig // restore original function
	}()

	// list files
	url := "/mount-files"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Errorf("Error creating request: %v", err.Error())
		return
	}
	req.Header.Set("REMOTE_USER", "testUser")
	w := httptest.NewRecorder()
	handler := http.HandlerFunc(mountFiles)
	handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("Error when hitting /mount-files endpoint: got status code %v", w.Code)
		return
	}
	expectedOutput := "[{\"file_path\":\"sample-nextflow-config.txt\",\"workspace_flavor\":\"nextflow\"}]"
	if w.Body.String() != expectedOutput {
		t.Errorf("The '%s' endpoint should have returned the expected output '%s', but it returned: '%v'", url, expectedOutput, w.Body)
		return
	}

	// get file contents
	url = "/mount-files?file_path=sample-nextflow-config.txt"
	req, err = http.NewRequest("GET", url, nil)
	if err != nil {
		t.Errorf("Error creating request: %v", err.Error())
		return
	}
	req.Header.Set("REMOTE_USER", "testUser")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("Error when hitting /mount-files endpoint: got status code %v", w.Code)
		return
	}
	if w.Body.String() != fileContents {
		t.Errorf("The '%s' endpoint should have returned the expected output '%s', but it returned: '%v'", url, fileContents, w.Body)
		return
	}
}
