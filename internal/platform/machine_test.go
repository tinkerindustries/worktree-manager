package platform

// machine_test.go pins the runner seam's pure parsing — the halves of the
// colima and wsl listings that turn bytes into instances. The live
// shell-outs cannot run on this Linux implementation machine (no colima,
// no wsl.exe) and are reported not_run; the driver's rails are proved
// against a fake runner in internal/driver/machine_test.go.

import (
	"errors"
	"testing"
)

func TestParseColimaList(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		instances, err := parseColimaList([]byte("[]"))
		if err != nil || len(instances) != 0 {
			t.Fatalf("parseColimaList([]) = %v, %v", instances, err)
		}
	})

	t.Run("no profiles writes nothing", func(t *testing.T) {
		instances, err := parseColimaList([]byte("\n"))
		if err != nil || len(instances) != 0 {
			t.Fatalf("parseColimaList(blank) = %v, %v", instances, err)
		}
	})

	t.Run("one object per line, the shape colima writes", func(t *testing.T) {
		// Verbatim from `colima list --json` on macOS: a JSON stream, one
		// object per line, with no enclosing array. The array-only tests
		// below passed for the whole time the driver could not list a
		// single profile.
		out := `{"name":"arc","status":"Stopped","arch":"aarch64","cpus":4,"memory":8589934592,"disk":64424509440}
{"name":"vm-app-wt-1-1","status":"Running","arch":"aarch64","cpus":2,"memory":4294967296,"disk":64424509440}
`
		instances, err := parseColimaList([]byte(out))
		if err != nil {
			t.Fatalf("parseColimaList: %v", err)
		}
		if len(instances) != 2 {
			t.Fatalf("instances = %v, want 2", instances)
		}
		if instances[0].Name != "arc" || instances[0].Running {
			t.Errorf("arc = %+v, want stopped", instances[0])
		}
		if instances[1].Name != "vm-app-wt-1-1" || !instances[1].Running {
			t.Errorf("vm-app-wt-1-1 = %+v, want running", instances[1])
		}
	})

	t.Run("a single object is a stream of one", func(t *testing.T) {
		instances, err := parseColimaList([]byte(`{"name":"default","status":"Running"}`))
		if err != nil {
			t.Fatalf("parseColimaList: %v", err)
		}
		if len(instances) != 1 || instances[0].Name != "default" || !instances[0].Running {
			t.Fatalf("instances = %+v, want one running default", instances)
		}
	})

	t.Run("running and stopped profiles", func(t *testing.T) {
		out := `[{"name":"default","status":"Running"},{"name":"vm-app-wt-1-1","status":"Running"},{"name":"vm-app-wt-2-2","status":"Stopped"}]`
		instances, err := parseColimaList([]byte(out))
		if err != nil {
			t.Fatalf("parseColimaList: %v", err)
		}
		if len(instances) != 3 {
			t.Fatalf("instances = %v, want 3", instances)
		}
		if instances[0].Name != "default" || !instances[0].Running {
			t.Errorf("default = %+v, want running", instances[0])
		}
		if instances[2].Name != "vm-app-wt-2-2" || instances[2].Running {
			t.Errorf("stopped profile = %+v, want not running", instances[2])
		}
	})

	t.Run("unparseable output is an error", func(t *testing.T) {
		if _, err := parseColimaList([]byte("definitely not json")); err == nil {
			t.Fatal("unparseable output must be an error, never an empty count")
		}
	})
}

func TestParseWSLNames(t *testing.T) {
	t.Run("quiet listing with the marker column", func(t *testing.T) {
		out := "* Ubuntu\n  Debian\n\n  vm-app-wt-1-1\n"
		names := parseWSLNames(out)
		want := []string{"Ubuntu", "Debian", "vm-app-wt-1-1"}
		if len(names) != len(want) {
			t.Fatalf("names = %v, want %v", names, want)
		}
		for i := range want {
			if names[i] != want[i] {
				t.Errorf("names[%d] = %q, want %q", i, names[i], want[i])
			}
		}
	})

	t.Run("running listing", func(t *testing.T) {
		names := parseWSLNames("Ubuntu\nvm-app-wt-1-1\n")
		if len(names) != 2 || names[0] != "Ubuntu" || names[1] != "vm-app-wt-1-1" {
			t.Errorf("names = %v", names)
		}
	})
}

func TestMachineUnavailableSentinel(t *testing.T) {
	// The platform's no-runner error is the marker the driver maps to
	// unavailable; it must survive wrapping.
	err := machineUnavailable("this platform has no VM runner")
	if !errors.Is(err, ErrMachineUnavailable) {
		t.Fatalf("machineUnavailable must wrap ErrMachineUnavailable: %v", err)
	}
}
