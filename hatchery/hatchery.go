package hatchery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	httptrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/net/http"
	k8sv1 "k8s.io/api/core/v1"
)

// Config package-global shared hatchery config
var Config *FullHatcheryConfig

type containerOption struct {
	Name          string `json:"name"`
	CPULimit      string `json:"cpu-limit"`
	MemoryLimit   string `json:"memory-limit"`
	ID            string `json:"id"`
	IdleTimeLimit int    `json:"idle-time-limit"`
}

// RegisterHatchery setup endpoints with the http engine
func RegisterHatchery(mux *httptrace.ServeMux) {
	mux.HandleFunc("/", home)
	mux.HandleFunc("/launch", launch)
	mux.HandleFunc("/terminate", terminate)
	mux.HandleFunc("/status", status)
	mux.HandleFunc("/options", options)
	mux.HandleFunc("/mount-files", mountFiles)
	mux.HandleFunc("/paymodels", paymodels)
	mux.HandleFunc("/setpaymodel", setpaymodel)
	mux.HandleFunc("/resetpaymodels", resetPaymodels)
	mux.HandleFunc("/allpaymodels", allpaymodels)

	// ECS functions
	mux.HandleFunc("/create-ecs-cluster", createECSCluster)
}

func home(w http.ResponseWriter, r *http.Request) {
	htmlHeader := `<html>
	<head>Gen3 Hatchery</head>
	<body>`
	fmt.Fprintln(w, htmlHeader)

	for k, v := range Config.ContainersMap {
		fmt.Fprintf(w, "<h1><a href=\"%s/launch?hash=%s\">Launch %s - %s CPU - %s Memory</a></h1>", Config.Config.SubDir, k, v.Name, v.CPULimit, v.MemoryLimit)
	}

	htmlFooter := `</body>
	</html>`
	fmt.Fprintln(w, htmlFooter)
}

func getCurrentUserName(r *http.Request) (userName string) {
	user := r.Header.Get("REMOTE_USER")
	if user == "" {
		Config.Logger.Printf("Warning: No username in header REMOTE_USER!")
	}
	return user
}

var getWorkspaceStatus = func(ctx context.Context, userName string, accessToken string) (*WorkspaceStatus, error) {
	allpaymodels, err := getPayModelsForUser(userName)
	if err != nil {
		return nil, err
	}

	if allpaymodels == nil {
		return statusK8sPod(ctx, userName, accessToken, nil)
	}

	payModel := allpaymodels.CurrentPayModel
	if payModel != nil && payModel.Ecs {
		return statusEcs(ctx, userName, accessToken, payModel.AWSAccountId)
	} else {
		return statusK8sPod(ctx, userName, accessToken, payModel)
	}
}

func paymodels(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	userName := getCurrentUserName(r)

	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if payModel == nil {
		http.Error(w, "Current paymodel not set", http.StatusNotFound)
		return
	}
	out, err := json.Marshal(payModel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, string(out))
}

func allpaymodels(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	userName := getCurrentUserName(r)

	payModels, err := getPayModelsForUser(userName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if payModels == nil {
		http.Error(w, "No paymodel set", http.StatusNotFound)
		return
	}
	out, err := json.Marshal(payModels)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, string(out))
}

