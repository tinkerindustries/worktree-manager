package coord

// onboard_test.go exercises the phase-7 primitives: ports.scan (what is
// listening, reported as facts) and bands.suggest (where an app's band
// could sit, constrained by the ledger). Neither classifies anything — the
// assertions here pin the facts and the constraints, not any judgement
// about what the listeners or the ranges mean.

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// TestPortsScanReportsFacts runs the scan against a real listener this
// process opened, and asserts the scan sees it as a fact: the port, the
// pid, and a command name. The listener is the test's own, torn down
// before the test returns.
func TestPortsScanReportsFacts(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	ctx := context.Background()
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}

	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatalf("opening a test listener: %v", lerr)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	resp := h.Request(ctx, sess, verbPortsScan, nil)
	if resp.Error != nil {
		t.Fatalf("ports.scan refused: %+v", resp.Error)
	}
	var res api.PortsScanResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decoding the scan: %v", err)
	}
	found := false
	for _, l := range res.Listeners {
		if l.Port == port {
			found = true
			if l.PID <= 0 {
				t.Errorf("listener on %d has pid %d, want a real pid", port, l.PID)
			}
			if l.Command == "" {
				t.Errorf("listener on %d has no command name", port)
			}
		}
	}
	if !found {
		t.Errorf("the scan did not report the test's own listener on %d; listeners: %+v", port, res.Listeners)
	}
	// Sorted by port: the report is stable.
	for i := 1; i < len(res.Listeners); i++ {
		if res.Listeners[i-1].Port > res.Listeners[i].Port {
			t.Errorf("the scan is not sorted by port: %d after %d",
				res.Listeners[i].Port, res.Listeners[i-1].Port)
		}
	}
}

// TestBandsSuggestFindsLowestFreeBase: with an empty ledger, suggest
// proposes the lowest base whose range fits, sized by the coordinator's
// required-size computation.
func TestBandsSuggestFindsLowestFreeBase(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	ctx := context.Background()
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}

	sp := plainSuggestSpec(t, 8, []spec.Resource{{Type: "port", Name: "api"}})
	resp := h.Request(ctx, sess, verbBandsSuggest, &api.SuggestBandArgs{Spec: *sp})
	if resp.Error != nil {
		t.Fatalf("bands.suggest refused: %+v", resp.Error)
	}
	var res api.SuggestBandResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decoding the suggestion: %v", err)
	}
	if len(res.Suggestions) != 1 {
		t.Fatalf("suggestions = %+v, want one", res.Suggestions)
	}
	s := res.Suggestions[0]
	if s.Resource != "api" || s.Base != 1 || s.Span != 8 || s.Low != 1 || s.High != 8 {
		t.Errorf("suggestion = %+v, want api at base 1, span 8, range 1..8", s)
	}
}

// TestBandsSuggestSkipsBandsAndReservations: a registered band and a host
// reservation make the suggestion jump past both — the constraint is the
// ledger, and the suggestion is the lowest base that fits.
func TestBandsSuggestSkipsBandsAndReservations(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	ctx := context.Background()
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}

	// An existing app's band at 100..115 (span 16) and a host reservation
	// at 200.
	other := plainSuggestSpec(t, 16, []spec.Resource{{Type: "port", Name: "web"}})
	reserve := h.Request(ctx, sess, verbBandsReserve, &api.ReserveBandArgs{
		Spec: *other, Bases: map[string]int{"web": 100},
	})
	if reserve.Error != nil {
		t.Fatalf("bands.reserve refused: %+v", reserve.Error)
	}
	host := h.Request(ctx, sess, verbBandsReserve, &api.ReserveBandArgs{
		Host: true, Ports: []int{200}, Note: "the test's production stack",
	})
	if host.Error != nil {
		t.Fatalf("host reserve refused: %+v", host.Error)
	}

	sp := plainSuggestSpec(t, 8, []spec.Resource{{Type: "port", Name: "api"}})
	resp := h.Request(ctx, sess, verbBandsSuggest, &api.SuggestBandArgs{Spec: *sp})
	if resp.Error != nil {
		t.Fatalf("bands.suggest refused: %+v", resp.Error)
	}
	var res api.SuggestBandResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decoding the suggestion: %v", err)
	}
	if len(res.Suggestions) != 1 {
		t.Fatalf("suggestions = %+v, want one", res.Suggestions)
	}
	// The lowest free range of 8 consecutive ports: 1..8 is free.
	if res.Suggestions[0].Base != 1 {
		t.Errorf("base = %d, want 1 (the band at 100 and the reservation at 200 are both free ranges)", res.Suggestions[0].Base)
	}

	// Now the low ranges are busy: a band occupying 1..16 forces the
	// suggestion higher.
	first := plainSuggestSpec(t, 16, []spec.Resource{{Type: "port", Name: "other"}})
	if r := h.Request(ctx, sess, verbBandsReserve, &api.ReserveBandArgs{
		Spec: *first, Bases: map[string]int{"other": 1},
	}); r.Error != nil {
		t.Fatalf("bands.reserve refused: %+v", r.Error)
	}
	resp = h.Request(ctx, sess, verbBandsSuggest, &api.SuggestBandArgs{Spec: *sp})
	if resp.Error != nil {
		t.Fatalf("bands.suggest refused: %+v", resp.Error)
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decoding the suggestion: %v", err)
	}
	if res.Suggestions[0].Base != 17 {
		t.Errorf("base = %d, want 17 (1..16 is occupied by the other app's band)", res.Suggestions[0].Base)
	}
}

