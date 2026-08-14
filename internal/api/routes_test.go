package api

import (
	"strings"
	"testing"
)

// TestRoutesIsTheFrozenSurface pins the route table: all fifteen RPCs of
// the R1 surface, each POSTed under /v1 (with the two GET exceptions),
// and no route outside the table. The table is frozen at the end of R1 —
// R2 generates the OpenAPI document and the client from it (plan.md §5,
// phase R2; PLAN-SCOPE.md, "Behaviours").
func TestRoutesIsTheFrozenSurface(t *testing.T) {
	want := []struct {
		verb, path string
	}{
		{VerbSession, "/v1/session"},
		{VerbAllocate, "/v1/allocate"},
		{VerbMaterialise, "/v1/materialise"},
		{VerbActivate, "/v1/activate"},
		{VerbRelease, "/v1/release"},
		{VerbRm, "/v1/rm"},
		{VerbBandsReserve, "/v1/bands/reserve"},
		{VerbBandsList, "/v1/bands/list"},
		{VerbBandsSuggest, "/v1/bands/suggest"},
		{VerbPortsScan, "/v1/ports/scan"},
		{VerbList, "/v1/list"},
		{VerbDoctor, "/v1/doctor"},
		{VerbReconcile, "/v1/reconcile"},
		{VerbClientsList, "/v1/clients/list"},
		{VerbPing, "/v1/ping"},
	}
	if len(Routes) != len(want) {
		t.Fatalf("route table has %d routes, want %d", len(Routes), len(want))
	}
	for i, w := range want {
		if Routes[i].Verb != w.verb || Routes[i].Path != w.path {
			t.Errorf("route %d = %+v, want verb %q path %q", i, Routes[i], w.verb, w.path)
		}
		got, ok := PathForVerb(w.verb)
		if !ok || got != w.path {
			t.Errorf("PathForVerb(%q) = %q, %v; want %q", w.verb, got, ok, w.path)
		}
		verb, ok := VerbForPath(w.path)
		if !ok || verb != w.verb {
			t.Errorf("VerbForPath(%q) = %q, %v; want %q", w.path, verb, ok, w.verb)
		}
	}
	// The GET exceptions are part of the surface.
	if VersionPath != "/version" {
		t.Errorf("version path = %q", VersionPath)
	}
	for _, p := range []string{VersionPath, "/v1/ping"} {
		if !IsRPC(p) {
			t.Errorf("IsRPC(%q) = false, want true", p)
		}
	}
	// Nothing outside the table is an RPC.
	if IsRPC("/v1/bogus") || IsRPC("/") || IsRPC("/v1") {
		t.Error("IsRPC accepted a path outside the route table")
	}
}

// TestCheckVersion is the client's half of the old negotiation: an
// overlapping pair agrees (nil error), a non-overlapping pair refuses
// naming the binary that must move and the version it must speak.
func TestCheckVersion(t *testing.T) {
	if err := CheckVersion(1, 1, 1, 1); err != nil {
		t.Fatalf("identical ranges refused: %+v", err)
	}
	if err := CheckVersion(1, 1, 1, 3); err != nil {
		t.Fatalf("client narrower refused: %+v", err)
	}
	if err := CheckVersion(2, 3, 1, 2); err != nil {
		t.Fatalf("overlapping ranges refused: %+v", err)
	}

	cases := []struct {
		name                   string
		cmin, cmax, smin, smax int
		wantSide               string
		wantNeed               int
	}{
		{"client one ahead", 2, 2, 1, 1, "coordinator", 2},
		{"client far ahead", 4, 5, 1, 3, "coordinator", 4},
		{"coordinator one ahead", 1, 1, 2, 2, "client", 2},
		{"coordinator far ahead", 1, 3, 4, 5, "client", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckVersion(tc.cmin, tc.cmax, tc.smin, tc.smax)
			if err == nil {
				t.Fatal("non-overlapping ranges agreed, want a refusal")
			}
			if err.Code != 3 {
				t.Errorf("refusal code = %d, want 3", err.Code)
			}
			want := "upgrade the " + tc.wantSide
			if !strings.Contains(err.Msg, want) {
				t.Errorf("message %q does not name the upgrade (%q)", err.Msg, want)
			}
			for _, frag := range []string{"do not overlap", "speaks", "to speak API version"} {
				if !strings.Contains(err.Msg, frag) {
					t.Errorf("message %q lacks %q", err.Msg, frag)
				}
			}
		})
	}
}