func setpaymodel(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	userName := getCurrentUserName(r)
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing ID argument", http.StatusBadRequest)
		return
	}

	currentStatus, err := getWorkspaceStatus(r.Context(), userName, getBearerToken(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Do not let users update status when a workpsace session is in progress
	if currentStatus.Status != "Not Found" {
		http.Error(w, "Can not update paymodel when workspace is running", http.StatusInternalServerError)
		return
	}

	pm, err := setCurrentPaymodel(userName, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out, err := json.Marshal(pm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, string(out))
}

func status(w http.ResponseWriter, r *http.Request) {
	userName := getCurrentUserName(r)
	accessToken := getBearerToken(r)

	result, err := getWorkspaceStatus(r.Context(), userName, accessToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out, err := json.Marshal(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, string(out))
}

func resetPaymodels(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	userName := getCurrentUserName(r)

	currentStatus, err := getWorkspaceStatus(r.Context(), userName, getBearerToken(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Do not let users update status when a workpsace session is in progress
	if currentStatus.Status != "Not Found" {
		http.Error(w, "Can not reset paymodels when workspace is running", http.StatusInternalServerError)
		return
	}

	err = resetCurrentPaymodel(userName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, "Current Paymodel has been reset")
}

func getOptionOutputForContainer(containerId string, containerSettings Container) containerOption {
	c := containerOption{
		Name:        containerSettings.Name,
		CPULimit:    containerSettings.CPULimit,
		MemoryLimit: containerSettings.MemoryLimit,
		ID:          containerId,
	}
	c.IdleTimeLimit = -1
	for _, arg := range containerSettings.Args {
		if strings.Contains(arg, "shutdown_no_activity_timeout=") {
			argSplit := strings.Split(arg, "=")
			idleTimeLimit, err := strconv.Atoi(argSplit[len(argSplit)-1])
			if err == nil {
				c.IdleTimeLimit = idleTimeLimit * 1000
			}
			break
		}
	}

	return c
}

func options(w http.ResponseWriter, r *http.Request) {
	userName := getCurrentUserName(r)
	accessToken := getBearerToken(r)

	// handle `/options?id=abc` => return the specified option
	hash := r.URL.Query().Get("id")
	if hash != "" {
		containerSettings, ok := Config.ContainersMap[hash]
		if !ok {
			http.Error(w, fmt.Sprintf("Invalid 'id' parameter '%s'", hash), http.StatusBadRequest)
			return
		}
		allowed, err := isUserAuthorizedForContainer(userName, accessToken, Config.ContainersMap[hash])
		if err != nil {
			Config.Logger.Printf("Unable to check if user is authorized to launch this container. Assuming unthorized. Details: %v", err)
		}
		if err != nil || !allowed {
			// return the same as for an unknown id
			http.Error(w, fmt.Sprintf("Invalid 'id' parameter '%s'", hash), http.StatusBadRequest)
			return
		}

		out, err := json.Marshal(getOptionOutputForContainer(hash, containerSettings))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		fmt.Fprint(w, string(out))
		return
	}

	// handle `/options` without `id` parameter => return all available options
	var options []containerOption
	for k, v := range Config.ContainersMap {
		// filter out workspace options that the user is not allowed to run
		allowed, err := isUserAuthorizedForContainer(userName, accessToken, v)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !allowed {
			continue // do not return containers that the user is not allowed to run
		}

		c := getOptionOutputForContainer(k, v)
		options = append(options, c)
	}

	out, err := json.Marshal(options)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, string(out))
}

func getWorkspaceFlavor(container Container) string {
	if container.NextflowConfig.Enabled {
		return "nextflow"
	} else if strings.Contains(strings.ToLower(container.Name), "jupyter") {
		return "jupyter"
	} else {
		return ""
	}
}

func launch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	accessToken := getBearerToken(r)

	hash := r.URL.Query().Get("id")
	if hash == "" {
		http.Error(w, "Missing 'id' parameter", http.StatusBadRequest)
		return
	}
	_, ok := Config.ContainersMap[hash]
	if !ok {
		http.Error(w, fmt.Sprintf("Invalid 'id' parameter '%s'", hash), http.StatusBadRequest)
		return
	}

	userName := getCurrentUserName(r)
	if userName == "" {
		http.Error(w, "No username found. Launch forbidden", http.StatusBadRequest)
		return
	}

	allowed, err := isUserAuthorizedForContainer(userName, accessToken, Config.ContainersMap[hash])
	if err != nil {
		Config.Logger.Printf("Unable to check if user is authorized to launch this container. Assuming unthorized. Details: %v", err)
	}
	if err != nil || !allowed {
		// return the same as for an unknown id
		http.Error(w, fmt.Sprintf("Invalid 'id' parameter '%s'", hash), http.StatusBadRequest)
		return
	}

	var envVars []k8sv1.EnvVar
	var envVarsEcs []EnvVar

	workspaceFlavor := getWorkspaceFlavor(Config.ContainersMap[hash])
	envVars = append(
		envVars,
		k8sv1.EnvVar{
			Name:  "WORKSPACE_FLAVOR",
			Value: workspaceFlavor,
		},
	)
	envVarsEcs = append(
		envVarsEcs,
		EnvVar{
			Key:   "WORKSPACE_FLAVOR",
			Value: workspaceFlavor,
		},
	)

	if Config.ContainersMap[hash].NextflowConfig.Enabled {
		Config.Logger.Printf("Info: Nextflow is enabled: creating Nextflow resources in AWS...")
		nextflowKeyId, nextflowKeySecret, err := createNextflowResources(userName, Config.ContainersMap[hash].NextflowConfig)
		if err != nil {
			Config.Logger.Printf("Error creating Nextflow AWS resources in AWS for user '%s': %v", userName, err)
			http.Error(w, "Unable to create AWS resources for Nextflow", http.StatusInternalServerError)
			return
		}
		envVars = append(
			envVars,
			k8sv1.EnvVar{
				Name:  "AWS_ACCESS_KEY_ID",
				Value: nextflowKeyId,
			},
			k8sv1.EnvVar{
				Name:  "AWS_SECRET_ACCESS_KEY",
				Value: nextflowKeySecret,
			},
		)
		envVarsEcs = append(
			envVarsEcs,
			EnvVar{
				Key:   "AWS_ACCESS_KEY_ID",
				Value: nextflowKeyId,
			},
			EnvVar{
				Key:   "AWS_SECRET_ACCESS_KEY",
				Value: nextflowKeySecret,
			},
		)
		// TODO do we need to set AWS_DEFAULT_REGION too?
	} else {
		Config.Logger.Printf("Debug: Nextflow is not enabled: skipping Nextflow resources creation")
	}

	allpaymodels, err := getPayModelsForUser(userName)
	if err != nil {
		Config.Logger.Printf(err.Error())
	}
	if allpaymodels == nil { // Commons with no concept of paymodels
		err = createLocalK8sPod(r.Context(), hash, userName, accessToken, envVars)
	} else {
		payModel := allpaymodels.CurrentPayModel
		if payModel == nil {
			Config.Logger.Printf("Current Paymodel is not set. Launch forbidden for user %s", userName)
			http.Error(w, "Current Paymodel is not set. Launch forbidden", http.StatusInternalServerError)
			return
		} else if payModel.Local {
			err = createLocalK8sPod(r.Context(), hash, userName, accessToken, envVars)
		} else if payModel.Ecs {

			if payModel.Status != "active" {
				// send 500 response.
				// TODO: 403 is the correct code, but it triggers a 302 to the default 403 page in revproxy instead of showing error message.
				Config.Logger.Printf("Paymodel is not active. Launch forbidden for user %s", userName)
				http.Error(w, "Paymodel is not active. Launch forbidden", http.StatusInternalServerError)
				return
			}

			Config.Logger.Printf("Launching ECS workspace for user %s", userName)
			// Sending a 200 response straight away, but starting the launch in a goroutine
			// TODO: Do more sanity checks before returning 200.
			w.WriteHeader(http.StatusOK)
			go launchEcsWorkspaceWrapper(userName, hash, accessToken, *payModel, envVarsEcs)
			fmt.Fprintf(w, "Launch accepted")
			return
		} else {
			err = createExternalK8sPod(r.Context(), hash, userName, accessToken, *payModel, envVars)
		}
	}
	if err != nil {
		Config.Logger.Printf("error during launch: %-v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "Success")
}

func terminate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	accessToken := getBearerToken(r)
	userName := getCurrentUserName(r)
	if userName == "" {
		http.Error(w, "No username found. Unable to terminate", http.StatusBadRequest)
		return
	}

	Config.Logger.Printf("Terminating workspace for user %s", userName)

	// delete nextflow resources. There is no way to know if the actual workspace being
	// terminated is a nextflow workspace or not, so always attempt to delete
	Config.Logger.Printf("Info: Deleting Nextflow resources in AWS...")
	err := cleanUpNextflowResources(userName)
	if err != nil {
		Config.Logger.Printf("Unable to delete AWS resources for Nextflow... continuing anyway")
	}

	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		Config.Logger.Printf(err.Error())
	}
	if payModel != nil && payModel.Ecs {
		_, err = terminateEcsWorkspace(r.Context(), userName, accessToken, payModel.AWSAccountId)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		} else {
			Config.Logger.Printf("Succesfully terminated all resources related to ECS workspace for user %s", userName)
			fmt.Fprintf(w, "Terminated ECS workspace")
		}
	} else {
		err := deleteK8sPod(r.Context(), userName, accessToken, payModel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		Config.Logger.Printf("Terminated workspace for user %s", userName)
		fmt.Fprintf(w, "Terminated workspace")
	}

	// Need to reset pay model only after workspace termination is completed.
	go func() {
		// Periodically poll for status, until it is set as "Not Found"
		for {
			status, err := getWorkspaceStatus(r.Context(), userName, accessToken)
			if err != nil {
				Config.Logger.Printf("error fetching workspace status for user %s\n err: %s", userName, err)
			}
			if status.Status == "Not Found" {
				break
			}
			time.Sleep(5 * time.Second)
		}
		err = resetCurrentPaymodel(userName)
		if err != nil {
			Config.Logger.Printf("unable to reset current paymodel for current user %s\nerr: %s", userName, err)
		}
	}()
}

func getBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}
	s := strings.SplitN(authHeader, " ", 2)
	if len(s) == 2 && strings.ToLower(s[0]) == "bearer" {
		return s[1]
	}
	return ""
}

// ECS functions

// Function to create ECS cluster.
// TODO: NEED TO CALL THIS FUNCTION IF IT DOESN'T EXIST!!!
func createECSCluster(w http.ResponseWriter, r *http.Request) {
	userName := getCurrentUserName(r)
	payModel, err := getCurrentPayModel(userName)
	if payModel == nil {
		http.Error(w, "Paymodel has not been setup for user", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	roleARN := "arn:aws:iam::" + payModel.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))
	svc := NewSVC(sess, roleARN)

	result, err := svc.launchEcsCluster(userName)
	var reader *strings.Reader
	if err != nil {
		reader = strings.NewReader(err.Error())
		Config.Logger.Printf("Error: %s", err)
	} else {
		reader = strings.NewReader(result.String())
	}
	_, err = io.Copy(w, reader)
	if err != nil {
		Config.Logger.Printf("Error: %s", err)
	}
}

// Function to check status of ECS workspace.
var statusEcs = func(ctx context.Context, userName string, accessToken string, awsAcctID string) (*WorkspaceStatus, error) {
	roleARN := "arn:aws:iam::" + awsAcctID + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))
	svc := NewSVC(sess, roleARN)
	result, err := svc.statusEcsWorkspace(ctx, userName, accessToken)
	if err != nil {
		Config.Logger.Printf("Error: %s", err)
		return nil, err
	}
	return result, nil
}

