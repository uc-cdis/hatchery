package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/uc-cdis/hatchery/hatchery"
	"go.uber.org/zap"
	httptrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/net/http"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
	"gopkg.in/DataDog/dd-trace-go.v1/profiler"
)

func verifyPath(path string) (string, error) {
	c := filepath.Clean(path)
	r, err := filepath.EvalSymlinks(c)
	if err != nil {
		return c, errors.New("Unsafe or invalid path specified")
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
	zapLogger, _ := zap.NewProduction()
	defer zapLogger.Sync()
	logger := zapLogger.Sugar()

	cleanPath, err := verifyPath(configPath)
	if err != nil {
		// logger.Printf(fmt.Sprintf("Failed to load config - got %v", err))
		logger.Errorf("Failed to load config - got %v", err)
		return
	}
	config, err := hatchery.LoadConfig(cleanPath, logger)
	if err != nil {
		logger.Errorf("Failed to load config - got %v", err)
		return
	}
	hatchery.Config = config
	ddEnabled := os.Getenv("DD_ENABLED")
	if strings.ToLower(ddEnabled) == "true" {
		// config.Logger.Printf("Setting up datadog")
		config.Logger.Infow("Setting up datadog")
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
			// config.Logger.Printf("DD profiler setup failed with error: %s", err)
			logger.Errorw("Failed to setup DD profiler",
				"error", err,
			)
		}
		defer profiler.Stop()
	} else {
		// config.Logger.Printf("Datadog not enabled in manifest, skipping...")
		config.Logger.Infow("Datadog not enabled in manifest, skipping...")
	}

	// config.Logger.Printf("Setting up routes")
	config.Logger.Infow("Setting up routes for hatchery api")
	mux := httptrace.NewServeMux()
	hatchery.RegisterSystem(mux)
	hatchery.RegisterHatchery(mux)

	// config.Logger.Printf("Running main")
	config.Logger.Infow("Running main")
	log.Fatal(http.ListenAndServe("0.0.0.0:8000", mux))
}
