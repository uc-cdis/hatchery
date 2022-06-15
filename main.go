package main

import (
	"flag"
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
	configPath := flag.String("config", "/hatchery.json", "Use: hatchery --config path/to/hatchery.json")
	job := flag.String("job", "", "Job to kick off")
	flag.Parse()
	config, err := hatchery.LoadConfig(*configPath, log.New(os.Stdout, "", log.LstdFlags))
	if err != nil {
		config.Logger.Printf(fmt.Sprintf("Failed to load config - got %v", err))
		return
	}
	hatchery.Config = config

	if *job != "" {
		switch job := *job; job {
		case "LaunchEcsWorkspace":
			userName := os.Getenv("USER")
			config.Logger.Printf("Launching ECS Workspace for %s\n", userName)
			// payModel, err := hatchery.GetCurrentPayModel(userName)
			if err != nil {
				config.Logger.Fatalf("Error getting PayModel: %s\n", err)
				return
			}
			// hatchery.LaunchEcsWorkspace(context.TODO(), userName, os.Getenv("HASH"), os.Getenv("ACCESS_TOKEN"), *payModel)
			hatchery.LaunchEcsWsJob(userName)
		default:
			config.Logger.Fatalf("No such job defined: %s\n", job)
		}
		return
	}

	ddEnabled := os.Getenv("DD_ENABLED")
	if strings.ToLower(ddEnabled) == "true" {
		config.Logger.Printf("Setting up datadog")
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
		config.Logger.Printf("Datadog not enabled in manifest, skipping...")
	}

	config.Logger.Printf("Setting up routes")
	mux := httptrace.NewServeMux()
	hatchery.RegisterSystem(mux)
	hatchery.RegisterHatchery(mux)

	config.Logger.Printf("Running main")
	log.Fatal(http.ListenAndServe("0.0.0.0:8000", mux))
}
