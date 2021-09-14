package hatchery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	httptrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/net/http"
)

type APIKeyStruct struct {
	APIKey string `json:"api_key"`
	KeyID  string `json:"key_id"`
}

type WorkspaceKernelStatusStruct struct {
	LastActivityTime string `json:"last_activity"`
}

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

	// ECS functions
	mux.HandleFunc("/create-ecs-cluster", ecsCluster)
}

func home(w http.ResponseWriter, r *http.Request) {
	htmlHeader := `<html>
	<head>Gen3 Hatchery</head>
	<body>`
	fmt.Fprintf(w, htmlHeader)

	for k, v := range Config.ContainersMap {
		fmt.Fprintf(w, "<h1><a href=\"%s/launch?hash=%s\">Launch %s - %s CPU - %s Memory</a></h1>", Config.Config.SubDir, k, v.Name, v.CPULimit, v.MemoryLimit)
	}

	htmlFooter := `</body>
	</html>`
	fmt.Fprintf(w, htmlFooter)

}

func paymodels(w http.ResponseWriter, r *http.Request) {
	userName := r.Header.Get("REMOTE_USER")
	if payModelExistsForUser(userName) {
		out, err := json.Marshal(Config.PayModelMap[userName])
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fmt.Fprintf(w, string(out))
	} else {
		http.Error(w, "Not Found", 404)
	}
}

func status(w http.ResponseWriter, r *http.Request) {
	userName := r.Header.Get("REMOTE_USER")
	accessToken := getBearerToken(r)

	pm := Config.PayModelMap[userName]
	if pm.Ecs == "true" {
		statusEcs(r.Context(), w, userName, accessToken)
	} else {
		result, err := statusK8sPod(r.Context(), userName, accessToken)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		out, err := json.Marshal(result)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		fmt.Fprintf(w, string(out))
	}
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
		http.Error(w, err.Error(), 500)
		return
	}

	fmt.Fprintf(w, string(out))
}

func launch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Not Found", 404)
		return
	}
	accessToken := getBearerToken(r)

	hash := r.URL.Query().Get("id")

	if hash == "" {
		http.Error(w, "Missing ID argument", 400)
		return
	}

	userName := r.Header.Get("REMOTE_USER")
	pm := Config.PayModelMap[userName]
	if pm.Ecs == "true" {
		launchEcs(w, r)
	} else {
		err := createK8sPod(r.Context(), string(hash), userName, accessToken)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fmt.Fprintf(w, "Success")
	}
}

func terminate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Not Found", 404)
		return
	}
	accessToken := getBearerToken(r)
	userName := r.Header.Get("REMOTE_USER")
	pm := Config.PayModelMap[userName]
	if pm.Ecs == "true" {
		terminateEcs(w, r)
	} else {
		err := deleteK8sPod(r.Context(), userName, accessToken)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
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

// Function to terminate workspace in ECS
func terminateEcs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Not Found", 404)
		return
	}
	accessToken := getBearerToken(r)
	userName := r.Header.Get("REMOTE_USER")
	if payModelExistsForUser(userName) {
		svc, err := terminateEcsWorkspace(r.Context(), userName, accessToken)
		if err != nil {
			fmt.Fprintf(w, fmt.Sprintf("%s", err))
		} else {
			fmt.Fprintf(w, fmt.Sprintf("%s", svc))
		}
	} else {
		http.Error(w, "Paymodel has not been setup for user", 404)
	}
}

// Function to launch workspace in ECS
// TODO: Evaluate this functionality
func launchEcs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Not Found", 404)
		return
	}
	hash := r.URL.Query().Get("id")

	if hash == "" {
		http.Error(w, "Missing ID argument", 400)
		return
	}

	accessToken := getBearerToken(r)
	userName := r.Header.Get("REMOTE_USER")
	if payModelExistsForUser(userName) {
		result, err := launchEcsWorkspace(r.Context(), userName, hash, accessToken)
		if err != nil {
			http.Error(w, fmt.Sprintf("%s", err), 500)
			Config.Logger.Printf("Error: %s", err)
		}

		fmt.Fprintf(w, fmt.Sprintf("%+v", result))

	} else {
		http.Error(w, "Paymodel has not been setup for user", 404)
	}
}

// Function to create ECS cluster.
// TODO: Evaluate the need for this!! Delete?
func ecsCluster(w http.ResponseWriter, r *http.Request) {
	userName := r.Header.Get("REMOTE_USER")
	if payModelExistsForUser(userName) {
		pm := Config.PayModelMap[userName]
		roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
		sess := session.Must(session.NewSession(&aws.Config{
			// TODO: Make this configurable
			Region: aws.String("us-east-1"),
		}))
		svc := NewSession(sess, roleARN)

		result, err := svc.launchEcsCluster(userName)
		if err != nil {
			fmt.Fprintf(w, fmt.Sprintf("%s", err))
			Config.Logger.Printf("Error: %s", err)
		} else {
			fmt.Fprintf(w, fmt.Sprintf("%s", result))
		}
	} else {
		http.Error(w, "Paymodel has not been setup for user", 404)
	}
}

