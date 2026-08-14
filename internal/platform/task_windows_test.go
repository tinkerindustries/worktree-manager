//go:build windows

package platform

// task_windows_test.go is what a hosted windows-latest runner can prove
// about the logon-task registration: the XML document's shape, the UTF-16
// encoding schtasks requires, the status-line parser, and the prefix rail
// (no test may register a real scheduled task on the machine running it).
// The registration itself — schtasks /Create against a real Task
// Scheduler — is interactive-desktop work and is reported not_run.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// TestWindowsTaskXMLShape pins the document's decisions: a LogonTrigger in
// the interactive session, the coordinator binary as the Exec action, and
// RestartOnFailure — the partial replacement for the SCM's restart
// handling that the logon-task choice accepts. With the opt-in loopback
// TCP surface configured, the Arguments carry --tcp and --tcp-token.
func TestWindowsTaskXMLShape(t *testing.T) {
	wtd := `C:\Program Files\wt\wtd.exe`
	raw := windowsTaskXML(wtd, "", "", nil)
	if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xFE {
		t.Error("task XML lacks the UTF-16LE byte-order mark")
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	if len(u) > 0 && u[0] == 0xFEFF {
		u = u[1:]
	}
	xml := string(utf16.Decode(u))
	for _, want := range []string{
		`encoding="UTF-16"`,
		"<LogonTrigger>",
		"<LogonType>InteractiveToken</LogonType>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<Command>" + wtd + "</Command>",
		"<RestartOnFailure>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("task XML lacks %s:\n%s", want, xml)
		}
	}
	if strings.Contains(xml, "<Arguments>") {
		t.Error("the plain registration must not carry Arguments")
	}

	// The TCP variant carries the surface's configuration in the action's
	// Arguments — the token is validated whitespace-free so it stays one
	// argument in the task's command line.
	raw2 := windowsTaskXML(wtd, "127.0.0.1:7331", "tcp-token-0123456789abcdef", nil)
	u2 := make([]uint16, len(raw2)/2)
	for i := range u2 {
		u2[i] = uint16(raw2[2*i]) | uint16(raw2[2*i+1])<<8
	}
	if len(u2) > 0 && u2[0] == 0xFEFF {
		u2 = u2[1:]
	}
	xml2 := string(utf16.Decode(u2))
	for _, want := range []string{
		"--tcp 127.0.0.1:7331",
		"--tcp-token tcp-token-0123456789abcdef",
	} {
		if !strings.Contains(xml2, want) {
			t.Errorf("the TCP task XML lacks %q:\n%s", want, xml2)
		}
	}
}

// TestParseTaskStatus covers the /FO LIST status values: Running reports
// running; Ready, Disabled and unknown values report not running — the
// safe side of the localized-status misreport.
func TestParseTaskStatus(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"TaskName: com.mrgeoffrich.wtd\nStatus: Running\n", true},
		{"TaskName: com.mrgeoffrich.wtd\nStatus: Ready\n", false},
		{"TaskName: com.mrgeoffrich.wtd\nStatus: Disabled\n", false},
		{"TaskName: com.mrgeoffrich.wtd\nNext Run Time: N/A\n", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := parseTaskStatus(tc.out); got != tc.want {
			t.Errorf("parseTaskStatus(%q) = %v, want %v", tc.out, got, tc.want)
		}
	}
}

// TestTaskSchedulerRunningUnderPrefix: a test prefix never registered
// anything, so the scheduler is never consulted.
func TestTaskSchedulerRunningUnderPrefix(t *testing.T) {
	running, err := taskSchedulerRunning(t.TempDir())
	if err != nil {
		t.Fatalf("taskSchedulerRunning(prefix): %v", err)
	}
	if running {
		t.Error("a prefixed registration reports running; a test prefix never registers a task")
	}
}

// TestInstallWindowsTaskPrefix: install under a prefix writes the XML and
// registers nothing — the no-test-registers-a-real-task rail.
func TestInstallWindowsTaskPrefix(t *testing.T) {
	prefix := t.TempDir()
	wtd := filepath.Join(prefix, "wtd.exe")
	if err := os.WriteFile(wtd, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := InstallSupervisor(InstallSupervisorOpts{Prefix: prefix, WtdPath: wtd})
	if err != nil {
		t.Fatalf("InstallSupervisor: %v", err)
	}
	if res.Loaded {
		t.Error("a prefixed install reported loaded; it must never register a task")
	}
	if res.RegistrationPath != filepath.Join(prefix, WindowsTaskFilename) {
		t.Errorf("registration path = %q", res.RegistrationPath)
	}
	data, err := os.ReadFile(res.RegistrationPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xFE {
		t.Error("the written XML is not UTF-16LE")
	}
}
