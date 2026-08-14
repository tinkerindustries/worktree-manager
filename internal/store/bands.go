package store

import (
	"context"
	"strconv"
)

// Band is one app's registration: a map from port resource name to the band
// base that resource derives from (spec.Context.Bases has the same shape —
// this is where the ledger's bases feed into resolution). One app, one band;
// re-registration replaces in place. Spans carries the required size of each
// base's range — slot ceiling × ports per slot, computed by the coordinator
// at registration — recorded so doctor can detect overlap between two apps'
// bands without reading either app's spec.
type Band struct {
	App   string         `json:"app"`
	Bases map[string]int `json:"bases"`
	Spans map[string]int `json:"spans,omitempty"`
}

// Reservation is one host-global reservation: ports no app may allocate
// from and compose project names no teardown may reach, with the required
// note naming what holds the range. Nothing infers a production stack — a
// person declares it once per machine, and the note is what lets anyone
// later judge it (plan.md §8, R6). Names joined in phase 4: the namespace
// driver's B8.2 rail refuses to tear down a project whose resolved name
// matches a reserved name, and a name has to be declared somewhere — the
// ledger is where the machine's facts live.
type Reservation struct {
	Ports []int    `json:"ports"`
	Names []string `json:"names,omitempty"`
	Note  string   `json:"note"`
}

// PortsStrings renders the reservation's ports for a message.
func (r Reservation) PortsStrings() []string {
	out := make([]string, len(r.Ports))
	for i, p := range r.Ports {
		out[i] = strconv.Itoa(p)
	}
	return out
}

// BandsFile is the whole band ledger: every app's band and every
// host-global reservation. SchemaVersion is the database's own
// meta.schema_version.
type BandsFile struct {
	Versioned
	Bands        []Band        `json:"bands"`
	Reservations []Reservation `json:"reservations"`
}

// ReadBands loads the band ledger; a fresh store is an empty ledger.
func (s *Store) ReadBands() (BandsFile, error) {
	v, err := s.metaVersion()
	if err != nil {
		return BandsFile{}, err
	}
	if v > SchemaVersion {
		return BandsFile{}, &VersionError{Name: DBFileName, File: v, Current: SchemaVersion}
	}
	ctx := context.Background()
	q := New(s.db)
	brows, err := q.ListBands(ctx)
	if err != nil {
		return BandsFile{}, err
	}
	f := BandsFile{Versioned: Versioned{SchemaVersion: v}, Bands: []Band{}, Reservations: []Reservation{}}
	byApp := map[string]*Band{}
	for _, r := range brows {
		b, ok := byApp[r.App]
		if !ok {
			f.Bands = append(f.Bands, Band{App: r.App, Bases: map[string]int{}, Spans: map[string]int{}})
			b = &f.Bands[len(f.Bands)-1]
			byApp[r.App] = b
		}
		b.Bases[r.Resource] = int(r.Base)
		b.Spans[r.Resource] = int(r.Span)
	}
	resrows, err := q.ListReservations(ctx)
	if err != nil {
		return BandsFile{}, err
	}
	portRows, err := q.ListReservationPorts(ctx)
	if err != nil {
		return BandsFile{}, err
	}
	nameRows, err := q.ListReservationNames(ctx)
	if err != nil {
		return BandsFile{}, err
	}
	ports := map[int64][]int{}
	for _, r := range portRows {
		ports[r.ReservationID] = append(ports[r.ReservationID], int(r.Port))
	}
	names := map[int64][]string{}
	for _, r := range nameRows {
		names[r.ReservationID] = append(names[r.ReservationID], r.Name)
	}
	for _, r := range resrows {
		f.Reservations = append(f.Reservations, Reservation{
			Ports: ports[r.ID], Names: names[r.ID], Note: r.Note,
		})
	}
	return f, nil
}

// UpsertBand registers or replaces one app's band. Re-registration
// replaces in place: the app's rows are dropped and re-inserted in one
// transaction, so a registration with fewer resources than before cannot
// leave stale bases behind.
func (s *Store) UpsertBand(b Band) error {
	if err := s.checkSchemaVersion(); err != nil {
		return err
	}
	return s.WithTx(func(tx *Tx) error {
		return tx.upsertBand(b)
	})
}

// upsertBand is the transaction-level body: one app's rows, replaced.
func (t *Tx) upsertBand(b Band) error {
	ctx := context.Background()
	if err := t.q.DeleteBandApp(ctx, b.App); err != nil {
		return err
	}
	for resource, base := range b.Bases {
		if err := t.q.UpsertBand(ctx, UpsertBandParams{
			App: b.App, Resource: resource, Base: int64(base), Span: int64(b.Spans[resource]),
		}); err != nil {
			return err
		}
	}
	return nil
}

// AddReservation records one host-global reservation: the note row and
// its ports and names, all in one transaction — a reservation that
// half-applied (ports without the note, say) could never be judged or
// removed.
func (s *Store) AddReservation(r Reservation) error {
	if err := s.checkSchemaVersion(); err != nil {
		return err
	}
	return s.WithTx(func(tx *Tx) error {
		ctx := context.Background()
		res, err := tx.q.AddReservation(ctx, r.Note)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, p := range r.Ports {
			if err := tx.q.AddReservationPort(ctx, AddReservationPortParams{ReservationID: id, Port: int64(p)}); err != nil {
				return err
			}
		}
		for _, n := range r.Names {
			if err := tx.q.AddReservationName(ctx, AddReservationNameParams{ReservationID: id, Name: n}); err != nil {
				return err
			}
		}
		return nil
	})
}
