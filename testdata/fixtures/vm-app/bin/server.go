// vm-app's server: the runnable half phase 8 gives the fixture (plan.md
// §4.1: "phases 7 and 8 do the same for the other two [fixtures] as they
// need them"). It reads the worktree's descriptor and prints the
// allocation — the VM profile and the egress /22 — so a session inside
// the worktree sees exactly what its worktree was allocated, and it
// sanity-checks the egress value against the pool the spec carves from.
//
// Everything comes from the descriptor — VM_PROFILE and EGRESS_CIDR come
// from `wt show`, never from a hardcoded value — and, exactly like the
// generated reader's loud-failure row, the program refuses to run in a
// linked worktree with no descriptor, naming `wt init`. The parser below
// reads the subset of YAML the tool's own emitter writes (every string
// scalar quoted, two-space indentation); it is not a general YAML reader.
package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// allocation is what the server needs from the descriptor.
type allocation struct {
	App      string
	Slug     string
	Slot     int
	VMProfile string
	Egress   string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "vm-app-check: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	a, err := readDescriptor(filepath.Join(dir, "wt-env.yaml"))
	if err != nil {
		return err
	}

	// The cidr sanity check: the egress value must be a /22 inside
	// 172.30.0.0/16 — the pool the spec declares. This is the fixture's
	// own half of the cidr arithmetic, checked against the actual
	// allocation at runtime.
	egress, err := parseCIDR(a.Egress)
	if err != nil {
		return fmt.Errorf("the descriptor's egress %q is not a CIDR: %v", a.Egress, err)
	}
	pool, err := parseCIDR("172.30.0.0/16")
	if err != nil {
		return err
	}
	egressOnes, _ := egress.Mask.Size()
	if egressOnes != 22 || !cidrWithin(egress, pool) {
		return fmt.Errorf("the descriptor's egress %s is not a /22 inside 172.30.0.0/16 — the allocation does not match the spec", a.Egress)
	}

	fmt.Printf("vm-app worktree %s/%s (slot %d)\n", a.App, a.Slug, a.Slot)
	fmt.Printf("vm profile: %s\n", a.VMProfile)
	fmt.Printf("egress cidr: %s\n", a.Egress)
	return nil
}

// readDescriptor parses the tool-emitted descriptor YAML: top-level
// `key: value` lines and the resources block's two-level entries. A
// missing descriptor is the loud-failure row, naming wt init.
func readDescriptor(path string) (*allocation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no descriptor at %s — this linked worktree is not initialised; run 'wt init' in it", path)
		}
		return nil, err
	}
	a := &allocation{}
	resource := "" // the resource name whose indented block we are in
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		body := strings.TrimSpace(line)
		if indent == 0 {
			resource = ""
		}
		key, value, ok := strings.Cut(body, ":")
		if !ok {
			continue
		}
		// The emitter quotes every string scalar — resource names included
		// — so keys are unquoted as well as values.
		key = unquote(strings.TrimSpace(key))
		value = unquote(strings.TrimSpace(value))
		switch {
		case indent == 0 && key == "app":
			a.App = value
		case indent == 0 && key == "slug":
			a.Slug = value
		case indent == 0 && key == "slot":
			a.Slot, _ = strconv.Atoi(value)
		case indent == 2:
			resource = key
		case indent == 4 && resource == "vm" && key == "value":
			a.VMProfile = value
		case indent == 4 && resource == "egress" && key == "value":
			a.Egress = value
		}
	}
	if a.App == "" || a.Slug == "" || a.VMProfile == "" || a.Egress == "" {
		return nil, fmt.Errorf("the descriptor at %s lacks the allocation (app, slug, vm, egress); re-run 'wt init'", path)
	}
	return a, nil
}

// unquote strips the quotes the tool's emitter puts around every string
// scalar.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// parseCIDR parses an IPv4 CIDR.
func parseCIDR(s string) (*net.IPNet, error) {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return nil, err
	}
	if n.IP.To4() == nil {
		return nil, fmt.Errorf("%s is not IPv4", s)
	}
	return n, nil
}

// cidrWithin reports whether subnet lies entirely inside pool.
func cidrWithin(subnet, pool *net.IPNet) bool {
	subnetStart := ip4toUint32(subnet.IP)
	subnetBits, _ := subnet.Mask.Size()
	subnetSize := uint64(1) << (32 - subnetBits)
	poolStart := ip4toUint32(pool.IP)
	poolBits, _ := pool.Mask.Size()
	poolSize := uint64(1) << (32 - poolBits)
	return subnetStart >= poolStart && subnetStart+subnetSize <= poolStart+poolSize
}

func ip4toUint32(ip net.IP) uint64 {
	ip = ip.To4()
	return uint64(ip[0])<<24 | uint64(ip[1])<<16 | uint64(ip[2])<<8 | uint64(ip[3])
}
