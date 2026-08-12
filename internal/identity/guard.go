package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// GuardRequest is the enforcement hook's input: the tool name and its input
// object, the shape of a Claude Code PreToolUse hook payload. Phase 7
// generates the hook against this shape (ARCHITECTURE.md §9.6).
type GuardRequest struct {
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
}

// FilePath extracts the file_path field from the tool input, reporting
// whether it was present and a string.
func (r GuardRequest) FilePath() (string, bool) {
	v, ok := r.ToolInput["file_path"]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// GuardVerdict is the one JSON object `wt guard --json` prints: whether the
// tool call is allowed and, when it is denied, exactly one reason. The
// denial reason names the correct worktree root verbatim, so the model
// reading it corrects itself and retries (ARCHITECTURE.md §9.6).
type GuardVerdict struct {
	Allowed      bool   `json:"allowed"`
	Reason       string `json:"reason,omitempty"`
	WorktreeRoot string `json:"worktree_root,omitempty"`
	Outcome      string `json:"outcome,omitempty"`
	ToolName     string `json:"tool_name,omitempty"`
}

// writeTools are the file-mutating tools the guard constrains. The set is
// deliberately small: an unknown tool is never assumed to write, because a
// guard producing false denials trains the agent to work around it
// (ARCHITECTURE.md §9.6).
var writeTools = map[string]bool{
	"Write":        true,
	"Edit":         true,
	"MultiEdit":    true,
	"NotebookEdit": true,
	"Patch":        true,
}

// Guard is the enforcement hook's engine. It classifies cwd, resolves the
// path under test and applies the containment test, denying with a reason
// that names the correct root verbatim. It reads git and the descriptor and
// opens no socket.
//
// Three denials, in priority order for a single reason per call: direct
// mutation of a live shared store named in the descriptor's shared block; a
// write whose resolved path lies outside the worktree; a read of a CLAUDE.md
// outside the worktree, whose parent copy is stale.
//
// It fails open on ambiguous shell constructs — pipes, $(...), environment
// variables and globs — because its goal is to make the obvious bypass loud,
// not to be a general bash sandbox: a tool call with no extractable file_path
// (a Bash command) is allowed, and so is a file_path carrying shell or glob
// metacharacters.
func Guard(cwd string, req GuardRequest) (*GuardVerdict, error) {
	cls, err := Classify(cwd, StandaloneDeclared())
	if err != nil {
		return nil, err
	}
	v := &GuardVerdict{
		WorktreeRoot: cls.WorktreeRoot,
		Outcome:      cls.Outcome.String(),
		ToolName:     req.ToolName,
	}
	switch cls.Outcome {
	case NotARepository:
		v.Allowed = true
		v.Reason = "not a git repository; nothing to guard"
		return v, nil
	case PrimaryCheckout, StandaloneClone:
		// Slot 0 is never managed and never isolated: nothing to guard
		// (A2). The hook is generated for worktrees of adopted repos, so
		// this branch serves manual use and the primary checkout.
		v.Allowed = true
		v.Reason = "primary checkout (slot 0) is unmanaged and unisolated; the guard constrains linked worktrees only"
		return v, nil
	}

	path, ok := req.FilePath()
	if !ok {
		v.Allowed = true
		v.Reason = "tool input carries no file_path; ambiguous shell constructs (pipes, $(...), variables, globs) are not parsed — fail open"
		return v, nil
	}
	if ambiguousPath(path) {
		v.Allowed = true
		v.Reason = fmt.Sprintf("file_path %q contains shell or glob metacharacters; ambiguous — fail open", path)
		return v, nil
	}
	resolved := resolveAgainst(cwd, path)

	if writeTools[req.ToolName] {
		if reason, ok := sharedStoreDenial(cls.WorktreeRoot, resolved); ok {
			v.Allowed = false
			v.Reason = reason
			return v, nil
		}
		if !Contains(cls.WorktreeRoot, resolved) {
			v.Allowed = false
			v.Reason = fmt.Sprintf(
				"write to %s is refused: it lies outside this worktree; the worktree root is %s",
				resolved, cls.WorktreeRoot)
			return v, nil
		}
		v.Allowed = true
		v.Reason = fmt.Sprintf("write to %s is inside this worktree", resolved)
		return v, nil
	}

	if req.ToolName == "Read" {
		if strings.EqualFold(filepath.Base(resolved), "CLAUDE.md") && !Contains(cls.WorktreeRoot, resolved) {
			v.Allowed = false
			v.Reason = fmt.Sprintf(
				"read of %s is refused: it is a CLAUDE.md outside this worktree and the parent copy is stale; the worktree root is %s",
				resolved, cls.WorktreeRoot)
			return v, nil
		}
		v.Allowed = true
		v.Reason = fmt.Sprintf("read of %s is inside this worktree", resolved)
		return v, nil
	}

	// Any other tool: fail open. The guard never assumes an unknown tool
	// mutates the file it names, so it cannot deny on its behalf.
	v.Allowed = true
	v.Reason = fmt.Sprintf("tool %q is not a file tool this guard checks — fail open", req.ToolName)
	return v, nil
}

// ambiguousPath reports whether a file_path carries shell or glob
// metacharacters. A real path can contain most of these; the guard's
// contract is to fail open on the ambiguous rather than risk a false denial.
func ambiguousPath(p string) bool {
	return strings.ContainsAny(p, "*?[]$`|;&<>")
}

// resolveAgainst absolutises the path under test against the classification
// cwd, so a relative file_path from a hook or a hand invocation lands where
// the caller meant.
func resolveAgainst(cwd, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		absCwd = cwd
	}
	return filepath.Join(absCwd, path)
}

// sharedStoreDenial returns the deny reason when a write targets a live
// shared store named in the worktree's descriptor's shared block — the
// "direct mutation of a live shared store" denial. It needs the spec to know
// the descriptor's filename, and the descriptor to know the block; when
// either is absent or unreadable the check fails open (an unmanaged tree has
// no live stores to protect).
func sharedStoreDenial(worktreeRoot, resolved string) (string, bool) {
	specPath, err := spec.FindSpecPath(worktreeRoot)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(specPath)
	if err != nil {
		return "", false
	}
	parsed, err := spec.Parse(data)
	if err != nil {
		return "", false
	}
	dpath := DescriptorPath(worktreeRoot, parsed.Emit.Descriptor)
	if _, err := os.Stat(dpath); err != nil {
		return "", false // no descriptor: nothing recorded as shared
	}
	d, err := descriptor.Read(dpath, parsed.Emit.Descriptor.Format)
	if err != nil {
		return "", false // unreadable descriptor: fail open
	}
	for _, sh := range d.Shared {
		if !filepath.IsAbs(sh.Name) {
			continue // a URL or a relative name is not a path the guard can place
		}
		if Contains(sh.Name, resolved) {
			return fmt.Sprintf(
				"write to %s is refused: %s is a shared resource named in this worktree's descriptor (impact: %s); a shared store is not mutated directly from a worktree",
				resolved, sh.Name, sh.Impact), true
		}
	}
	return "", false
}
