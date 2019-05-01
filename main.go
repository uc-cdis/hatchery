package main

import (
	"fmt"
	"handlers"
	"log"
	"net/http"
)

func main() {
	fmt.Println("Running main")
	handlers.RegisterSystem()
	handlers.RegisterHatchery()
	log.Fatal(http.ListenAndServe("0.0.0.0:8000", nil))
}
