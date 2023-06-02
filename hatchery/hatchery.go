package hatchery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	k8sv1 "k8s.io/api/core/v1"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	httptrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/net/http"
)

// Config package-global shared hatchery config
var Config *FullHatcheryConfig

// RegisterHatchery setup endpoints with the http engine
func RegisterHatchery(mux *httptrace.ServeMux) {
	mux.HandleFunc("/", home)
	mux.HandleFunc("/launch", launch)
	mux.HandleFunc("/terminate", terminate)
	mux.HandleFunc("/status", status)
	mux.HandleFunc("/options", options)
	mux.HandleFunc("/paymodels", paymodels)
	mux.HandleFunc("/setpaymodel", setpaymodel)
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
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	userName := getCurrentUserName(r)
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing ID argument", http.StatusBadRequest)
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

	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		if err != NopaymodelsError {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	var result *WorkspaceStatus

	if payModel == nil {
		result, err = statusK8sPod(r.Context(), userName, accessToken, payModel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if payModel.Ecs {
			result, err = statusEcs(r.Context(), userName, accessToken, payModel.AWSAccountId)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			result, err = statusK8sPod(r.Context(), userName, accessToken, payModel)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	out, err := json.Marshal(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, string(out))
}

func options(w http.ResponseWriter, r *http.Request) {
	type container struct {
		Name          string `json:"name"`
		CPULimit      string `json:"cpu-limit"`
		MemoryLimit   string `json:"memory-limit"`
		ID            string `json:"id"`
		IdleTimeLimit int    `json:"idle-time-limit"`
	}
	var options []container
	for k, v := range Config.ContainersMap {
		c := container{
			Name:        v.Name,
			CPULimit:    v.CPULimit,
			MemoryLimit: v.MemoryLimit,
			ID:          k,
		}
		c.IdleTimeLimit = -1
		for _, arg := range v.Args {
			if strings.Contains(arg, "shutdown_no_activity_timeout=") {
				argSplit := strings.Split(arg, "=")
				idleTimeLimit, err := strconv.Atoi(argSplit[len(argSplit)-1])
				if err == nil {
					c.IdleTimeLimit = idleTimeLimit * 1000
				}
				break
			}
		}
		options = append(options, c)
	}

	out, err := json.Marshal(options)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, string(out))
}

func launch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	accessToken := getBearerToken(r)

	hash := r.URL.Query().Get("id")

	if hash == "" {
		http.Error(w, "Missing ID argument", http.StatusBadRequest)
		return
	}

	userName := getCurrentUserName(r)
	var envVars []k8sv1.EnvVar
	var envVarsEcs []EnvVar

	if Config.ContainersMap[hash].EnableNextflow {
		Config.Logger.Printf("Info: Nextflow is enabled: creating Nextflow resources in AWS...")
		nextflowKeyId, nextflowKeySecret, err := createNextflowUserResources(userName)
		if err != nil {
			http.Error(w, "Unable to create AWS resources for Nextflow", http.StatusInternalServerError)
			return
		}
		envVars = append(envVars, k8sv1.EnvVar{
			Name:  "AWS_ACCESS_KEY_ID",
			Value: nextflowKeyId,
		})
		envVars = append(envVars, k8sv1.EnvVar{
			Name:  "AWS_SECRET_ACCESS_KEY",
			Value: nextflowKeySecret,
		})
		envVarsEcs = append(envVarsEcs, EnvVar{
			Key:   "AWS_ACCESS_KEY_ID",
			Value: nextflowKeyId,
		})
		envVarsEcs = append(envVarsEcs, EnvVar{
			Key:   "AWS_SECRET_ACCESS_KEY",
			Value: nextflowKeySecret,
		})
		// TODO do we need to set AWS_DEFAULT_REGION too?
	} else {
		Config.Logger.Printf("Debug: Nextflow is not enabled: skipping Nextflow resources creation")
	}

	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		Config.Logger.Printf(err.Error())
	}
	if payModel == nil || payModel.Local {
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
	if err != nil {
		Config.Logger.Printf("error during launch: %-v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "Success")
}

func terminate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	accessToken := getBearerToken(r)
	userName := getCurrentUserName(r)
	Config.Logger.Printf("Terminating workspace for user %s", userName)
	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		Config.Logger.Printf(err.Error())
	}
	if payModel != nil && payModel.Ecs {
		_, err := terminateEcsWorkspace(r.Context(), userName, accessToken, payModel.AWSAccountId)
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
func statusEcs(ctx context.Context, userName string, accessToken string, awsAcctID string) (*WorkspaceStatus, error) {
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
func launchEcsWorkspaceWrapper(userName string, hash string, accessToken string, payModel PayModel, envVars []EnvVar) {

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
