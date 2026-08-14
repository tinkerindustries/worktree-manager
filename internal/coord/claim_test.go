package coord

// claim_test.go proves the lock rule of claim.go: the mutex covers the
// store, a claim covers the entry, and a slow driver does not queue every
// other client behind it.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// blockingDriver holds its teardown open until the test releases it, so a
// test can ask what the coordinator will do for another client meanwhile.
type blockingDriver struct {
	entered chan struct{}
	release chan struct{}
}

func (d *blockingDriver) Type() string          { return "namespace" }
func (d *blockingDriver) HasApply() bool        { return false }
func (d *blockingDriver) HasTeardown() bool     { return true }
func (d *blockingDriver) GatesAllocation() bool { return false }

func (d *blockingDriver) Derive(r *spec.Resource, s *spec.Spec, ctx spec.Context) (any, error) {
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		return nil, err
	}
	return table[r.Name].Value, nil
}

func (d *blockingDriver) Probe(*spec.Resource, any, driver.Env) driver.ProbeResult {
	return driver.ProbeFree
}
func (d *blockingDriver) Apply(*spec.Resource, any, driver.Env) (driver.ApplyResult, error) {
	return driver.ApplyResult{}, nil
}
func (d *blockingDriver) Teardown(*spec.Resource, any, driver.Env) error {
	close(d.entered)
	<-d.release
	return nil
}
func (d *blockingDriver) Verify(*spec.Resource, any, driver.Env) ([]driver.Finding, error) {
	return nil, nil
}
func (d *blockingDriver) BlastRadius(*spec.Resource, *spec.Spec) string { return "prose" }

// TestSlowDriverDoesNotBlockOtherClients is N1: the whole premise is
// several worktrees side by side, which means several clients. A teardown
// that takes as long as docker takes must not queue another client's list
// behind it.
func TestSlowDriverDoesNotBlockOtherClients(t *testing.T) {
	blocker := &blockingDriver{entered: make(chan struct{}), release: make(chan struct{})}
	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(blocker))
	reader, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("second hello refused: %+v", err)
	}

	done := make(chan *api.Response, 1)
	go func() { done <- h.H.Teardown(sess, ref, sp, nil) }()

	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the teardown never reached the driver")
	}

	// The driver is still working. Another client's read must be served.
	served := make(chan *api.Response, 1)
	go func() { served <- h.Request(context.Background(), reader, verbList, &api.ListArgs{}) }()
	select {
	case resp := <-served:
		if resp.Error != nil {
			t.Fatalf("list refused while a teardown was running: %+v", resp.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("list blocked behind a running teardown; the mutex is held across driver work")
	}

	close(blocker.release)
	if resp := <-done; resp.Error != nil {
		t.Fatalf("teardown failed: %+v", resp.Error)
	}
}

// TestSecondOperationOnAClaimedEntryIsRefused: two teardowns of one entry
// race on the same objects, so the second is refused rather than queued.
func TestSecondOperationOnAClaimedEntryIsRefused(t *testing.T) {
	blocker := &blockingDriver{entered: make(chan struct{}), release: make(chan struct{})}
	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(blocker))

	done := make(chan *api.Response, 1)
	go func() { done <- h.H.Teardown(sess, ref, sp, nil) }()
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the teardown never reached the driver")
	}

	second := make(chan *api.Response, 1)
	go func() { second <- h.H.Teardown(sess, ref, sp, nil) }()
	select {
	case resp := <-second:
		if resp.Error == nil {
			t.Fatal("a second teardown of the same entry was allowed to run alongside the first")
		}
		if resp.Error.Remedy == "" {
			t.Error("the refusal carries no remedy")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second teardown queued behind the first instead of being refused")
	}

	close(blocker.release)
	if resp := <-done; resp.Error != nil {
		t.Fatalf("teardown failed: %+v", resp.Error)
	}
}

// TestClaimIsReleasedForTheNextOperation: the claim covers one operation,
// not the entry's life, so a re-run after a failed teardown still works.
func TestClaimIsReleasedForTheNextOperation(t *testing.T) {
	stub := &stubDriver{}
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	h.H.InstallDrivers(driver.NewRegistry(stub))
	sp := teardownSpec(t)
	res, perr := allocate(t, h, sess, sp, "wt-1")
	if perr != nil {
		t.Fatalf("allocation refused: %+v", perr)
	}
	ref := api.EntryRef{App: res.App, Slug: res.Slug}

	if resp := h.H.Teardown(sess, ref, sp, nil); resp.Error != nil {
		t.Fatalf("teardown refused: %+v", resp.Error)
	}
	// The entry is gone, so the second call is the no-entry refusal — not
	// the claim refusal, which would mean the claim outlived its operation.
	resp := h.H.Teardown(sess, ref, sp, nil)
	if resp.Error == nil {
		t.Fatal("a teardown of a dropped entry succeeded")
	}
	if got := resp.Error.Msg; got == "" || strings.Contains(got, "already running") {
		t.Errorf("error = %q, want the no-entry refusal; the claim was not released", got)
	}
}
