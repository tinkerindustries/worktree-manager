//go:build windows

package platform

// task_windows.go is the Windows half of the supervisor seam: the
// registration of the coordinator with the Task Scheduler as a logon
// scheduled task, answering plan.md's R2 ("Windows service under the user
// account, or a logon task", phase 8).
//
// The logon task is the default, and the choice is reasoned. The
// alternative — a service configured to run as the user account — buys
// the service control manager's restart handling, and costs three things
// this project is not willing to pay for a per-user tool: an elevated
// install (sc create needs an administrator, and a tool that suddenly
// demands elevation at install is where its security story gets worse,
// not better), session-0 reachability workarounds (a conventional
// service runs in session 0, isolated from the user's desktop session,
// where reaching Docker Desktop's user-context pipe and a per-user WSL2
// distro is exactly the awkward case docs/ARCHITECTURE.md §4.1 calls
// out), and stored credentials (a service logging on as the user breaks
// when the password changes). The logon task runs in the user's
// interactive session, where every resource the coordinator touches
// lives; it installs without elevation; and the restart handling it
// gives up is partially replaced by the task's RestartOnFailure setting,
// with the remaining gap stated: a coordinator that dies between the
// task's restart attempts stays dead until the user's next wt invocation
// exits 5 naming the start command.
//
// Registration is the task XML, written UTF-16 (schtasks /Create /XML
// requires it) under %LOCALAPPDATA%\wt and handed to schtasks; the XML
// file doubles as the "registered" observation `daemon status` checks,
// exactly as the plist does on macOS.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// windowsTaskDir resolves the directory holding the task XML:
// %LOCALAPPDATA%\wt, or the test prefix.
func windowsTaskDir(prefix string) (string, error) {
	if prefix != "" {
		return prefix, nil
	}
	if appData := os.Getenv("LOCALAPPDATA"); appData != "" {
		return filepath.Join(appData, "wt"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving the task registration directory: %w", err)
	}
	return filepath.Join(home, "AppData", "Local", "wt"), nil
}

// windowsTaskXML is the scheduled-task document: a LogonTrigger starts
// the coordinator at logon in the user's interactive session, the Exec
// action runs wtd in the foreground, and RestartOnFailure restarts the
// task (and with it wtd) when it crashes — the partial replacement for
// the service control manager's recovery that the logon-task choice
// accepts. With the opt-in loopback TCP surface configured, the action's
// Arguments carry --tcp and --tcp-token; the token is validated to
// contain no whitespace, which is what keeps it one argument in the
// task's command line.
func windowsTaskXML(wtdPath, tcpAddr, tcpToken string) []byte {
	args := ""
	if tcpAddr != "" {
		args = fmt.Sprintf("<Arguments>--tcp %s --tcp-token %s</Arguments>", xmlEscape(tcpAddr), xmlEscape(tcpToken))
	}
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Worktree Manager coordinator (wtd)</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      %s
    </Exec>
  </Actions>
</Task>
`, xmlEscape(wtdPath), args)
	return encodeUTF16LE(xml)
}

// encodeUTF16LE encodes s as UTF-16LE with a byte-order mark — the
// encoding schtasks /Create /XML requires.
func encodeUTF16LE(s string) []byte {
	u := utf16.Encode([]rune(string(rune(0xFEFF)) + s))
	buf := make([]byte, 0, len(u)*2)
	for _, r := range u {
		buf = append(buf, byte(r), byte(r>>8))
	}
	return buf
}

// schtasks runs one schtasks invocation.
func schtasks(args ...string) error {
	cmd := exec.Command("schtasks", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// installWindowsTask writes the task XML and, outside a test prefix,
// registers and starts the task: schtasks /Create overwrites or creates
// the task from the XML, /Run starts it now (the mirror of launchctl
// kickstart). No elevation: a task in the user's own context registers
// without one — one of the two reasons the logon task is the default.
func installWindowsTask(prefix, wtdPath, tcpAddr, tcpToken string) (InstallSupervisorResult, error) {
	dir, err := windowsTaskDir(prefix)
	if err != nil {
		return InstallSupervisorResult{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return InstallSupervisorResult{}, fmt.Errorf("creating the task registration directory %s: %w", dir, err)
	}
	xmlPath := filepath.Join(dir, WindowsTaskFilename)
	if err := os.WriteFile(xmlPath, windowsTaskXML(wtdPath, tcpAddr, tcpToken), 0o600); err != nil {
		return InstallSupervisorResult{}, fmt.Errorf("writing %s: %w", xmlPath, err)
	}
	if prefix != "" {
		note := "registration written under a test prefix (" + xmlPath + "); no scheduled task was registered"
		if tcpToken != "" {
			note += "; the task XML carries the loopback TCP token"
		}
		return InstallSupervisorResult{
			RegistrationPath: xmlPath, Label: WindowsTaskName, Loaded: false,
			Note: note,
		}, nil
	}
	if err := schtasks("/Create", "/F", "/TN", WindowsTaskName, "/XML", xmlPath); err != nil {
		return InstallSupervisorResult{RegistrationPath: xmlPath, Label: WindowsTaskName},
			fmt.Errorf("registering the coordinator's logon task: %w", err)
	}
	if err := schtasks("/Run", "/TN", WindowsTaskName); err != nil {
		return InstallSupervisorResult{RegistrationPath: xmlPath, Label: WindowsTaskName},
			fmt.Errorf("starting the coordinator's logon task: %w", err)
	}
	return InstallSupervisorResult{
		RegistrationPath: xmlPath, Label: WindowsTaskName, Loaded: true,
		Note: "registered as a logon scheduled task (runs in your interactive session, where Docker Desktop and WSL2 live); the service-under-the-user-account alternative would buy the SCM's restart handling at the cost of an elevated install and session-0 reachability workarounds",
	}, nil
}

// taskSchedulerRunning asks the Task Scheduler whether the task is
// running — the supervisor fact `daemon status` needs to distinguish
// "registered but stopped" from "running but unreachable". A test prefix
// never registered anything, so the answer is false without consulting
// the scheduler.
//
// Bounded coverage, stated: schtasks's status line is localized, so on a
// non-English system a running task may be reported as stopped. The
// remedy `daemon status` then offers (schtasks /Run) is a no-op under
// MultipleInstancesPolicy=IgnoreNew, so the misreport degrades safely.
func taskSchedulerRunning(prefix string) (bool, error) {
	if prefix != "" {
		return false, nil
	}
	out, err := exec.Command("schtasks", "/Query", "/TN", WindowsTaskName, "/FO", "LIST").Output()
	if err != nil {
		// A task that does not exist makes schtasks exit non-zero: not
		// running. Any other failure is treated the same way (false), the
		// launchd precedent — the state machine's fixes are safe under a
		// misreport.
		return false, nil
	}
	return parseTaskStatus(string(out)), nil
}

// parseTaskStatus extracts the running state from schtasks /Query
// /FO LIST output: the Status line's value. "Running" reports running;
// anything else (Ready, Disabled, Unknown, a localized value) reports
// not running — the safe side of the misreport.
func parseTaskStatus(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.LastIndex(line, ":"); i >= 0 {
			value := strings.TrimSpace(line[i+1:])
			if strings.EqualFold(value, "Running") {
				return true
			}
		}
	}
	return false
}

// errNoTaskScheduler is the non-Windows stub's refusal; unreachable in
// this file.
var errNoTaskScheduler = errors.New("no Task Scheduler on this platform")