// Function to check status of ECS workspace.
func statusEcs(ctx context.Context, w http.ResponseWriter, userName string, accessToken string) {
	if payModelExistsForUser(userName) {
		pm := Config.PayModelMap[userName]
		roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
		sess := session.Must(session.NewSession(&aws.Config{
			// TODO: Make this configurable
			Region: aws.String("us-east-1"),
		}))
		svc := NewSession(sess, roleARN)
		result, err := svc.statusEcsWorkspace(ctx, userName, accessToken)
		if err != nil {
			Config.Logger.Printf("Error: %s", err)
		}
		out, err := json.Marshal(result)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		fmt.Fprintf(w, string(out))
	} else {
		http.Error(w, "Paymodel has not been setup for user", 404)
	}
}

// API key related helper functions
// Make http request with header and body
func MakeARequestWithContext(ctx context.Context, method string, apiEndpoint string, accessToken string, contentType string, headers map[string]string, body *bytes.Buffer) (*http.Response, error) {
	if headers == nil {
		headers = make(map[string]string)
	}
	if accessToken != "" {
		headers["Authorization"] = "Bearer " + accessToken
	}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	client := &http.Client{Timeout: 10 * time.Second}
	var req *http.Request
	var err error
	if body == nil {
		req, err = http.NewRequestWithContext(ctx, method, apiEndpoint, nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, apiEndpoint, body)
	}

	if err != nil {
		return nil, errors.New("Error occurred during generating HTTP request: " + err.Error())
	}
	for k, v := range headers {
		req.Header.Add(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Error occurred during making HTTP request: " + err.Error())
	}
	return resp, nil
}

func getFenceURL() string {
	fenceURL := "http://fence-service/"
	_, ok := os.LookupEnv("BASE_URL")
	if ok {
		fenceURL = "https://" + os.Getenv("BASE_URL") + "/user/"
	}
	return fenceURL
}

func getAmbassadorURL() string {
	ambassadorURL := "http://ambassador-service/"
	_, ok := os.LookupEnv("BASE_URL")
	if ok {
		ambassadorURL = "https://" + os.Getenv("BASE_URL") + "/lw-workspace/proxy/"
	}
	return ambassadorURL
}

func getAPIKeyWithContext(ctx context.Context, accessToken string) (apiKey *APIKeyStruct, err error) {
	if accessToken == "" {
		return nil, errors.New("No valid access token")
	}

	fenceAPIKeyURL := getFenceURL() + "credentials/api/"
	body := bytes.NewBufferString("{\"scope\": [\"data\", \"user\"]}")

	resp, err := MakeARequestWithContext(ctx, "POST", fenceAPIKeyURL, accessToken, "application/json", nil, body)
	if err != nil {
		return nil, err
	}

	if resp != nil && resp.StatusCode != 200 {
		return nil, errors.New("Error occurred when creating API key with error code " + strconv.Itoa(resp.StatusCode))
	}
	defer resp.Body.Close()

	fenceApiKeyResponse := new(APIKeyStruct)
	err = json.NewDecoder(resp.Body).Decode(fenceApiKeyResponse)
	if err != nil {
		return nil, errors.New("Unable to decode API key response: " + err.Error())
	}
	return fenceApiKeyResponse, nil
}

func deleteAPIKeyWithContext(ctx context.Context, accessToken string, apiKeyID string) error {
	if accessToken == "" {
		return errors.New("No valid access token")
	}

	fenceDeleteAPIKeyURL := getFenceURL() + "credentials/api/" + apiKeyID
	resp, err := MakeARequestWithContext(ctx, "DELETE", fenceDeleteAPIKeyURL, accessToken, "", nil, nil)
	if err != nil {
		return err
	}
	if resp != nil && resp.StatusCode != 204 {
		return errors.New("Error occurred when deleting API key with error code " + strconv.Itoa(resp.StatusCode))
	}
	return nil
}

func getKernelIdleTimeWithContext(ctx context.Context, accessToken string) (lastActivityTime int64, err error) {
	if accessToken == "" {
		return -1, errors.New("No valid access token")
	}

	workspaceKernelStatusURL := getAmbassadorURL() + "api/status"
	resp, err := MakeARequestWithContext(ctx, "GET", workspaceKernelStatusURL, accessToken, "", nil, nil)
	if err != nil {
		return -1, err
	}
	if resp != nil && resp.StatusCode != 200 {
		return -1, errors.New("Error occurred when getting workspace kernel status with error code " + strconv.Itoa(resp.StatusCode))
	}
	defer resp.Body.Close()

	workspaceKernelStatusResponse := new(WorkspaceKernelStatusStruct)
	err = json.NewDecoder(resp.Body).Decode(workspaceKernelStatusResponse)
	if err != nil {
		return -1, errors.New("Unable to decode workspace kernel status response: " + err.Error())
	}
	lastAct, err := time.Parse(time.RFC3339, workspaceKernelStatusResponse.LastActivityTime)
	if err != nil {
		return -1, errors.New("Unable to parse last activity time: " + err.Error())
	}
	return lastAct.UnixMilli(), nil
}
