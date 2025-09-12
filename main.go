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

func verifyPath(path string) (string, error) {
	c := filepath.Clean(path)
	r, err := filepath.EvalSymlinks(c)
	if err != nil {
		return c, errors.New("Unsafe or invalid path specified")
	}
	if strings.ToLower(filepath.Ext(c)) != ".json" {
		return c, errors.New("config file must be json")
	}
	return r, nil
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
	cleanPath, err := verifyPath(configPath)
	if err != nil {
		logger.Printf("Failed to load config - got %s", err.Error())
		return
	}
	config, err := hatchery.LoadConfig(cleanPath, logger)
	if err != nil {
		config.Logger.Printf("Failed to load config - got %s", err.Error())
		return
	}
	hatchery.Config = config

	config.Logger.Printf("Setting up routes")
	hatchery.RegisterSystem()
	hatchery.RegisterHatchery()

	config.Logger.Printf("Running main")
	log.Fatal(http.ListenAndServe("0.0.0.0:8000", nil))
}
