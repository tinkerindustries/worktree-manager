package store

import (
	"strconv"
)

// BandsFileName is the band ledger's filename inside the store: it maps each
// app to the port bases it holds, and holds the machine-wide reservations no
// app may allocate from (ARCHITECTURE.md §8.2, §8.4).
const BandsFileName = "bands.json"

// Band is one app's registration: a map from port resource name to the band
// base that resource derives from (spec.Context.Bases has the same shape —
// this is where the ledger's bases feed into resolution). One app, one band;
// re-registration replaces in place. Spans carries the required size of each
// base's range — slot ceiling × ports per slot, computed by the coordinator
// at registration — recorded so phase 6's doctor can detect overlap between
// two apps' bands without reading either app's spec.
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

// BandsFile is bands.json.
type BandsFile struct {
	Versioned
	Bands        []Band        `json:"bands"`
	Reservations []Reservation `json:"reservations"`
}

// ReadBands loads the band ledger; a missing file is an empty ledger.
func (s *Store) ReadBands() (BandsFile, error) {
	return loadFile(s, BandsFileName, func() BandsFile {
		return BandsFile{Versioned: Versioned{SchemaVersion: SchemaVersion}}
	})
}

// WriteBands persists the band ledger atomically.
func (s *Store) WriteBands(f BandsFile) error {
	f.Versioned = Versioned{SchemaVersion: SchemaVersion}
	return s.Save(BandsFileName, &f)
}
