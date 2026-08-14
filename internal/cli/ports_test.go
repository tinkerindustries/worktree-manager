package cli

// ports_test.go exercises `wt ports scan` through the real client against
// a fake coordinator: the verb dials, requests, and renders — and the
// usage and unreachable paths are covered like every other coordinator
// verb.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
)

// TestRunPortsScanJSON: `wt ports scan --json` prints exactly one JSON
// object with the listeners sorted by port.
func TestRunPortsScanJSON(t *testing.T) {
	ep := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"ports.scan": canned(&api.Response{Result: json.RawMessage(
			`{"listeners":[{"port":4200,"pid":123,"command":"compose-app-dev"},{"port":5319,"pid":456,"command":"compose-app-prod"}],"notes":["a note"]}`)}),
	})
	t.Setenv("WT_ENDPOINT", ep)

	code, stdout, stderr := runCLI(t, "ports", "scan", "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	var res api.PortsScanResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if len(res.Listeners) != 2 || res.Listeners[0].Port != 4200 || res.Listeners[1].Command != "compose-app-prod" {
		t.Errorf("scan result = %+v", res)
	}
}

// TestRunPortsScanHuman: the text form lists port, pid and command, and
// the bounded-coverage notes go to stderr.
func TestRunPortsScanHuman(t *testing.T) {
	ep := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"ports.scan": canned(&api.Response{Result: json.RawMessage(
			`{"listeners":[{"port":4200,"pid":123,"command":"compose-app-dev"}],"notes":["this coordinator runs inside a container"]}`)}),
	})
	t.Setenv("WT_ENDPOINT", ep)

	code, stdout, stderr := runCLI(t, "ports", "scan")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	for _, want := range []string{"PORT", "PID", "COMMAND", "4200", "123", "compose-app-dev"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "inside a container") {
		t.Errorf("the bounded-coverage note did not reach stderr:\n%s", stderr)
	}
}

// TestRunPortsScanUnavailable: a scan that cannot see every listener
// exits 4 with the remedy naming the missing discovery tool.
func TestRunPortsScanUnavailable(t *testing.T) {
	ep := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"ports.scan": canned(&api.Response{Error: &api.Error{
			Code:   4,
			Msg:    "listener discovery needs lsof, which is unavailable: it is not installed or not on PATH",
			Remedy: "install the discovery tool the message names, then re-run: wt ports scan",
		}}),
	})
	t.Setenv("WT_ENDPOINT", ep)

	code, _, stderr := runCLI(t, "ports", "scan")
	if code != ExitUnavailable {
		t.Errorf("exit = %d, want 4", code)
	}
	if !strings.Contains(stderr, "wt ports scan") {
		t.Errorf("stderr lacks the remedy:\n%s", stderr)
	}
}

// TestRunPortsScanUnreachableExits5: with the coordinator stopped, ports
// scan exits 5 naming the start command.
func TestRunPortsScanUnreachableExits5(t *testing.T) {
	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:1")
	code, _, stderr := runCLI(t, "ports", "scan")
	if code != ExitUnreachable {
		t.Errorf("exit = %d, want 5", code)
	}
	if !strings.Contains(stderr, "fix:") {
		t.Errorf("stderr lacks a remedy:\n%s", stderr)
	}
}

// TestRunPortsVerbUsage: ports needs the scan subverb, and help lists it.
func TestRunPortsVerbUsage(t *testing.T) {
	code, _, _ := runCLI(t, "ports")
	if code != ExitUsage {
		t.Errorf("bare ports exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "ports", "bogus")
	if code != ExitUsage {
		t.Errorf("unknown ports verb exit = %d, want 2", code)
	}
	code, stdout, _ := runCLI(t, "help")
	if code != ExitOK || !strings.Contains(stdout, "ports scan") {
		t.Errorf("help lacks the ports verb:\n%s", stdout)
	}
}

// TestRunBandsSuggestJSON: `wt bands suggest --json` sends the committed
// spec to the coordinator and prints the suggestions.
func TestRunBandsSuggestJSON(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	var got api.SuggestBandArgs
	ep := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"bands.suggest": func(req *api.Request) *api.Response {
			if err := json.Unmarshal(req.Args, &got); err != nil {
				t.Errorf("decoding the suggest request: %v", err)
			}
			return canned(&api.Response{Result: json.RawMessage(
				`{"app":"plain-app","suggestions":[{"resource":"api","base":8200,"span":32,"low":8200,"high":8231}]}`)})(req)
		},
	})
	t.Setenv("WT_ENDPOINT", ep)

	code, stdout, stderr := runCLI(t, "bands", "suggest", "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	if got.Spec.App != "plain-app" {
		t.Errorf("request did not carry the committed spec: %+v", got.Spec)
	}
	var res api.SuggestBandResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if len(res.Suggestions) != 1 || res.Suggestions[0].Base != 8200 {
		t.Errorf("suggest result = %+v", res)
	}
}

// TestRunBandsSuggestExplicitSpec: --spec names the file instead of the
// walk-up rule.
func TestRunBandsSuggestExplicitSpec(t *testing.T) {
	// Resolve the fixture spec against the package's own cwd before
	// chdir moves the process elsewhere.
	specPath, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "compose-app", "wt.yaml"))
	if err != nil {
		t.Fatalf("resolving the spec path: %v", err)
	}
	chdir(t, t.TempDir())
	ep := fakeCoordServer(t, map[string]func(*api.Request) *api.Response{
		"bands.suggest": canned(&api.Response{Result: json.RawMessage(
			`{"app":"compose-app","suggestions":[{"resource":"api","base":1,"span":64,"low":1,"high":64}]}`)}),
	})
	t.Setenv("WT_ENDPOINT", ep)

	code, stdout, stderr := runCLI(t, "bands", "suggest", "--spec", specPath)
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	for _, want := range []string{"compose-app", "api", "1..64"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
}

// TestRunBandsSuggestNotAdopted: outside a repository, suggest with no
// --spec reports not adopted with exit 4, never a socket error.
func TestRunBandsSuggestNotAdopted(t *testing.T) {
	chdir(t, t.TempDir())
	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:1")
	code, _, stderr := runCLI(t, "bands", "suggest")
	if code != ExitUnavailable {
		t.Errorf("exit = %d, want 4 (not adopted)", code)
	}
	if !strings.Contains(stderr, "wt.yaml") {
		t.Errorf("stderr does not name the missing spec:\n%s", stderr)
	}
}

// TestRunBandsSuggestUnreachableExits5: with the coordinator stopped,
// suggest exits 5 naming the start command.
func TestRunBandsSuggestUnreachableExits5(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:1")
	code, _, stderr := runCLI(t, "bands", "suggest")
	if code != ExitUnreachable {
		t.Errorf("exit = %d, want 5", code)
	}
	if !strings.Contains(stderr, "wtd") && !strings.Contains(stderr, "install") {
		t.Errorf("stderr lacks the start command:\n%s", stderr)
	}
}
