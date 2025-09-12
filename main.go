package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/uc-cdis/hatchery/hatchery"
)

func verifyPath(userPath string, baseDir string) (string, error) {
	fullPath := filepath.Join(baseDir, userPath)
	canonicalPath := filepath.Clean(fullPath)

	resolved, err := filepath.EvalSymlinks(canonicalPath)
	if err != nil {
		return canonicalPath, errors.New("unsafe or invalid path specified")
	}

	if !strings.HasPrefix(canonicalPath, filepath.Clean(baseDir)+string(os.PathSeparator)) {
		return canonicalPath, errors.New("access denied: cannot read config files outside of the base directory")
	}

	if strings.ToLower(filepath.Ext(canonicalPath)) != ".json" {
		return canonicalPath, errors.New("config file must be json")
	}

	return resolved, nil
}

func main() {
	configPath := "/hatchery.json"
	if len(os.Args) > 2 && strings.HasSuffix(os.Args[1], "-config") {
		configPath = os.Args[2]
	} else if len(os.Args) > 1 {
		os.Stderr.WriteString(
			`Use: hatchery -config path/to/hatchery.json
		- also harvests dockstore/bla.yml app definitions where dockstore/
		  is in the same folder as hatchery.json
`)
		return
	}
	logger := log.New(os.Stdout, "", log.LstdFlags)
	baseDir, err := os.Getwd()
	if err != nil {
		logger.Printf("Error in getting baseDir of executable - %s", err.Error())
		return
	}
	cleanPath, err := verifyPath(configPath, baseDir)
	if err != nil {
		logger.Printf("Failed to verify config - got %s", err.Error())
		return
	}
	config, err := hatchery.LoadConfig(cleanPath, logger)
	if err != nil {
		message := err.Error()
		if os.IsPermission(err) {
			message = "permission issue"
		}
		config.Logger.Printf("Failed to load config - got %s", message)
		return
	}
	hatchery.Config = config

	config.Logger.Printf("Setting up routes")
	hatchery.RegisterSystem()
	hatchery.RegisterHatchery()

	config.Logger.Printf("Running main")
	log.Fatal(http.ListenAndServe("0.0.0.0:8000", nil))
}
