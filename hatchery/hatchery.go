package hatchery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

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

func getCurrentUserName(r *http.Request) (userName string) {
	return r.Header.Get("REMOTE_USER")
}

func paymodels(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	userName := getCurrentUserName(r)
	paymodel, err := getPayModelForUser(userName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out, err := json.Marshal(paymodel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, string(out))
}

func status(w http.ResponseWriter, r *http.Request) {
	userName := getCurrentUserName(r)
	accessToken := getBearerToken(r)

	paymodel, err := getPayModelForUser(userName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var result *WorkspaceStatus
	if paymodel.Ecs == "true" {
		result, err = statusEcs(r.Context(), userName, accessToken)

	} else {
		result, err = statusK8sPod(r.Context(), userName, accessToken)
	}
	if err != nil {
		if err.Error() == "Paymodel has not been setup for user" {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out, err := json.Marshal(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprintf(w, string(out))
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

	fmt.Fprintf(w, string(out))
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
	paymodel, err := getPayModelForUser(userName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if paymodel.Ecs == "true" {
		launchEcs(w, r)
	} else {
		err := createK8sPod(r.Context(), string(hash), userName, accessToken)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "Success")
	}
}

func terminate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	accessToken := getBearerToken(r)
	userName := getCurrentUserName(r)
	paymodel, err := getPayModelForUser(userName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if paymodel.Ecs == "true" {
		terminateEcs(w, r)
	} else {
		err := deleteK8sPod(r.Context(), userName, accessToken)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	accessToken := getBearerToken(r)
	userName := getCurrentUserName(r)
	if payModelExistsForUser(userName) {
		svc, err := terminateEcsWorkspace(r.Context(), userName, accessToken)
		if err != nil {
			fmt.Fprintf(w, fmt.Sprintf("%s", err))
		} else {
			fmt.Fprintf(w, fmt.Sprintf("%s", svc))
		}
	} else {
		http.Error(w, "Paymodel has not been setup for user", http.StatusNotFound)
	}
}

// Function to launch workspace in ECS
// TODO: Evaluate this functionality
func launchEcs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	hash := r.URL.Query().Get("id")

	if hash == "" {
		http.Error(w, "Missing ID argument", http.StatusBadRequest)
		return
	}

	accessToken := getBearerToken(r)
	userName := getCurrentUserName(r)
	if payModelExistsForUser(userName) {
		result, err := launchEcsWorkspace(r.Context(), userName, hash, accessToken)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			Config.Logger.Printf("Error: %s", err)
		}

		fmt.Fprintf(w, fmt.Sprintf("%+v", result))
	} else {
		http.Error(w, "Paymodel has not been setup for user", http.StatusNotFound)
	}
}

// Function to create ECS cluster.
// TODO: NEED TO CALL THIS FUNCTION IF IT DOESN'T EXIST!!!
func ecsCluster(w http.ResponseWriter, r *http.Request) {
	userName := getCurrentUserName(r)
	if payModelExistsForUser(userName) {
		paymodel, err := getPayModelForUser(userName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		roleARN := "arn:aws:iam::" + paymodel.AWSAccountId + ":role/csoc_adminvm"
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
		http.Error(w, "Paymodel has not been setup for user", http.StatusNotFound)
	}
}

// Function to check status of ECS workspace.
func statusEcs(ctx context.Context, userName string, accessToken string) (*WorkspaceStatus, error) {
	if payModelExistsForUser(userName) {
		paymodel, err := getPayModelForUser(userName)
		if err != nil {
			return nil, err
		}
		roleARN := "arn:aws:iam::" + paymodel.AWSAccountId + ":role/csoc_adminvm"
		sess := session.Must(session.NewSession(&aws.Config{
			// TODO: Make this configurable
			Region: aws.String("us-east-1"),
		}))
		svc := NewSession(sess, roleARN)
		result, err := svc.statusEcsWorkspace(ctx, userName, accessToken)
		if err != nil {
			Config.Logger.Printf("Error: %s", err)
			return nil, err
		}
		return result, nil
	} else {
		return nil, errors.New("Paymodel has not been setup for user")
	}
}
