package driver

// sequence.go runs the six-operation contract across a whole spec: apply in
// dependency order with reverse-order rollback, and teardown in reverse
// continuing past failures. Phase 5's init leans on the rollback and phase
// 5's rm leans on the teardown; both are deliverables of this phase rather
// than details (plan.md §5, phase 4).

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// ApplyOutcome is one resource's apply result, in apply order.
type ApplyOutcome struct {
	Resource string
	Notes    []string
}

// ApplyReport is what ApplyAll returns: what applied, in order, and — when
// a failure part-way through triggered the rollback — which resources were
// torn down in reverse and whether the rollback itself failed.
type ApplyReport struct {
	Outcomes []ApplyOutcome
	// Failed is the resource whose apply failed; empty when all succeeded.
	Failed string
	// Err is the failure, when Failed is set.
	Err error
	// RolledBack lists the applied resources torn down in reverse order.
	RolledBack []string
	// RollbackErr is a teardown failure during the rollback, if any.
	RollbackErr error
}

// ApplyAll runs apply over every resource whose driver implements it, in
// dependency order. A failure part-way through tears down the applied
// resources in reverse — the rollback phase 5's init depends on
// (03-drivers.md §5).
func (r Registry) ApplyAll(s *spec.Spec, values map[string]spec.Resolved, env Env) ApplyReport {
	var rep ApplyReport
	for _, name := range r.ApplyOrder(s) {
		res := resourceByName(s, name)
		d := r.Driver(res.Type)
		value, ok := values[name]
		if !ok {
			return failApply(rep, name, fmt.Errorf("no resolved value recorded for resource %q", name))
		}
		ar, err := d.Apply(res, value.Value, env)
		if err != nil {
			return r.rollback(rep, name, err, s, values, env)
		}
		rep.Outcomes = append(rep.Outcomes, ApplyOutcome{Resource: name, Notes: ar.Notes})
	}
	return rep
}

// failApply records the failed resource and returns the report.
func failApply(rep ApplyReport, name string, err error) ApplyReport {
	rep.Failed = name
	rep.Err = err
	return rep
}

// rollback records the failure and tears down the applied resources in
// reverse order. The rollback's own teardown failures are collected, not
// fatal: the report says what could not be rolled back.
func (r Registry) rollback(rep ApplyReport, failed string, err error, s *spec.Spec, values map[string]spec.Resolved, env Env) ApplyReport {
	rep.Failed = failed
	rep.Err = err
	for i := len(rep.Outcomes) - 1; i >= 0; i-- {
		name := rep.Outcomes[i].Resource
		res := resourceByName(s, name)
		d := r.Driver(res.Type)
		value := values[name]
		if d != nil && d.HasTeardown() {
			if terr := d.Teardown(res, value.Value, env); terr != nil {
				rep.RollbackErr = terr
			}
		}
		rep.RolledBack = append(rep.RolledBack, name)
	}
	return rep
}

// TeardownOutcome is one resource's teardown outcome.
type TeardownOutcome struct {
	Resource  string
	OK        bool
	Survivors []Survivor
}

// Refusal is one refused teardown in a report: the resource and the reason
// naming the reservation or path (exit code 3 at the coordinator).
type Refusal struct {
	Resource string
	Reason   string
}

// TeardownReport is what TeardownAll returns: per-resource outcomes, the
// deduplicated survivors across resources, the resources whose teardown
// could not run at all (unavailable), the refusals, and the bounded-
// coverage notes.
type TeardownReport struct {
	Outcomes    []TeardownOutcome
	Survivors   []Survivor
	Unavailable []string
	Refusals    []Refusal
	Notes       []string
}

// Clean reports whether nothing survived, nothing is unavailable and
// nothing was refused — the slot may be freed.
func (rep TeardownReport) Clean() bool {
	return len(rep.Survivors) == 0 && len(rep.Unavailable) == 0 && len(rep.Refusals) == 0
}

