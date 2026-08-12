package cli

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
)

// fakeCoordServer is a stand-in for the coordinator: it answers the hello
// and then serves requests from a per-verb handler map, so the client's
// dial, request and output paths run end to end without importing
// coordinator code into the client's tests. The handler sees the request
// and returns the response, which lets a test both inspect what the client
// sent and control what it gets back.
func fakeCoordServer(t *testing.T, sock string, handlers map[string]func(*protocol.Request) *protocol.Response) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("fake coordinator listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				bw := bufio.NewWriter(conn)
				var hello protocol.Hello
				if err := protocol.ReadMessage(br, &hello); err != nil {
					return
				}
				protocol.WriteMessage(bw, &protocol.HelloReply{Agreed: hello.MaxVer})
				bw.Flush()
				for {
					var req protocol.Request
					if err := protocol.ReadMessage(br, &req); err != nil {
						return
					}
					handle, ok := handlers[req.Verb]
					if !ok {
						handle = func(*protocol.Request) *protocol.Response {
							return &protocol.Response{Error: &protocol.Error{
								Code: 1, Msg: "fake coordinator: no canned response for " + req.Verb,
								Remedy: "test defect",
							}}
						}
					}
					if err := protocol.WriteMessage(bw, handle(&req)); err != nil {
						return
					}
					bw.Flush()
				}
			}()
		}
	}()
}

// canned is the handler for a fixed response.
func canned(resp *protocol.Response) func(*protocol.Request) *protocol.Response {
	return func(*protocol.Request) *protocol.Response { return resp }
}

// TestRunBandsListJSON: `wt bands list --json` prints exactly one JSON
// object with the ledger's two halves.
func TestRunBandsListJSON(t *testing.T) {
	sock := shortSock(t, "s")
	fakeCoordServer(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"bands.list": canned(&protocol.Response{Result: json.RawMessage(
			`{"bands":[{"app":"plain-app","bases":{"api":8200}},{"app":"compose-app","bases":{"api":4200,"proxy":4200}}],` +
				`"reservations":[{"ports":[5319,5320],"note":"compose-app production stack"}]}`)}),
	})
	t.Setenv("WT_SOCKET", sock)

	code, stdout, stderr := runCLI(t, "bands", "list", "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	var res protocol.BandsListResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if len(res.Bands) != 2 || len(res.Reservations) != 1 {
		t.Fatalf("bands.list result = %+v", res)
	}
	apps := map[string]int{}
	for _, b := range res.Bands {
		apps[b.App] = b.Bases["api"]
	}
	if apps["compose-app"] != 4200 || apps["plain-app"] != 8200 {
		t.Errorf("bands = %+v", res.Bands)
	}
	if res.Reservations[0].Note != "compose-app production stack" {
		t.Errorf("reservations = %+v", res.Reservations)
	}
	// Exactly one JSON object on stdout: the whole output is one value, and
	// nothing follows it.
	if !strings.HasSuffix(stdout, "\n") || len(strings.TrimSpace(stdout)) == 0 {
		t.Errorf("stdout is not exactly one newline-terminated JSON object:\n%s", stdout)
	}
}

// TestRunBandsListHuman: the text form names both halves of the ledger.
func TestRunBandsListHuman(t *testing.T) {
	sock := shortSock(t, "s")
	fakeCoordServer(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"bands.list": canned(&protocol.Response{Result: json.RawMessage(
			`{"bands":[{"app":"compose-app","bases":{"api":4200,"proxy":4200}}],` +
				`"reservations":[{"ports":[5319,5320],"note":"compose-app production stack"}]}`)}),
	})
	t.Setenv("WT_SOCKET", sock)

	code, stdout, stderr := runCLI(t, "bands", "list")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	for _, want := range []string{"compose-app", "api=4200", "proxy=4200", "5319, 5320", "compose-app production stack", "RESERVATIONS"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
}

