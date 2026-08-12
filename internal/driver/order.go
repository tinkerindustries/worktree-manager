package driver

// order.go is the sequencing contract of 03-drivers.md §5: apply runs in
// dependency order derived from the template references — every resource
// after everything its template references — and teardown runs in reverse.
// The phase-8 forcing rule ("machine first among its dependents, when it
// arrives") slots into the same keep predicate the ordering functions use.

import "github.com/mrgeoffrich/worktree-manager/internal/spec"

// resourceByName finds one resource of the spec by name.
func resourceByName(s *spec.Spec, name string) *spec.Resource {
	if s == nil {
		return nil
	}
	for i := range s.Resources {
		if s.Resources[i].Name == name {
			return &s.Resources[i]
		}
	}
	return nil
}

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
		r := resourceByName(s, name)
		if r == nil {
			return
		}
		if r.Template != nil {
			for _, v := range templateVarsOf(*r.Template) {
				if resourceByName(s, v) != nil {
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
// order restricted to the resources whose driver implements apply. When the
// machine driver arrives in phase 8, its "forced first among its
// dependents" rule is implemented here, in the same keep predicate.
func (r Registry) ApplyOrder(s *spec.Spec) []string {
	return DependencyOrder(s, func(res *spec.Resource) bool {
		d := r.Driver(res.Type)
		return d != nil && d.HasApply()
	})
}

// TeardownOrder returns the resource names in teardown order: the reverse
// of the dependency order restricted to the resources whose driver
// implements teardown (03-drivers.md §5). A dependent project's objects
// leave before the parent's.
func (r Registry) TeardownOrder(s *spec.Spec) []string {
	order := DependencyOrder(s, func(res *spec.Resource) bool {
		d := r.Driver(res.Type)
		return d != nil && d.HasTeardown()
	})
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}
	return order
}
