// plain-app's server: the runnable half phase 7 gives the fixture
// (plan.md §4.1: "phases 7 and 8 do the same for the other two [fixtures]
// as they need them"). It binds the worktree's allocated port and serves
// /healthz so the health hook has a real probe; -healthcheck dials the
// same endpoint and exits 0 or 1, which is what the hook runs.
//
// Everything comes from the environment — API_PORT, DB_PATH, CACHE_PATH,
// SHARED_DB_PATH — which the worktree allocation delivers through the .env
// managed block and the hook environment, never from a hardcoded value.
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "dial the health endpoint and exit 0 or 1")
	flag.Parse()

	port := os.Getenv("API_PORT")
	if port == "" {
		fatal("API_PORT is unset: run this from a worktree's allocated environment, or source the .env")
	}

	if *healthcheck {
		code := 1
		client := &http.Client{Timeout: 2 * time.Second}
		if resp, err := client.Get("http://127.0.0.1:" + port + "/healthz"); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				code = 0
			}
		}
		os.Exit(code)
	}

	addr := "127.0.0.1:" + port
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fatal("binding " + addr + ": " + err.Error())
	}

	fmt.Printf("plain-app listening on http://%s\n", addr)
	fmt.Printf("db: %s\n", envOr("DB_PATH", "(unset)"))
	fmt.Printf("cache: %s\n", envOr("CACHE_PATH", "(unset)"))
	fmt.Printf("shared_db: %s\n", envOr("SHARED_DB_PATH", "(unset)"))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "plain-app on %s\ndb: %s\ncache: %s\nshared: %s\n",
			addr, envOr("DB_PATH", "-"), envOr("CACHE_PATH", "-"), envOr("SHARED_DB_PATH", "-"))
	})
	if err := http.Serve(ln, mux); err != nil {
		fatal("serving: " + err.Error())
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "plain-app:", msg)
	os.Exit(1)
}