// TestRunBandsListEmpty: an empty ledger is reported as such, not as an
// error.
func TestRunBandsListEmpty(t *testing.T) {
	sock := shortSock(t, "s")
	fakeCoordServer(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"bands.list": canned(&protocol.Response{Result: json.RawMessage(`{"bands":[],"reservations":[]}`)}),
	})
	t.Setenv("WT_SOCKET", sock)

	code, stdout, stderr := runCLI(t, "bands", "list")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "(none)") {
		t.Errorf("an empty ledger should say so:\n%s", stdout)
	}
}

// TestRunBandsReserveHost: `wt bands reserve --host --port ... --note ...`
// carries the ports and the note to the coordinator and prints the
// registration back.
func TestRunBandsReserveHost(t *testing.T) {
	sock := shortSock(t, "s")
	var got protocol.ReserveBandArgs
	fakeCoordServer(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"bands.reserve": func(req *protocol.Request) *protocol.Response {
			if err := json.Unmarshal(req.Args, &got); err != nil {
				t.Errorf("decoding the reserve request: %v", err)
			}
			return canned(&protocol.Response{Result: json.RawMessage(
				`{"host":true,"ports":[5319,5320],"note":"compose-app production stack"}`)})(req)
		},
	})
	t.Setenv("WT_SOCKET", sock)

	code, stdout, stderr := runCLI(t, "bands", "reserve", "--host",
		"--port", "5320", "--port", "5319",
		"--note", "compose-app production stack", "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	if !got.Host || got.Note != "compose-app production stack" {
		t.Errorf("request = %+v, want host with the note", got)
	}
	if len(got.Ports) != 2 || got.Ports[0] != 5320 || got.Ports[1] != 5319 {
		t.Errorf("request ports = %v, want the ports as given", got.Ports)
	}
	var res protocol.ReserveBandResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if !res.Host || res.Note != "compose-app production stack" {
		t.Errorf("reserve result = %+v", res)
	}
}

// TestRunBandsReserveHostUsage: the note is required, --host and --base are
// mutually exclusive, and --port without --host is a usage error.
func TestRunBandsReserveHostUsage(t *testing.T) {
	t.Setenv("WT_SOCKET", shortSock(t, "dead"))

	code, _, stderr := runCLI(t, "bands", "reserve", "--host", "--port", "5319")
	if code != ExitUsage {
		t.Errorf("--host without --note exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "--note") {
		t.Errorf("stderr does not name the note requirement:\n%s", stderr)
	}

	code, _, _ = runCLI(t, "bands", "reserve", "--host", "--base", "api=4200", "--note", "x")
	if code != ExitUsage {
		t.Errorf("--host with --base exit = %d, want 2", code)
	}

	code, _, _ = runCLI(t, "bands", "reserve", "--port", "5319")
	if code != ExitUsage {
		t.Errorf("--port without --host exit = %d, want 2", code)
	}

	code, _, _ = runCLI(t, "bands", "reserve")
	if code != ExitUsage {
		t.Errorf("bare reserve exit = %d, want 2", code)
	}
}

// TestRunBandsReserveAppMode: `wt bands reserve --base ...` reads the
// committed spec by walking up from cwd and sends it with the bases; the
// spans in the reply come from the coordinator's required-size computation.
func TestRunBandsReserveAppMode(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "compose-app"))
	sock := shortSock(t, "s")
	var got protocol.ReserveBandArgs
	fakeCoordServer(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"bands.reserve": func(req *protocol.Request) *protocol.Response {
			if err := json.Unmarshal(req.Args, &got); err != nil {
				t.Errorf("decoding the reserve request: %v", err)
			}
			return canned(&protocol.Response{Result: json.RawMessage(
				`{"app":"compose-app","bases":{"api":4200,"proxy":4200},"spans":{"api":32,"proxy":64}}`)})(req)
		},
	})
	t.Setenv("WT_SOCKET", sock)

	code, stdout, stderr := runCLI(t, "bands", "reserve",
		"--base", "api=4200", "--base", "proxy=4200")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	if got.Spec.App != "compose-app" {
		t.Errorf("request did not carry the committed spec: %+v", got.Spec)
	}
	if got.Bases["api"] != 4200 || got.Bases["proxy"] != 4200 {
		t.Errorf("request bases = %+v", got.Bases)
	}
	for _, want := range []string{"compose-app", "api=4200", "span 32 ports: 4200..4231", "proxy=4200", "span 64 ports: 4200..4263"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
}

