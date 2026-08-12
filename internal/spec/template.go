package spec

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// builtinVars are the template variables that are not resource names,
// per 03-drivers.md §3.3: app, slug, slot, home and worktree (the worktree
// root).
var builtinVars = []string{"app", "slug", "slot", "home", "worktree"}

// isBuiltinVar reports whether name is one of the five builtin variables.
func isBuiltinVar(name string) bool {
	for _, v := range builtinVars {
		if name == v {
			return true
		}
	}
	return false
}

// templateVars returns the variable names referenced by t, in order. Every
// '{' must open a reference — a literal brace cannot be expressed — and a
// malformed reference is an error. The caller attaches the field name.
func templateVars(t string) ([]string, error) {
	var names []string
	for i := 0; i < len(t); {
		if t[i] != '{' {
			i++
			continue
		}
		j := strings.IndexByte(t[i:], '}')
		if j < 0 {
			return nil, fmt.Errorf("unclosed '{'")
		}
		name := t[i+1 : i+j]
		if name == "" {
			return nil, fmt.Errorf("empty '{ }' reference")
		}
		names = append(names, name)
		i += j + 1
	}
	return names, nil
}

// substitute replaces every {var} reference in t. lookup must answer for
// every variable; a miss is an error naming the variable. Substitution is
// single-pass: a resolved value is never re-scanned for references.
func substitute(t string, lookup func(name string) (string, bool)) (string, error) {
	var b strings.Builder
	b.Grow(len(t))
	for i := 0; i < len(t); {
		if t[i] != '{' {
			b.WriteByte(t[i])
			i++
			continue
		}
		j := strings.IndexByte(t[i:], '}')
		if j < 0 {
			return "", fmt.Errorf("unclosed '{'")
		}
		name := t[i+1 : i+j]
		v, ok := lookup(name)
		if !ok {
			return "", fmt.Errorf("unknown template variable {%s}", name)
		}
		b.WriteString(v)
		i += j + 1
	}
	return b.String(), nil
}

// resourceNameRe is the resource-name pattern. Resource names are template
// variables, and the design's own example is `compose_test` (03-drivers.md
// §3.3), so underscores are allowed; the cap is ResourceNameMaxLen.
var resourceNameRe = mustCompile(`^[a-z][a-z0-9_-]*$`)

// envNameRe is the environment-variable-name pattern for emit.env keys and
// the reader's env var: a POSIX environment variable name.
var envNameRe = mustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// goPackageRe is the Go package-name pattern for emit.reader.package.
var goPackageRe = mustCompile(`^[a-z][a-z0-9_]*$`)

func mustCompile(expr string) *regexp.Regexp {
	return regexp.MustCompile(expr)
}

// validateTemplateVars checks every reference in t against the builtin
// variables and the resource names. A reference that matches the resource
// pattern but no resource is an unknown resource; anything else is an
// unknown variable.
func validateTemplateVars(t, field string, resources map[string]bool) error {
	vars, err := templateVars(t)
	if err != nil {
		return &FieldError{Field: field, Reason: err.Error()}
	}
	for _, v := range vars {
		if isBuiltinVar(v) {
			continue
		}
		if resources[v] {
			continue
		}
		if resourceNameRe.MatchString(v) {
			return &FieldError{Field: field, Reason: fmt.Sprintf("unknown resource %q in template", v)}
		}
		return &FieldError{Field: field, Reason: fmt.Sprintf("unknown template variable {%s}", v)}
	}
	return nil
}

// slotString renders the {slot} variable. {app}, {home} and {worktree} are
// plain strings; {slug} is validated separately.
func slotString(slot int) string {
	return strconv.Itoa(slot)
}

// Substitute resolves one non-resource template — a seed.from path or a
// hand-authored shared name — against the same variables Resolve uses: the
// builtins ({app}, {slug}, {slot}, {home}, {worktree}) plus the names of
// other resources, which resolve through the already-resolved table. It is
// the exported form of the substitution the resource templates go through,
// for templates the schema does not model as resources: the state-path
// driver resolves seed.from with it, and phase 5 resolves the shared
// block's hand-authored half (03-drivers.md §3.3).
func Substitute(t string, ctx Context, resolved map[string]Resolved) (string, error) {
	value, err := substitute(t, func(name string) (string, bool) {
		switch name {
		case "app":
			return ctx.App, true
		case "slug":
			return ctx.Slug, true
		case "slot":
			return strconv.Itoa(ctx.Slot), true
		case "home":
			return ctx.Home, true
		case "worktree":
			return ctx.Worktree, true
		}
		v, ok := resolved[name]
		if !ok {
			return "", false
		}
		return fmt.Sprint(v.Value), true
	})
	if err != nil {
		return "", err
	}
	return value, nil
}
