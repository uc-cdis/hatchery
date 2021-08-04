package hatchery

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	httptrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/net/http"
)

// Config package-global shared hatchery config
var Config *FullHatcheryConfig

// RegisterHatchery setup endpoints with the http engine
func RegisterHatchery(mux *httptrace.ServeMux) {
	mux.HandleFunc("/", home)
	mux.HandleFunc("/launch", launch)
	mux.HandleFunc("/findecs", findEcs)
	mux.HandleFunc("/launchecs", launchEcs)
	mux.HandleFunc("/terminate", terminate)
	mux.HandleFunc("/status", status)
	mux.HandleFunc("/options", options)
	mux.HandleFunc("/paymodels", paymodels)
}

func launchEcs(w http.ResponseWriter, r *http.Request) {
	userName := r.Header.Get("REMOTE_USER")
	if payModelExistsForUser(userName) {
		result, err := launchEcsCluster()
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

func findEcs(w http.ResponseWriter, r *http.Request) {
	userName := r.Header.Get("REMOTE_USER")
	if payModelExistsForUser(userName) {
		result, err := findEcsCluster()
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

	result, err := statusK8sPod(userName)
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

func options(w http.ResponseWriter, r *http.Request) {
	type container struct {
		Name        string `json:"name"`
		CPULimit    string `json:"cpu-limit"`
		MemoryLimit string `json:"memory-limit"`
		ID          string `json:"id"`
	}
	var options []container
	for k, v := range Config.ContainersMap {
		c := container{
			Name:        v.Name,
			CPULimit:    v.CPULimit,
			MemoryLimit: v.MemoryLimit,
			ID:          k,
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
	err := createK8sPod(string(hash), accessToken, userName)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	fmt.Fprintf(w, "Success")
}

func terminate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Not Found", 404)
		return
	}
	userName := r.Header.Get("REMOTE_USER")

	err := deleteK8sPod(userName)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	fmt.Fprintf(w, "Terminated workspace")
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
