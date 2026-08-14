package coord

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// TestReserveOwnPortKeepsBandsSuggestOffIt is the point of the whole
// mechanism: the tool allocates ports, and the coordinator's own port is
// one of the machine's ports. Without the reservation, suggest could hand
// an app a base whose range covers it and the collision would surface much
// later as a coordinator that cannot restart.
func TestReserveOwnPortKeepsBandsSuggestOffIt(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	ctx := context.Background()
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}

	// Reserve a port inside the range suggest would otherwise propose
	// first. With an empty ledger and a span of 8, suggest picks 1..8.
	if rerr := h.H.ReserveOwnPort("127.0.0.1:4"); rerr != nil {
		t.Fatalf("reserving the coordinator's own port: %v", rerr)
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
	if s.Low <= 4 && 4 <= s.High {
		t.Errorf("suggestion %+v covers the coordinator's own port 4", s)
	}
}

// TestReserveOwnPortIsIdempotentAndMoves: the coordinator re-asserts this
// at every start, so restarting must not accumulate reservations, and
// moving to a new --addr must release the old port rather than leave it
// reserved with nothing behind it.
func TestReserveOwnPortIsIdempotentAndMoves(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))

	for range 3 {
		if err := h.H.ReserveOwnPort("127.0.0.1:7833"); err != nil {
			t.Fatalf("reserving the coordinator's own port: %v", err)
		}
	}
	bands, err := h.Store.ReadBands()
	if err != nil {
		t.Fatalf("reading the band ledger: %v", err)
	}
	if n := countOwnPortReservations(bands.Reservations); n != 1 {
		t.Errorf("three starts left %d coordinator reservations, want 1", n)
	}

	// Move the coordinator to a different port.
	if err := h.H.ReserveOwnPort("127.0.0.1:7900"); err != nil {
		t.Fatalf("re-reserving on a new address: %v", err)
	}
	bands, err = h.Store.ReadBands()
	if err != nil {
		t.Fatalf("reading the band ledger: %v", err)
	}
	if n := countOwnPortReservations(bands.Reservations); n != 1 {
		t.Fatalf("after moving there are %d coordinator reservations, want 1", n)
	}
	for _, r := range bands.Reservations {
		if r.Note != OwnPortNote {
			continue
		}
		if len(r.Ports) != 1 || r.Ports[0] != 7900 {
			t.Errorf("coordinator reservation holds %v, want only the new port 7900", r.Ports)
		}
	}
}

// TestReserveOwnPortRefusesAnUnparseableAddress: a coordinator that
// silently failed to reserve its own port is the case the reservation
// exists to prevent, so this is an error rather than a skip.
func TestReserveOwnPortRefusesAnUnparseableAddress(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	if err := h.H.ReserveOwnPort("not-host-port"); err == nil {
		t.Error("an unparseable listen address was accepted")
	}
}

func countOwnPortReservations(rs []store.Reservation) int {
	n := 0
	for _, r := range rs {
		if r.Note == OwnPortNote {
			n++
		}
	}
	return n
}
