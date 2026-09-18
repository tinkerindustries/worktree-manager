package coord

// security_test.go is the phase-9 security pass's rails, one test per
// finding that was fixed: an identity key is a credential and is redacted
// for every reader who is not that identity (list, clients list, the
// ownership-refusal message), and bands.reserve — a machine-global policy
// mutation — is host-client-only, making the design's container grant
// ("create entries and mutate what it created", ARCHITECTURE.md §12.2) an
// enforced boundary rather than a hope.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/api"
)

// TestListRedactsForeignNamedOwnerKey: a named client's key is its token —
// serving it to any other client would hand that client the identity, with
// which it could pass the ownership check and read the owner's secrets
// with --wide. The owner sees its own full key; everyone else sees the
// redacted form. Ephemeral session ids are redacted the same way.
func TestListRedactsForeignOwnerKeys(t *testing.T) {
	h := fleetHarness(t)
	named, err := h.Connect(api.KindNamed, "named-token-0123456789abcdef")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	other, err := h.ConnectPeer(5000, api.KindHost, "")
	if err != nil {
		t.Fatalf("second hello refused: %+v", err)
	}

	e := baseFleetEntry(tempRoot(t), true)
	e.Owner, e.OwnerKind = "named-token-0123456789abcdef", api.KindNamed
	fleetEntry(t, h, e)

	// The owner's own listing carries the full key.
	mine := listEntries(t, h, named, false)
	if mine.Entries[0].Owner != "named-token-0123456789abcdef" {
		t.Errorf("the owner's own listing shows %q, want the full token", mine.Entries[0].Owner)
	}
	// Any other client sees the redacted form — never the token.
	theirs := listEntries(t, h, other, false)
	got := theirs.Entries[0].Owner
	if got == "named-token-0123456789abcdef" {
		t.Errorf("a foreign listing leaks the named token: %q", got)
	}
	if len(got) == 0 || len(got) >= 20 {
		t.Errorf("a foreign listing shows %q, want a short redaction", got)
	}
	if !strings.Contains(theirs.Entries[0].Flags[0], "foreign") {
		t.Errorf("the entry must still be marked foreign: %v", theirs.Entries[0].Flags)
	}
}

// TestClientsListRedactsForeignIdentities: a client row's identity is the
// token (named) or session id (ephemeral); only the client itself sees its
// own full identity.
func TestClientsListRedactsForeignIdentities(t *testing.T) {
	h := fleetHarness(t)
	named, err := h.Connect(api.KindNamed, "named-token-0123456789abcdef")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	other, err := h.ConnectPeer(5000, api.KindHost, "")
	if err != nil {
		t.Fatalf("second hello refused: %+v", err)
	}

	resp := h.Request(context.Background(), other, verbClients, nil)
	if resp.Error != nil {
		t.Fatalf("clients.list refused: %+v", resp.Error)
	}
	var res api.ClientsListResult
	mustUnmarshal(t, resp.Result, &res)
	for _, c := range res.Clients {
		if c.Kind != api.KindNamed {
			continue
		}
		if c.Identity == "named-token-0123456789abcdef" {
			t.Errorf("a foreign client's listing leaks the named token: %q", c.Identity)
		}
		if len(c.Identity) == 0 || len(c.Identity) >= 20 {
			t.Errorf("a foreign client's listing shows %q, want a short redaction", c.Identity)
		}
	}

	// The client itself sees its own full identity.
	resp = h.Request(context.Background(), named, verbClients, nil)
	if resp.Error != nil {
		t.Fatalf("clients.list refused: %+v", resp.Error)
	}
	mustUnmarshal(t, resp.Result, &res)
	for _, c := range res.Clients {
		if c.Kind == api.KindNamed && c.Identity != "named-token-0123456789abcdef" {
			t.Errorf("the named client does not see its own full identity: %q", c.Identity)
		}
	}
}

// TestOwnershipRefusalNeverLeaksTheToken: the mutating-call refusal for a
// named-owned entry names a redacted owner — the message a non-owner reads
// must not carry the token it would need to become the owner.
func TestOwnershipRefusalNeverLeaksTheToken(t *testing.T) {
	h := fleetHarness(t)
	other, err := h.ConnectPeer(5000, api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	e := baseFleetEntry(tempRoot(t), true)
	e.Owner, e.OwnerKind = "named-token-0123456789abcdef", api.KindNamed
	fleetEntry(t, h, e)

	resp := h.Request(context.Background(), other, verbActivate, &api.EntryRef{App: e.App, Slug: e.Slug})
	if resp.Error == nil || resp.Error.Code != 3 {
		t.Fatalf("foreign activate = %+v, want a refusal", resp.Error)
	}
	if strings.Contains(resp.Error.Msg, "named-token-0123456789abcdef") ||
		strings.Contains(resp.Error.Remedy, "named-token-0123456789abcdef") {
		t.Errorf("the refusal leaks the token:\n%s\n%s", resp.Error.Msg, resp.Error.Remedy)
	}
	if !strings.Contains(resp.Error.Msg, "named client") {
		t.Errorf("the refusal must still name the owner's kind: %s", resp.Error.Msg)
	}
}

// TestBandsReserveIsHostClientOnly: bands.reserve changes machine-global
// policy — the port ledger and the host-global reservations — and the
// design's grant to a container client is limited to the entries it
// created. Named and ephemeral sessions are refused; the host client
// succeeds.
func TestBandsReserveIsHostClientOnly(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sp := testSpec(t, "compose-app", 8)

	for _, tc := range []struct {
		name  string
		kind  string
		token string
	}{
		{"named", api.KindNamed, "named-token-0123456789abcdef"},
		{"ephemeral", api.KindEphemeral, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess, err := h.Connect(tc.kind, tc.token)
			if err != nil {
				t.Fatalf("hello refused: %+v", err)
			}
			resp := h.Request(context.Background(), sess, verbBandsReserve,
				&api.ReserveBandArgs{Spec: *sp, Bases: map[string]int{"api": 4200}})
			if resp.Error == nil || resp.Error.Code != 3 {
				t.Fatalf("bands.reserve by a %s client = %+v, want a refusal", tc.kind, resp.Error)
			}
			if !strings.Contains(resp.Error.Remedy, "on the host") {
				t.Errorf("the refusal must name the host remedy: %s", resp.Error.Remedy)
			}
		})
	}

	// The host client can still register the band.
	host, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	resp := h.Request(context.Background(), host, verbBandsReserve,
		&api.ReserveBandArgs{Spec: *sp, Bases: map[string]int{"api": 4200}})
	if resp.Error != nil {
		t.Fatalf("bands.reserve by the host client refused: %+v", resp.Error)
	}
}
