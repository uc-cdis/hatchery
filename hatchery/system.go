package hatchery

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/uc-cdis/hatchery/hatchery/version"
)

type versionSummary struct {
	Commit  string `json:"commit"`
	Version string `json:"version"`
}

func RegisterSystem() {
	http.HandleFunc("/_status", systemStatus)
	http.HandleFunc("/_version", systemVersion)
}

func systemStatus(w http.ResponseWriter, r *http.Request) {
	if _, err := fmt.Fprintf(w, "Healthy"); err != nil {
		Config.Logger.Printf("Error writing system status response: %v", err)
	}
}

func systemVersion(w http.ResponseWriter, r *http.Request) {
	ver := versionSummary{Commit: version.GitCommit, Version: version.GitVersion}
	out, err := json.Marshal(ver)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if _, err = fmt.Fprint(w, string(out)); err != nil {
		Config.Logger.Printf("Error writing status response: %v", err)
	}
}
