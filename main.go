package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/uc-cdis/hatchery/hatchery"
)

func main() {

	config, err := hatchery.LoadConfig("/hatchery.json", log.New(os.Stdout, "", log.LstdFlags))
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
