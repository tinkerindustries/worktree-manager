package coord

// drift_test.go pins exit criterion 3: moving a band and re-running
// doctor reports the generated file and the field that moved. The test
// builds a real repository with the committed spec and the generated
// artefacts, runs doctor clean, moves the band in the ledger, and expects
// the finding naming the file and the field.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/artefact"
	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// TestDoctorReportsMovedBandNamesFileAndField is exit criterion 3: moving
// a band and re-running doctor reports the generated file and the field
// that moved.
func TestDoctorReportsMovedBandNamesFileAndField(t *testing.T) {
	h, main, wt := driftRepo(t)

	// The band at 8200, the worktree initialised.
	sp := driftSpec(t, main)
	ctx := context.Background()
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	if r := h.Request(ctx, sess, verbBandsReserve, &api.ReserveBandArgs{Spec: *sp, Bases: map[string]int{"api": 8200}}); r.Error != nil {
		t.Fatalf("bands.reserve refused: %+v", r.Error)
	}
	allocateEntry(t, h, sess, ctx, sp, wt)

	// Doctor is clean: the artefacts match the spec and the band.
	if findings := runDoctorFindings(t, h, sess, ctx); len(findings) != 0 {
		t.Fatalf("doctor before the band move reported findings:\n%s", docFindings(findings))
	}

	// The band moves: api from 8200 to 8300.
	if r := h.Request(ctx, sess, verbBandsReserve, &api.ReserveBandArgs{Spec: *sp, Bases: map[string]int{"api": 8300}}); r.Error != nil {
		t.Fatalf("re-registering the band refused: %+v", r.Error)
	}

	// Doctor reports the generated file and the field that moved.
	findings := runDoctorFindings(t, h, sess, ctx)
	matched := false
	for _, f := range findings {
		if f.Level != "warning" {
			continue
		}
		if strings.Contains(f.Message, "docs/wt.md") && strings.Contains(f.Message, "band api") &&
			strings.Contains(f.Message, "8200") && strings.Contains(f.Message, "8300") {
			matched = true
		}
		if f.Remedy == "" {
			t.Errorf("a drift finding has no remedy: %+v", f)
		}
	}
	if !matched {
		t.Errorf("no finding names the generated file and the moved field:\n%s", docFindings(findings))
	}
}

// TestDoctorReportsRenamedResource: renaming a resource in the spec makes
// doctor report the generated file and the resources field.
func TestDoctorReportsRenamedResource(t *testing.T) {
	h, main, wt := driftRepo(t)
	sp := driftSpec(t, main)
	ctx := context.Background()
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	if r := h.Request(ctx, sess, verbBandsReserve, &api.ReserveBandArgs{Spec: *sp, Bases: map[string]int{"api": 8200}}); r.Error != nil {
		t.Fatalf("bands.reserve refused: %+v", r.Error)
	}
	allocateEntry(t, h, sess, ctx, sp, wt)

	// The spec is rewritten with the port resource renamed api → api2, and
	// committed (the committed spec is the adoption signal; doctor reads
	// it by walking up from the main checkout).
	renamed := *sp
	renamed.Resources = []spec.Resource{{Type: "port", Name: "api2"}}
	data, rerr := spec.EmitYAML(&renamed)
	if rerr != nil {
		t.Fatalf("emitting the renamed spec: %v", rerr)
	}
	if err := os.WriteFile(filepath.Join(main, "wt.yaml"), data, 0o644); err != nil {
		t.Fatalf("writing the renamed spec: %v", err)
	}
	gitT(t, main, "add", ".")
	gitT(t, main, "commit", "-m", "rename api to api2")
	// The band now follows the new name; the generated file still records
	// the old one.
	if r := h.Request(ctx, sess, verbBandsReserve, &api.ReserveBandArgs{Spec: renamed, Bases: map[string]int{"api2": 8200}}); r.Error != nil {
		t.Fatalf("re-registering the renamed band refused: %+v", r.Error)
	}

	findings := runDoctorFindings(t, h, sess, ctx)
	matched := false
	for _, f := range findings {
		if strings.Contains(f.Message, "resources") && strings.Contains(f.Message, "api") && strings.Contains(f.Message, "api2") {
			matched = true
		}
	}
	if !matched {
		t.Errorf("no finding names the generated file and the renamed resources field:\n%s", docFindings(findings))
	}
}

