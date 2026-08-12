// Command server is the compose-app service: a minimal HTTP server that
// binds the worktree's API port and serves /healthz. It is the fixture
// standing in for a real pilot application; phase 5 wires it to the
// generated descriptor reader (internal/env, 05-delivery.md §4).
//
// The port follows the reader's precedence: an explicit --port beats
// everything; --env (or the COMPOSE_APP_ENV variable) names a descriptor;
// otherwise the descriptor found from cwd decides — and in the primary
// checkout, or a repository that has not adopted the tooling, the legacy
// default 8080 stands in for the committed defaults slot 0 keeps. The
// resolution is reported so a human can see which source won
// (B18.3: env_source and env_path).
//
// -healthcheck is the compose healthcheck's probe: an HTTP GET against the
// /healthz endpoint on the port this server would serve, exiting 0 on 200
// and 1 otherwise. It exists because the runtime image carries no curl or
// wget — the probe is the binary itself, so the check works in whatever
// image this builds.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"example.com/compose-app/internal/env"
)

func main() {
	flagEnv := flag.String("env", "", "path to a wt descriptor (beats "+env.EnvVar+")")
	flagPort := flag.Int("port", 0, "explicit API port (beats everything)")
	flagHealthcheck := flag.Bool("healthcheck", false, "probe /healthz on the port this server serves, exit 0/1")
	flag.Parse()

	explicit := map[string]string{}
	if *flagPort != 0 {
		explicit["api"] = strconv.Itoa(*flagPort)
	}
	e, err := env.Resolve(*flagEnv, explicit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// The legacy default: in the primary checkout, or without a spec, the
	// committed defaults stand — slot 0 is never managed. Inside the
	// container there is no spec and no git, so this is the resolution that
	// applies there, and it is the port the compose mapping publishes.
	port := "8080"
	if e.Source != env.SourceLegacy {
		port = e.Resources["api"]
	}

	if *flagHealthcheck {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
		if err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "healthcheck: status %s\n", resp.Status)
			os.Exit(1)
		}
		return
	}

	addr := "127.0.0.1:" + port
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	fmt.Printf("compose-app env_source=%s env_path=%s port=%s\n", e.Source, e.EnvPath, port)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