// TestRunBandsReserveAppModeUsage: a base for a non-port resource and a
// missing base for a port resource are usage errors caught client-side.
func TestRunBandsReserveAppModeUsage(t *testing.T) {
	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "compose-app"))
	t.Setenv("WT_SOCKET", shortSock(t, "dead"))

	code, _, _ := runCLI(t, "bands", "reserve", "--base", "compose=4200")
	if code != ExitUsage {
		t.Errorf("base for a non-port resource exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "bands", "reserve", "--base", "api=4200")
	if code != ExitUsage {
		t.Errorf("missing base for proxy exit = %d, want 2", code)
	}
}

// TestRunBandsReserveNotAdopted: reserve needs the committed spec; outside
// a repository it reports not adopted with exit 4, never a socket error.
func TestRunBandsReserveNotAdopted(t *testing.T) {
	t.Setenv("WT_SOCKET", shortSock(t, "dead"))
	code, _, stderr := runCLI(t, "bands", "reserve", "--base", "api=4200")
	if code != ExitUnavailable {
		t.Errorf("exit = %d, want 4 (not adopted)", code)
	}
	if !strings.Contains(stderr, "wt.yaml") {
		t.Errorf("stderr does not name the missing spec:\n%s", stderr)
	}
}

// TestRunBandsReserveCoordinatorRefusalPassesThrough: a coordinator-side
// refusal carries its own exit code and remedy to the process.
func TestRunBandsReserveCoordinatorRefusalPassesThrough(t *testing.T) {
	sock := shortSock(t, "s")
	fakeCoordServer(t, sock, map[string]func(*protocol.Request) *protocol.Response{
		"bands.reserve": canned(&protocol.Response{Error: &protocol.Error{
			Code:   3,
			Msg:    "a host reservation must carry a note naming what holds the range",
			Remedy: "re-run with --note",
		}}),
	})
	t.Setenv("WT_SOCKET", sock)
	code, _, stderr := runCLI(t, "bands", "reserve", "--host", "--port", "5319", "--note", "x")
	if code != ExitRefused {
		t.Errorf("exit = %d, want 3 (refused)", code)
	}
	if !strings.Contains(stderr, "re-run with --note") {
		t.Errorf("stderr lacks the remedy:\n%s", stderr)
	}
}

// TestRunBandsUnreachableExits5: with the coordinator stopped, both bands
// verbs exit 5 naming the start command.
func TestRunBandsUnreachableExits5(t *testing.T) {
	t.Setenv("WT_SOCKET", shortSock(t, "dead"))

	code, _, stderr := runCLI(t, "bands", "list")
	if code != ExitUnreachable {
		t.Errorf("bands list exit = %d, want 5", code)
	}
	if !strings.Contains(stderr, "fix:") {
		t.Errorf("bands list stderr lacks a remedy:\n%s", stderr)
	}

	chdir(t, filepath.Join("..", "..", "testdata", "fixtures", "plain-app"))
	code, _, stderr = runCLI(t, "bands", "reserve", "--base", "api=8200")
	if code != ExitUnreachable {
		t.Errorf("bands reserve exit = %d, want 5", code)
	}
	if !strings.Contains(stderr, "wtd") && !strings.Contains(stderr, "install") {
		t.Errorf("bands reserve remedy does not name the start command:\n%s", stderr)
	}
}

// TestRunBandsVerbUsage: bands needs a subverb and rejects unknown ones,
// and help lists the verbs.
func TestRunBandsVerbUsage(t *testing.T) {
	code, _, _ := runCLI(t, "bands")
	if code != ExitUsage {
		t.Errorf("bare bands exit = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "bands", "bogus")
	if code != ExitUsage {
		t.Errorf("unknown bands verb exit = %d, want 2", code)
	}
	code, stdout, _ := runCLI(t, "help")
	if !strings.Contains(stdout, "bands list") || !strings.Contains(stdout, "bands reserve") {
		t.Errorf("help lacks the bands verbs:\n%s", stdout)
	}
	_ = code
}
