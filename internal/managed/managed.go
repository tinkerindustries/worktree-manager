// Package managed is the generated-artefact half of the delivery channel
// (05-delivery.md §3.1's .env convention, reused for every file the
// onboarding skill generates — 09-onboarding.md §5): a delimited block
// inside the file that records the spec fields the file was generated
// from, replaced on every regeneration so hand edits outside the block
// survive.
//
// The markers are the .env block's own — `# --- managed by wt; edits below
// are overwritten ---` and `# --- end ---` — because the phase-7 brief
// says to follow that convention rather than inventing a second one
// (plan.md §5 phase 7). In a shell script the lines are comments, in a
// markdown file they render as headings; the parser is line-based and
// never cares.
//
// Inside the block, lines beginning `# wt-field: name=value` are the
// machine-compared records — the fields doctor checks against the current
// spec and ledger, so a moved band or a renamed resource is a finding
// naming the file and the field (09-onboarding.md §5). Any other line is
// content: human-readable facts the file needs, machine-written and
// machine-refreshed.
//
// The refusal semantics are the .env's (05-delivery.md §8): unbalanced or
// nested markers refuse the write, naming the line numbers.
package managed

import (
	"fmt"
	"sort"
	"strings"
)

// StartMarker and EndMarker delimit the managed block. They are the .env
// block's markers verbatim: one convention for every wt-managed block in
// every file the tool writes (05-delivery.md §3.1).
const (
	StartMarker = "# --- managed by wt; edits below are overwritten ---"
	EndMarker   = "# --- end ---"
)

// FieldPrefix marks one machine-compared record inside the block: a line
// `# wt-field: <name>=<value>`. Doctor parses these and compares them
// against the current spec and band ledger.
const FieldPrefix = "# wt-field: "

// MarkersError is the refusal to write when the managed markers are
// unbalanced or nested (the .env rule, 05-delivery.md §8): it names the
// offending line numbers.
type MarkersError struct {
	StartLines []int
	EndLines   []int
}

func (e *MarkersError) Error() string {
	describe := func(what string, lines []int) string {
		if len(lines) == 0 {
			return "no " + what + " marker"
		}
		nums := make([]string, len(lines))
		for i, l := range lines {
			nums[i] = fmt.Sprintf("line %d", l)
		}
		return what + " marker(s) at " + strings.Join(nums, ", ")
	}
	start, end := describe("start", e.StartLines), describe("end", e.EndLines)
	switch {
	case len(e.StartLines) > 1 || len(e.EndLines) > 1:
		return fmt.Sprintf("refusing to regenerate: nested or duplicated managed markers (%s; %s)", start, end)
	case len(e.StartLines) == 1 && len(e.EndLines) == 1 && e.StartLines[0] >= e.EndLines[0]:
		return fmt.Sprintf("refusing to regenerate: the end marker at line %d precedes its start marker at line %d", e.EndLines[0], e.StartLines[0])
	case len(e.StartLines) == 1:
		return fmt.Sprintf("refusing to regenerate: the managed block is never closed (%s)", start)
	default:
		return fmt.Sprintf("refusing to regenerate: an end marker without a start marker (%s)", end)
	}
}

// Block is the parsed managed block of one file: the field records and the
// content lines, in file order.
type Block struct {
	Fields  []Field // one per `# wt-field:` record, in file order
	Content []string
}

// Field is one `# wt-field: name=value` record.
type Field struct {
	Name  string
	Value string
}

// Lookup returns the first record with the given name.
func (b *Block) Lookup(name string) (string, bool) {
	for _, f := range b.Fields {
		if f.Name == name {
			return f.Value, true
		}
	}
	return "", false
}

// Parse reads the managed block out of content. It returns the block and
// whether the file carries the markers at all. A file with no markers has
// an empty block; a file with unbalanced or nested markers is refused with
// the line numbers.
func Parse(content []byte) (*Block, bool, error) {
	lines := splitLines(content)
	starts, ends := findMarkers(lines)
	if !validMarkers(starts, ends) {
		return nil, false, &MarkersError{StartLines: starts, EndLines: ends}
	}
	if len(starts) == 0 {
		return &Block{}, false, nil
	}
	block := &Block{}
	for _, line := range lines[starts[0] : ends[0]-1] {
		if rest, ok := strings.CutPrefix(line, FieldPrefix); ok {
			name, value, found := strings.Cut(rest, "=")
			if found {
				block.Fields = append(block.Fields, Field{Name: name, Value: value})
				continue
			}
		}
		block.Content = append(block.Content, line)
	}
	return block, true, nil
}

// Render builds the block's lines: the start marker, one `# wt-field:`
// record per field (sorted by name, so regeneration is deterministic), the
// content lines, and the end marker.
func Render(fields map[string]string, content []string) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	out := []string{StartMarker}
	for _, name := range names {
		out = append(out, FieldPrefix+name+"="+fields[name])
	}
	out = append(out, content...)
	out = append(out, EndMarker)
	return out
}

// Replace swaps the managed block of an existing file for a freshly
// rendered one, preserving everything outside the markers — the hand edits
// that must survive regeneration (09-onboarding.md §5). A file that
// carries no markers yet gets the block appended: the CLAUDE.md case,
// where the repo's own text stays and the tripwire section joins it.
// Unbalanced or nested markers refuse, naming the line numbers.
func Replace(existing []byte, fields map[string]string, content []string) ([]byte, error) {
	lines := splitLines(existing)
	starts, ends := findMarkers(lines)
	if !validMarkers(starts, ends) {
		return nil, &MarkersError{StartLines: starts, EndLines: ends}
	}
	block := Render(fields, content)
	if len(starts) == 0 {
		// Append the block: nothing of the existing file is touched.
		out := append(lines, block...)
		return joinLines(out), nil
	}
	kept := append([]string{}, lines[:starts[0]-1]...)
	kept = append(kept, block...)
	kept = append(kept, lines[ends[0]:]...)
	return joinLines(kept), nil
}

// splitLines splits content on newlines, dropping the final empty segment
// and tolerating CRLF — the same rule the .env block uses.
func splitLines(content []byte) []string {
	raw := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	if len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}
	return raw
}

// joinLines re-joins the lines with a trailing newline, exactly like the
// .env block writer.
func joinLines(lines []string) []byte {
	data := []byte(strings.Join(lines, "\n"))
	if len(lines) > 0 {
		data = append(data, '\n')
	}
	return data
}

// findMarkers returns the 1-based line numbers of the start and end
// markers, in file order.
func findMarkers(lines []string) (starts, ends []int) {
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case StartMarker:
			starts = append(starts, i+1)
		case EndMarker:
			ends = append(ends, i+1)
		}
	}
	return starts, ends
}

// validMarkers accepts exactly one start before exactly one end: a second
// marker of either kind is nested or duplicated, a lone one is unbalanced,
// and an end before its start is refused (05-delivery.md §8).
func validMarkers(starts, ends []int) bool {
	if len(starts) > 1 || len(ends) > 1 {
		return false
	}
	if len(starts) == 0 && len(ends) == 0 {
		return true
	}
	if len(starts) == 1 && len(ends) == 1 {
		return starts[0] < ends[0]
	}
	return false
}