// TestBandsSuggestGroupSharesOneBase: the group form's resources derive
// interleaved ports from one base, so suggest proposes the same base for
// both — the fixed-relationship case one base must serve (M2 §6.1).
func TestBandsSuggestGroupSharesOneBase(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	ctx := context.Background()
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}

	sp := plainSuggestSpec(t, 8, []spec.Resource{
		{Type: "port", Name: "proxy", Form: sptr("group"), Size: iptr(2), Offset: iptr(0)},
		{Type: "port", Name: "api", Form: sptr("group"), Size: iptr(2), Offset: iptr(1)},
	})
	resp := h.Request(ctx, sess, verbBandsSuggest, &api.SuggestBandArgs{Spec: *sp})
	if resp.Error != nil {
		t.Fatalf("bands.suggest refused: %+v", resp.Error)
	}
	var res api.SuggestBandResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decoding the suggestion: %v", err)
	}
	if len(res.Suggestions) != 2 {
		t.Fatalf("suggestions = %+v, want two", res.Suggestions)
	}
	if res.Suggestions[0].Base != res.Suggestions[1].Base {
		t.Errorf("bases = %d and %d, want one shared base (the group form)", res.Suggestions[0].Base, res.Suggestions[1].Base)
	}
	if res.Suggestions[0].Base != 1 || res.Suggestions[0].Span != 16 {
		t.Errorf("suggestion = %+v, want base 1 span 16", res.Suggestions[0])
	}
}

// TestBandsSuggestIndependentStridesGetDisjointBases: two independent
// stride resources cannot share a base (identical port sets would collide
// at allocation), so suggest keeps them on disjoint ranges.
func TestBandsSuggestIndependentStridesGetDisjointBases(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	ctx := context.Background()
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}

	sp := plainSuggestSpec(t, 8, []spec.Resource{
		{Type: "port", Name: "api"},
		{Type: "port", Name: "web"},
	})
	resp := h.Request(ctx, sess, verbBandsSuggest, &api.SuggestBandArgs{Spec: *sp})
	if resp.Error != nil {
		t.Fatalf("bands.suggest refused: %+v", resp.Error)
	}
	var res api.SuggestBandResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decoding the suggestion: %v", err)
	}
	if len(res.Suggestions) != 2 {
		t.Fatalf("suggestions = %+v, want two", res.Suggestions)
	}
	if res.Suggestions[0].Base == res.Suggestions[1].Base {
		t.Errorf("bases = %d and %d; independent strides must not share a base", res.Suggestions[0].Base, res.Suggestions[1].Base)
	}
	if res.Suggestions[0].Base != 1 || res.Suggestions[1].Base != 9 {
		t.Errorf("bases = %d and %d, want 1 and 9 (adjacent disjoint ranges)", res.Suggestions[0].Base, res.Suggestions[1].Base)
	}
}

// TestBandsSuggestRefusesInvalidSpec: a spec that does not validate is
// refused whole, like every verb that consumes one.
func TestBandsSuggestRefusesInvalidSpec(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	ctx := context.Background()
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	// A port resource carrying a cidr-only field is refused whole by
	// validation (a field that is not valid for the declared type).
	bad := &spec.Spec{
		Version: 1, App: "plain-app",
		Resources: []spec.Resource{
			{Type: "port", Name: "api", Pool: sptr("172.30.0.0/16")},
		},
	}
	resp := h.Request(ctx, sess, verbBandsSuggest, &api.SuggestBandArgs{Spec: *bad})
	if resp.Error == nil || resp.Error.Code != 3 {
		t.Fatalf("refusal = %+v, want exit 3 (refused whole)", resp.Error)
	}
}

// plainSuggestSpec is a minimal spec with the given slot ceiling and port
// resources.
func plainSuggestSpec(t *testing.T, max int, resources []spec.Resource) *spec.Spec {
	t.Helper()
	s := &spec.Spec{
		Version:   1,
		App:       "plain-app",
		Slots:     spec.Slots{Max: &max},
		Resources: resources,
		Emit:      spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.json", Format: "json"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("the suggestion spec does not validate: %v", err)
	}
	return s
}

// sptr and iptr are the pointer helpers the suggestion specs need (strPtr
// and intPtr exist in p4_test.go).
func sptr(s string) *string { return &s }
func iptr(i int) *int       { return &i }
