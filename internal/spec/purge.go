package spec

// purge.go is the state-path purge decision: whether a teardown deletes a
// resource's store (03-drivers.md §4.4). One rule with two readers — the
// state-path driver deletes by it and `wt rm` reports by it — so what the
// report names and what the teardown removes cannot disagree.

import "slices"

// PurgeOnTeardown reports a state-path resource's teardown purge mode:
// "always" when the store goes with the worktree, "flag" when it survives
// a teardown that did not select it. A resource with no purge block is in
// flag mode and has no flag, so nothing selects it.
func PurgeOnTeardown(r *Resource) string {
	if r.Purge == nil || r.Purge.OnTeardown == "" {
		return DefaultPurgeOnTeardown
	}
	return r.Purge.OnTeardown
}

// PurgeSelected reports whether the run selected this resource's store by
// name: its purge.flag is among the flags the caller passed. `--purge
// <resource>` resolves to the same flag before it gets here.
func PurgeSelected(r *Resource, purgeFlags []string) bool {
	if r.Purge == nil || r.Purge.Flag == "" {
		return false
	}
	return slices.Contains(purgeFlags, r.Purge.Flag)
}

// PurgeKept reports whether the run passed this resource's purge.keep_flag
// — the one-run opt-out of a store that purges on teardown, the shape
// --keep-vm has for a machine.
func PurgeKept(r *Resource, keepFlags []string) bool {
	if r.Purge == nil || r.Purge.KeepFlag == "" {
		return false
	}
	return slices.Contains(keepFlags, r.Purge.KeepFlag)
}

// Purges reports whether this teardown deletes the resource's store: the
// run selected it, or the spec purges on teardown and the run did not pass
// the keep flag.
func Purges(r *Resource, purgeFlags, keepFlags []string) bool {
	if r.Purge == nil {
		return false
	}
	if PurgeSelected(r, purgeFlags) {
		return true
	}
	return PurgeOnTeardown(r) == PurgeOnTeardownAlways && !PurgeKept(r, keepFlags)
}
