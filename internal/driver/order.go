package driver

// order.go is the sequencing contract of 03-drivers.md §5: apply runs in
// dependency order derived from the template references — every resource
// after everything its template references — and teardown runs in reverse.
// The phase-8 forcing rule ("machine first among its dependents, when it
// arrives") slots into the same keep predicate the ordering functions use.

import "github.com/mrgeoffrich/worktree-manager/internal/spec"

// DependencyOrder returns the resource names in dependency order — every
// resource after everything its template references — restricted to the
// resources keep accepts. The order is deterministic: a depth-first walk
// starting in declaration order, dependencies visited before their
// dependents. Only references to other resources are edges; the builtins
// ({app}, {slug}, ...) are not.
func DependencyOrder(s *spec.Spec, keep func(*spec.Resource) bool) []string {
	if s == nil {
		return nil
	}
	var order []string
	visited := make(map[string]bool, len(s.Resources))
	var visit func(name string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		r := spec.ResourceByName(s, name)
		if r == nil {
			return
		}
		if r.Template != nil {
			for _, v := range templateVarsOf(*r.Template) {
				if spec.ResourceByName(s, v) != nil {
					visit(v)
				}
			}
		}
		if keep(r) {
			order = append(order, name)
		}
	}
	for i := range s.Resources {
		visit(s.Resources[i].Name)
	}
	return order
}

// ApplyOrder returns the resource names in apply order: the dependency
// order restricted to the resources whose driver implements apply, with
// the phase-8 forcing rule applied — machine resources first among them,
// stably. A VM has to exist before anything expects its daemon
// (03-drivers.md §5), and the resources that need it (the cidr whose
// network lives inside the VM's docker, say) do not template-reference it,
// so the forcing cannot come from the dependency edges; it is this rule.
// Relative order is preserved within the two groups, so declaration order
// still decides between two machines.
func (r Registry) ApplyOrder(s *spec.Spec) []string {
	order := DependencyOrder(s, func(res *spec.Resource) bool {
		d := r.Driver(res.Type)
		return d != nil && d.HasApply()
	})
	return machinesFirst(order, s, r)
}

// machinesFirst stably partitions the ordered names: machine resources
// first (in their existing relative order), everything else after (also in
// its existing relative order).
func machinesFirst(order []string, s *spec.Spec, r Registry) []string {
	var machines, rest []string
	for _, name := range order {
		res := spec.ResourceByName(s, name)
		d := r.Driver(res.Type)
		if res.Type == "machine" && d != nil {
			machines = append(machines, name)
		} else {
			rest = append(rest, name)
		}
	}
	return append(machines, rest...)
}

// TeardownOrder returns the resource names in teardown order: the reverse
// of the dependency order restricted to the resources whose driver
// implements teardown (03-drivers.md §5). A dependent project's objects
// leave before the parent's, and the machine — forced first in apply —
// is destroyed last, after everything that used its daemon.
func (r Registry) TeardownOrder(s *spec.Spec) []string {
	order := machinesFirst(DependencyOrder(s, func(res *spec.Resource) bool {
		d := r.Driver(res.Type)
		return d != nil && d.HasTeardown()
	}), s, r)
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}
	return order
}
