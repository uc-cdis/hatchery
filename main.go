package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/uc-cdis/hatchery/hatchery"
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
	config.Logger.Printf("Running main")
	hatchery.RegisterSystem()
	hatchery.RegisterHatchery()
	log.Fatal(http.ListenAndServe("0.0.0.0:8000", nil))
}
