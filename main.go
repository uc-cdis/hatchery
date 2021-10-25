package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/uc-cdis/hatchery/hatchery"
	httptrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/net/http"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

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
	config, err := hatchery.LoadConfig(configPath, log.New(os.Stdout, "", log.LstdFlags))
	if err != nil {
		config.Logger.Printf(fmt.Sprintf("Failed to load config - got %v", err))
		return
	}

	hatchery.Config = config
	ddEnabled := os.Getenv("DD_ENABLED")
	if strings.ToLower(ddEnabled) == "true" {
		config.Logger.Printf("Setting up datadog")
		tracer.Start()
		defer tracer.Stop()
	} else {
		config.Logger.Printf("Datadog not enabled in manifest, skipping...")
	}

	licenseFiles, err := ioutil.ReadDir("/licenses/")
	if err != nil {
		config.Logger.Printf("No licenses available. %v", err)
	} else {
		config.Licenses = make(map[string]*hatchery.License)
		for _, licenseFile := range licenseFiles {
			config.Logger.Printf("Setting up licenses: %v", licenseFile.Name())
			license, err := hatchery.NewLicense("/licenses/" + licenseFile.Name())
			if nil != err {
				config.Logger.Printf("Error initializing license %v: %v", license.Name, err)
			} else {
				config.Logger.Printf("Initialized license %v", license.Name)
				config.Licenses[license.Name] = license
			}
		}
	}
	config.Logger.Printf("Setting up routes")
	mux := httptrace.NewServeMux()
	hatchery.RegisterSystem(mux)
	hatchery.RegisterHatchery(mux)

	config.Logger.Printf("Running main")
	log.Fatal(http.ListenAndServe("0.0.0.0:8000", mux))
}
