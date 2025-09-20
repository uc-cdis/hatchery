package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/uc-cdis/hatchery/hatchery"
)

func main() {
	configPath := "/var/hatchery/hatchery.json"
	devMode := false

	cleanedArgs := []string{}
	for _, arg := range os.Args {
		if arg == "-dev" {
			devMode = true
			configPath = "./hatchery.json"
		} else {
			cleanedArgs = append(cleanedArgs, arg)
		}
	}
	os.Args = cleanedArgs

	if len(cleanedArgs) > 2 && strings.HasSuffix(cleanedArgs[1], "-config") {
		configPath = cleanedArgs[2]
	} else if len(cleanedArgs) > 1 {
		os.Stderr.WriteString(
			`Use: hatchery -config path/to/hatchery.json
		- also harvests dockstore/bla.yml app definitions where dockstore/
		  is in the same folder as hatchery.json
`)
		return
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)

	var baseDir string
	var err error
	if devMode {
		baseDir, err = os.Getwd()
		if err != nil {
			logger.Print("Error in getting baseDir of executable\n")
			return
		}
	} else {
		baseDir = "/var/hatchery"
	}

	cleanPath, err := hatchery.VerifyPath(configPath, baseDir)
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
