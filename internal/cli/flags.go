package cli

// flags.go holds the flag shapes the verbs share. A repeatable flag is one
// shape with a parser: without this, every verb that takes a list grows its
// own two-method type and they drift on how they render.

import (
	"fmt"
	"strconv"
	"strings"
)

// listFlag is a repeatable flag: each occurrence parses one value and
// appends it to target.
type listFlag[T any] struct {
	target *[]T
	parse  func(string) (T, error)
}

func (f listFlag[T]) String() string {
	if f.target == nil {
		return ""
	}
	parts := make([]string, len(*f.target))
	for i, v := range *f.target {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ",")
}

func (f listFlag[T]) Set(v string) error {
	parsed, err := f.parse(v)
	if err != nil {
		return err
	}
	*f.target = append(*f.target, parsed)
	return nil
}

// stringList is a repeatable flag collecting its values verbatim: --purge,
// --base and --name.
func stringList(target *[]string) listFlag[string] {
	return listFlag[string]{target: target, parse: func(s string) (string, error) { return s, nil }}
}

// portList is a repeatable flag collecting port numbers: --port.
func portList(target *[]int) listFlag[int] {
	return listFlag[int]{target: target, parse: func(s string) (int, error) {
		p, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("%q is not a port number: %v", s, err)
		}
		return p, nil
	}}
}

// stringMapFlag is the repeatable <name>=<value> flag: --param.
type stringMapFlag map[string]string

func (m stringMapFlag) String() string { return "" }

func (m stringMapFlag) Set(v string) error {
	name, value, ok := strings.Cut(v, "=")
	if !ok || name == "" {
		return fmt.Errorf("--param expects <name>=<value>, got %q", v)
	}
	m[name] = value
	return nil
}
