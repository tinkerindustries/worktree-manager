package treecheck

import (
	"errors"
	"strings"
	"testing"
)

// fakeRunner answers a canned (output, error) for any command.
func fakeRunner(out string, err error) Runner {
	return func(string, ...string) ([]byte, error) { return []byte(out), err }
}

// TestPRStateReadsTheState is the contract the three callers disagreed
// about: gh exits 0 and prints the PR for every state it knows, so the state
// in the JSON is the answer, never the exit code.
func TestPRStateReadsTheState(t *testing.T) {
	cases := []struct {
		json   string
		state  State
		open   bool
		merged bool
	}{
		{`{"number":42,"state":"OPEN"}`, StateOpen, true, false},
		{`{"number":42,"state":"DRAFT"}`, StateDraft, true, false},
		{`{"number":42,"state":"MERGED"}`, StateMerged, false, true},
		{`{"number":42,"state":"CLOSED"}`, StateClosed, false, false},
	}
	for _, c := range cases {
		t.Run(string(c.state), func(t *testing.T) {
			pr, err := PRState("/tree", fakeRunner(c.json, nil))
			if err != nil {
				t.Fatalf("PRState: %v", err)
			}
			if pr.State != c.state || pr.Number != 42 {
				t.Fatalf("PRState = %+v, want %s #42", pr, c.state)
			}
			if pr.Open() != c.open {
				t.Errorf("Open() = %v, want %v", pr.Open(), c.open)
			}
			if pr.Merged() != c.merged {
				t.Errorf("Merged() = %v, want %v", pr.Merged(), c.merged)
			}
		})
	}
}

// TestPRStateNoPullRequest: gh's "no pull requests found" is an answer, not
// a failure.
func TestPRStateNoPullRequest(t *testing.T) {
	pr, err := PRState("/tree", fakeRunner("no pull requests found for branch \"wt-1\"", errors.New("exit status 1")))
	if err != nil {
		t.Fatalf("PRState: %v", err)
	}
	if pr.State != StateNone {
		t.Fatalf("state = %s, want %s", pr.State, StateNone)
	}
	if pr.Open() || pr.Merged() {
		t.Errorf("an absent PR is neither open nor merged: %+v", pr)
	}
}

// TestPRStateUnavailable: gh missing and gh unauthenticated are distinct
// unavailable answers, because they have distinct remedies.
func TestPRStateUnavailable(t *testing.T) {
	_, err := PRState("/tree", fakeRunner("", ErrGhMissing))
	var ue *UnavailableError
	if !errors.As(err, &ue) || !ue.NotInstalled {
		t.Fatalf("err = %v, want a not-installed UnavailableError", err)
	}

	_, err = PRState("/tree", fakeRunner("gh auth login required", errors.New("exit status 4")))
	if !errors.As(err, &ue) || !ue.NotAuthenticated {
		t.Fatalf("err = %v, want a not-authenticated UnavailableError", err)
	}

	_, err = PRState("/tree", fakeRunner("could not determine the repository", errors.New("exit status 1")))
	if !errors.As(err, &ue) || ue.NotInstalled || ue.NotAuthenticated {
		t.Fatalf("err = %v, want a plain UnavailableError", err)
	}
}

// TestUnpushedNoUpstream: an absent upstream is its own answer, so callers
// can make it a stop rather than reading it as a passing check.
func TestUnpushedNoUpstream(t *testing.T) {
	for _, msg := range []string{
		"fatal: no upstream configured for branch 'wt-1'",
		"fatal: ambiguous argument '@{u}': unknown revision",
	} {
		_, err := Unpushed("/tree", fakeRunner(msg, errors.New("exit status 128")))
		if !errors.Is(err, ErrNoUpstream) {
			t.Errorf("Unpushed(%q) err = %v, want ErrNoUpstream", msg, err)
		}
	}

	_, err := Unpushed("/tree", fakeRunner("", errors.New("git is broken")))
	if errors.Is(err, ErrNoUpstream) {
		t.Errorf("a broken git must not read as an absent upstream: %v", err)
	}
}

// TestResultLines: the checks report the offending lines, and First bounds
// how many a message names.
func TestResultLines(t *testing.T) {
	res, err := Uncommitted("/tree", fakeRunner(" M a.go\n M b.go\n?? c.go\n", nil))
	if err != nil {
		t.Fatalf("Uncommitted: %v", err)
	}
	if res.OK() || len(res.Lines) != 3 {
		t.Fatalf("Result = %+v, want three lines", res)
	}
	if got := res.First(2); strings.Count(got, ";") != 1 {
		t.Errorf("First(2) = %q, want two lines", got)
	}

	res, err = Uncommitted("/tree", fakeRunner("", nil))
	if err != nil || !res.OK() {
		t.Fatalf("a clean tree: Result = %+v, err = %v", res, err)
	}
}

// TestOnBranch: a detached HEAD is not a branch, and a branch is what a pull
// request points at.
func TestOnBranch(t *testing.T) {
	if name, ok := OnBranch("/tree", fakeRunner("feature/x\n", nil)); !ok || name != "feature/x" {
		t.Errorf("OnBranch = (%q, %v), want (feature/x, true)", name, ok)
	}
	if _, ok := OnBranch("/tree", fakeRunner("HEAD\n", nil)); ok {
		t.Error("a detached HEAD reported as a branch")
	}
	if _, ok := OnBranch("/tree", fakeRunner("", errors.New("not a repository"))); ok {
		t.Error("a failed rev-parse reported as a branch")
	}
}

// TestAuthStatus: the gate distinguishes a missing binary from an
// unauthenticated one.
func TestAuthStatus(t *testing.T) {
	if err := AuthStatus(fakeRunner("Logged in to github.com", nil)); err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	var ue *UnavailableError
	if err := AuthStatus(fakeRunner("", ErrGhMissing)); !errors.As(err, &ue) || !ue.NotInstalled {
		t.Fatalf("err = %v, want a not-installed UnavailableError", err)
	}
	if err := AuthStatus(fakeRunner("not logged in", errors.New("exit status 1"))); !errors.As(err, &ue) || !ue.NotAuthenticated {
		t.Fatalf("err = %v, want a not-authenticated UnavailableError", err)
	}
}