// TeardownAll runs teardown over every resource whose driver implements it,
// in reverse dependency order, continuing past a failure and collecting
// what survived — stopping early would leave more behind than continuing
// does (03-drivers.md §5). purgeFlags are the CLI purge flags the caller
// passed; a state-path resource whose purge.flag was passed is purged, every
// other state-path is left alone (03-drivers.md §4.4).
//
// The iteration covers the union of the spec's resources and the recorded
// values: a value with no spec row (the spec changed since allocation)
// still gets torn down, because the registry's handles are authoritative
// (03-drivers.md §2.2).
func (r Registry) TeardownAll(s *spec.Spec, values map[string]spec.Resolved, env Env, purgeFlags []string) TeardownReport {
	env.PurgeFlags = purgeFlags
	var rep TeardownReport

	// The iteration covers the union of the spec's teardown-capable
	// resources and the recorded values: a recorded value whose spec row is
	// gone (the repo's spec changed since allocation) still has to be
	// accounted for — it cannot be torn down without its spec row, so it is
	// reported as surviving and the slot stays held. The registry's handles
	// are authoritative (03-drivers.md §2.2).
	order := r.TeardownOrder(s)
	covered := make(map[string]bool, len(order))
	for _, name := range order {
		covered[name] = true
	}
	var extra []string
	for name := range values {
		if !covered[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	order = append(order, extra...)

	seenSurvivors := make(map[string]bool)
	seenRefusals := make(map[string]bool)
	for _, name := range order {
		res := resourceByName(s, name)
		value, ok := values[name]
		if res == nil {
			if ok {
				// A handle with no spec row: the driver cannot interpret it
				// (kind, files, seed.from all live in the spec), so it
				// survives and the slot stays held until the spec catches
				// up — failing closed rather than freeing the slot with
				// objects still out there.
				rep.Survivors = append(rep.Survivors, Survivor{Kind: "resource", Name: name, Resource: name,
					Reason: "the spec no longer declares this resource; it cannot be torn down without its spec row"})
				rep.Outcomes = append(rep.Outcomes, TeardownOutcome{Resource: name, OK: false})
			}
			continue
		}
		if !ok {
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s: no recorded value; nothing to tear down", name))
			continue
		}
		d := r.Driver(res.Type)
		if d == nil {
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s: no driver for type %q in this phase; skipped", name, res.Type))
			continue
		}
		if !d.HasTeardown() {
			continue // e.g. a port: a port is not a thing that exists
		}
		out := TeardownOutcome{Resource: name, OK: true}
		err := d.Teardown(res, value.Value, env)
		switch {
		case err == nil:
		case isRefusal(err):
			out.OK = false
			key := err.Error()
			if !seenRefusals[key] {
				seenRefusals[key] = true
				rep.Refusals = append(rep.Refusals, Refusal{Resource: name, Reason: err.Error()})
			}
		case isErrUnavailable(err):
			out.OK = false
			rep.Unavailable = append(rep.Unavailable, fmt.Sprintf("%s: %v", name, err))
		case isTeardownError(err):
			out.OK = false
			te := err.(*TeardownError)
			out.Survivors = te.Survivors
			for _, sv := range te.Survivors {
				key := sv.Kind + "|" + sv.Name + "|" + sv.Resource
				if !seenSurvivors[key] {
					seenSurvivors[key] = true
					rep.Survivors = append(rep.Survivors, sv)
				}
			}
		default:
			out.OK = false
			sv := Survivor{Kind: "resource", Name: name, Resource: name, Reason: err.Error()}
			out.Survivors = []Survivor{sv}
			rep.Survivors = append(rep.Survivors, sv)
		}
		rep.Outcomes = append(rep.Outcomes, out)
	}
	return rep
}

// Summary renders the report's bounded-coverage statement: everything that
// survived, was unavailable or was refused, one line each. The empty string
// means the teardown was clean.
func (rep TeardownReport) Summary() string {
	var parts []string
	for _, rf := range rep.Refusals {
		parts = append(parts, "refused: "+rf.Reason)
	}
	for _, u := range rep.Unavailable {
		parts = append(parts, "unavailable: "+u)
	}
	for _, sv := range rep.Survivors {
		parts = append(parts, fmt.Sprintf("survived: %s %s (%s)", sv.Kind, sv.Name, sv.Reason))
	}
	for _, n := range rep.Notes {
		parts = append(parts, n)
	}
	return strings.Join(parts, "; ")
}
