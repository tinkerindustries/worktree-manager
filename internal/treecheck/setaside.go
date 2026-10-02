package treecheck

// setaside.go is the fast half of WorktreeRemove. `git worktree remove`
// deletes a tree one file at a time, and on Windows that is the whole cost
// of removing a worktree with dependencies installed in it: a 57,000-file
// node_modules took git 34s, where sixteen concurrent deletions of the same
// tree took 10.5s. The ignored files are the bulk of any such tree, and git
// deletes them without asking, so they are moved out of git's way before it
// runs and deleted in parallel after it succeeds.
//
// A move, not a delete, because git can still refuse — an untracked file,
// a locked worktree, a submodule — and a refused removal has to leave the
// tree exactly as it was. Every entry moved aside is moved back when git
// refuses. Every step here that fails leaves its entry where it was, which
// is the pre-existing behaviour: git deletes it or refuses over it.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// removeWorkers is how many deletions run at once. The cost on Windows is
// per-file latency, not bandwidth, so concurrency is what pays; sixteen
// was the measured knee on the machine that prompted this.
const removeWorkers = 16

// removeFanout is how many independent subtrees removeAllParallel tries to
// hand its workers, so that one large directory is not one worker's job.
const removeFanout = 256

// setAside records the ignored entries moved out of a tree and where they
// went. The trash directory is a sibling of the tree, so every move is a
// rename within one volume, and its name says whose it was if a failed
// deletion leaves it behind.
type setAside struct {
	trash string
	moved []movedEntry
}

type movedEntry struct{ from, to string }

// setAsideIgnored moves every ignored entry of the tree into a new trash
// directory beside it. It returns nil when there is nothing to move or
// when the trash directory cannot be made; either way the removal proceeds
// as it always did.
func setAsideIgnored(dir string, git Runner) *setAside {
	out, err := git(dir, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return nil
	}
	var rels []string
	for _, p := range strings.Split(string(out), "\x00") {
		p = filepath.FromSlash(strings.TrimSuffix(p, "/"))
		// git only ever names paths inside the tree; IsLocal is the rail
		// that keeps a surprising answer from moving anything outside it.
		if p != "" && filepath.IsLocal(p) {
			rels = append(rels, p)
		}
	}
	if len(rels) == 0 {
		return nil
	}
	trash, err := os.MkdirTemp(filepath.Dir(dir), ".wt-trash-"+filepath.Base(dir)+"-")
	if err != nil {
		return nil
	}
	s := &setAside{trash: trash}
	for i, rel := range rels {
		from := filepath.Join(dir, rel)
		to := filepath.Join(trash, strconv.Itoa(i))
		// A rename fails on Windows when a process holds a file open
		// inside the entry. The entry stays, and git meets it as before.
		if os.Rename(from, to) == nil {
			s.moved = append(s.moved, movedEntry{from: from, to: to})
		}
	}
	return s
}

// restore moves every entry back to where it was and removes the empty
// trash directory. The error names every entry it could not put back and
// where each one is, because a refused removal is supposed to have changed
// nothing.
func (s *setAside) restore() error {
	if s == nil {
		return nil
	}
	var stranded []string
	for i := len(s.moved) - 1; i >= 0; i-- {
		m := s.moved[i]
		if err := os.Rename(m.to, m.from); err != nil {
			stranded = append(stranded, fmt.Sprintf("%s is at %s", m.from, m.to))
		}
	}
	if len(stranded) > 0 {
		return fmt.Errorf("could not move ignored files back into the tree: %s", strings.Join(stranded, "; "))
	}
	if err := os.Remove(s.trash); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("could not remove the empty directory %s: %w", s.trash, err)
	}
	return nil
}

// discard deletes the trash directory. It answers the directory it left
// behind, with the reason, or "" when it is gone.
func (s *setAside) discard() string {
	if s == nil {
		return ""
	}
	if err := removeAllParallel(s.trash); err != nil {
		return fmt.Sprintf("%s (%v)", s.trash, err)
	}
	return ""
}

// removeAllParallel deletes root and everything under it, splitting the
// tree into subtrees for concurrent deletion. Only real directories are
// descended into: a symlink or a Windows junction is a leaf handed to
// os.RemoveAll, which removes the link and never the directory it points
// at — pnpm and npm workspaces put links into node_modules that point
// outside it.
//
// The worker errors are not the answer. The final os.RemoveAll of root
// retries whatever they left, and its error is what remains.
func removeAllParallel(root string) error {
	jobs := make(chan string)
	var wg sync.WaitGroup
	for range removeWorkers {
		wg.Go(func() {
			for p := range jobs {
				_ = os.RemoveAll(p)
			}
		})
	}
	for _, p := range fanOut(root) {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	return os.RemoveAll(root)
}

// fanOut expands root breadth-first into the entries below it until there
// are removeFanout of them or four levels have been opened. Expanded
// directories are left out of the answer; their emptied shells go with
// the final removal of root.
func fanOut(root string) []string {
	frontier := []string{root}
	for depth := 0; depth < 4 && len(frontier) < removeFanout; depth++ {
		var next []string
		expanded := false
		for _, p := range frontier {
			entries, ok := readRealDir(p)
			if !ok {
				next = append(next, p)
				continue
			}
			expanded = true
			for _, e := range entries {
				next = append(next, filepath.Join(p, e.Name()))
			}
		}
		if !expanded {
			break
		}
		frontier = next
	}
	return frontier
}

// readRealDir lists p when p is a directory and not a link to one. A
// Windows junction reports extra type bits beside ModeDir, so the test is
// equality, not IsDir.
func readRealDir(p string) ([]os.DirEntry, bool) {
	fi, err := os.Lstat(p)
	if err != nil || fi.Mode().Type() != fs.ModeDir {
		return nil, false
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, false
	}
	return entries, true
}
