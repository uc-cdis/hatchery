package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/uc-cdis/hatchery/hatchery"
)

func main() {
	fmt.Println("Running main")
	hatchery.RegisterSystem()
	hatchery.RegisterHatchery()
	log.Fatal(http.ListenAndServe("0.0.0.0:8000", nil))
}
