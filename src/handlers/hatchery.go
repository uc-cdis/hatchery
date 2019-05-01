package handlers

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
)

func RegisterHatchery() {
	http.HandleFunc("/", dispatch)
	http.HandleFunc("/spawn", spawn)
	http.HandleFunc("/despawn", despawn)
	http.HandleFunc("/workspace", workspace)
}

func workspace(w http.ResponseWriter, r *http.Request) {
	userName := r.Header.Get("REMOTE_USER")
	
}

func spawn(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Not Found", 404)
		return
	}
	accessToken := getBearerToken(r)

	inputData, err := ioutil.ReadAll(r.Body)
	defer r.Body.Close()

	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	userName := r.Header.Get("REMOTE_USER")

	result, err := createK8sJob(string(inputData), *accessToken, userName)
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

func status(w http.ResponseWriter, r *http.Request) {
	UID := r.URL.Query().Get("UID")
	if UID != "" {
		result, errUID := getJobStatusByID(UID)
		if errUID != nil {
			http.Error(w, errUID.Error(), 500)
			return
		}

		out, err := json.Marshal(result)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		fmt.Fprintf(w, string(out))
	} else {
		http.Error(w, "Missing UID argument", 300)
		return
	}
}

func output(w http.ResponseWriter, r *http.Request) {
	UID := r.URL.Query().Get("UID")
	if UID != "" {
		result, errUID := getJobLogs(UID)
		if errUID != nil {
			http.Error(w, errUID.Error(), 500)
			return
		}

		out, err := json.Marshal(result)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		fmt.Fprintf(w, string(out))
	} else {
		http.Error(w, "Missing UID argument", 300)
		return
	}
}

func getBearerToken(r *http.Request) *string {
	authHeader := r.Header.Get("Authorization")
	fmt.Println("header: ", authHeader)
	if authHeader == "" {
		return nil
	}
	s := strings.SplitN(authHeader, " ", 2)
	if len(s) == 2 && strings.ToLower(s[0]) == "bearer" {
		return &s[1]
	}
	return nil
}
