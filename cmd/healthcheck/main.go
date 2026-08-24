package main

import (
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	client := http.Client{Timeout: time.Second}
	response, err := client.Get(os.Args[1])
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		os.Exit(1)
	}
	_ = response.Body.Close()
}
