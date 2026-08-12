// Command server is the compose-app service: a minimal HTTP server that
// binds API_PORT (or 8080) and serves /healthz. It is a fixture standing in
// for a real pilot application; phase 5 wires it to the generated reader.
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("API_PORT")
	if port == "" {
		port = "8080"
	}
	addr := "127.0.0.1:" + port
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	fmt.Println("compose-app listening on", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