// Wrapper function to launch ECS workspace in a goroutine.
// Terminates workspace if launch fails for whatever reason
var launchEcsWorkspaceWrapper = func(userName string, hash string, accessToken string, payModel PayModel, envVars []EnvVar) {
	err := launchEcsWorkspace(userName, hash, accessToken, payModel, envVars)
	if err != nil {
		Config.Logger.Printf("Error: %s", err)
		// Terminate ECS workspace if launch fails.
		_, err = terminateEcsWorkspace(context.Background(), userName, accessToken, payModel.AWSAccountId)
		if err != nil {
			Config.Logger.Printf("Error: %s", err)
		}
	}
}

// The files returned by this endpoint are mounted to the `/data` dir by the `ecs-ws-sidecar`
func mountFiles(w http.ResponseWriter, r *http.Request) {
	userName := getCurrentUserName(r)
	if userName == "" {
		http.Error(w, "Please login", http.StatusUnauthorized)
		return
	}

	// handle `/mount-files?file_path=abc` => return file contents
	filePath := r.URL.Query().Get("file_path")
	if filePath != "" {
		out, err := getMountFileContents(filePath, userName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, string(out))
		return
	}

	// handle `/mount-files` without `file_path` parameter => list the files
	type file struct {
		FilePath        string `json:"file_path"`
		WorkspaceFlavor string `json:"workspace_flavor"`
	}
	fileList := []file{}

	// Ideally we would only return this if the user is running a nextflow workspace. But we have
	// no way of knowing. Instead, set `WorkspaceFlavor=nextflow` and the sidecar will not mount
	// the file if env var `WORKSPACE_FLAVOR` is not `nextflow`.
	fileList = append(fileList, file{
		FilePath:        "sample-nextflow-config.txt",
		WorkspaceFlavor: "nextflow",
	})

	out, err := json.Marshal(fileList)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, string(out))
}

func getMountFileContents(fileId string, userName string) (string, error) {
	if fileId == "sample-nextflow-config.txt" {
		out, err := generateNextflowConfig(userName)
		if err != nil {
			Config.Logger.Printf("unable to generate Nextflow config: %v", err)
		}
		return out, nil
	} else {
		return "", fmt.Errorf("unknown id '%s'", fileId)
	}
}