// allocateEntry allocates and writes the descriptor for the drift
// worktree, the way init's steps 2 and 4 do, so doctor sees a fully
// initialised entry.
func allocateEntry(t *testing.T, h *Harness, sess *Session, ctx context.Context, sp *spec.Spec, wt string) {
	t.Helper()
	if r := h.Request(ctx, sess, verbAllocate, &api.AllocateArgs{
		Spec: *sp, Slug: "brisk-otter", Path: wt,
		DescriptorPath: filepath.Join(wt, "wt-env.json"), Description: "the drift test's worktree",
	}); r.Error != nil {
		t.Fatalf("allocate refused: %+v", r.Error)
	}
	d := &descriptor.Descriptor{
		Version: 1, App: sp.App, Slug: "brisk-otter", Slot: 1, Path: wt,
		Resources: map[string]spec.Resolved{"api": {Type: "port", Value: 8201}},
		Extras:    map[string]any{},
	}
	if err := descriptor.Write(filepath.Join(wt, "wt-env.json"), "json", d); err != nil {
		t.Fatalf("writing the descriptor: %v", err)
	}
}

// driftRepo builds the drift fixture: a repository with the spec and the
// generated artefacts committed, one linked worktree, and a harness. The
// band is registered by the caller at the base the artefacts record.
func driftRepo(t *testing.T) (*Harness, string, string) {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "main")
	gitT(t, "", "init", "-b", "main", main)
	gitT(t, main, "config", "user.email", "t@example.com")
	gitT(t, main, "config", "user.name", "T")
	gitT(t, main, "commit", "--allow-empty", "-m", "fixture")

	sp := driftSpec(t, main)
	files, err := artefact.Render(sp, map[string]int{"api": 8200}, artefact.Options{GuardHook: false})
	if err != nil {
		t.Fatalf("rendering the artefacts: %v", err)
	}
	if err := artefact.Apply(main, files); err != nil {
		t.Fatalf("applying the artefacts: %v", err)
	}
	gitT(t, main, "add", ".")
	gitT(t, main, "commit", "-m", "adopt: spec and generated artefacts")

	wt := filepath.Join(base, "brisk-otter")
	gitT(t, main, "worktree", "add", "-b", "brisk-otter", wt, "main")

	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	h.H.InstallDrivers(driver.NewRegistry(&driver.Port{}))
	return h, main, wt
}

// driftSpec is the drift fixture's spec: one stride port plus the minimal
// emit section.
func driftSpec(t *testing.T, main string) *spec.Spec {
	t.Helper()
	max := 32
	sp := &spec.Spec{
		Version:   1,
		App:       "plain-app",
		Slots:     spec.Slots{Max: &max},
		Resources: []spec.Resource{{Type: "port", Name: "api"}},
		Reaper:    spec.Reaper{Binaries: []string{"plain-app-server"}},
		Emit:      spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.json", Format: "json"}},
	}
	if err := spec.Validate(sp); err != nil {
		t.Fatalf("the drift spec does not validate: %v", err)
	}
	data, err := spec.EmitYAML(sp)
	if err != nil {
		t.Fatalf("emitting the drift spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(main, "wt.yaml"), data, 0o644); err != nil {
		t.Fatalf("writing the drift spec: %v", err)
	}
	return sp
}

// runDoctorFindings runs doctor and returns its findings.
func runDoctorFindings(t *testing.T, h *Harness, sess *Session, ctx context.Context) []api.DoctorFinding {
	t.Helper()
	resp := h.Request(ctx, sess, verbDoctor, nil)
	if resp.Error != nil {
		t.Fatalf("doctor refused: %+v", resp.Error)
	}
	var res api.DoctorResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decoding doctor: %v", err)
	}
	return res.Findings
}

// docFindings renders findings for a failure message.
func docFindings(findings []api.DoctorFinding) string {
	data, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return strings.TrimSpace(strings.TrimSpace(fmt.Sprint(findings)))
	}
	return string(data)
}

// gitT runs one git command.
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
}
