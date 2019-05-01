package main

import (
	"fmt"
	"handlers"
	"log"
	"net/http"
)

func main() {
	fmt.Println("Running main")
	http.HandleFunc("/", repoHandler)
	handlers.RegisterSystem()
	handlers.RegisterHatchery()
	go handlers.StartMonitoringProcess()
	log.Fatal(http.ListenAndServe("0.0.0.0:8000", nil))
}
