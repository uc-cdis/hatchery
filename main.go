package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/uc-cdis/hatchery/hatchery"
	httptrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/net/http"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
	"gopkg.in/DataDog/dd-trace-go.v1/profiler"
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
		config.Logger.Print("Setting up datadog")
		tracer.Start()
		defer tracer.Stop()
		if err := profiler.Start(
			profiler.WithProfileTypes(
				profiler.CPUProfile,
				profiler.HeapProfile,

				// The profiles below are disabled by default to keep overhead low, but can be enabled as needed.
				// profiler.BlockProfile,
				// profiler.MutexProfile,
				// profiler.GoroutineProfile,
			),
		); err != nil {
			config.Logger.Printf("DD profiler setup failed with error: %s", err)
		}
		defer profiler.Stop()
	} else {
		config.Logger.Print("Datadog not enabled in manifest, skipping...")
	}

	if config.Config.LicensesDynamodbTable != "" {
		config.Logger.Printf("Using licenses table %s", config.Config.LicensesDynamodbTable)
		err = hatchery.SetupLicensesTable()
		if err != nil {
			config.Logger.Printf("Error setting up licenses table %v", err)
		} else {
			licenseFile := os.Getenv("LICENSES_FILE")
			if licenseFile == "" {
				if _, err := os.Stat("/licenses.json"); err == nil {
					licenseFile = "/licenses.json"
				}
			}
			if licenseFile != "" {
				config.Logger.Printf("Loading licenses from file %s", licenseFile)
				err := hatchery.LoadLicensesTableFromFile(licenseFile)
				if err != nil {
					config.Logger.Printf("Error populating licenses table from file: %v", err)
				}
			}

			go hatchery.RevokeExpiredLicenses()
		}
	}

	config.Logger.Print("Setting up routes")
	mux := httptrace.NewServeMux()
	hatchery.RegisterSystem(mux)
	hatchery.RegisterHatchery(mux)

	config.Logger.Print("Running main")
	log.Fatal(http.ListenAndServe("0.0.0.0:8000", mux))
}
