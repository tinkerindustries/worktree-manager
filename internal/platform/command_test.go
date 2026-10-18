package platform

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoBareExecCommand holds the line command.go draws: no non-test file
// in the module builds a child with os/exec's own constructors, or an
// exec.Cmd literal, outside command.go. A bare one is a console window on
// a Windows wtd. The scan reads the syntax tree, so a constructor quoted
// inside a string — the generated reader's template — is not a call.
func TestNoBareExecCommand(t *testing.T) {
	root := filepath.Join("..", "..")
	self := filepath.Join(root, "internal", "platform", "command.go")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// testdata holds fixture repositories, and the dot
			// directories hold other worktrees of this one.
			if path != root && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == self {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		name := ""
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "os/exec" {
				name = "exec"
				if imp.Name != nil {
					name = imp.Name.Name
				}
			}
		}
		if name == "" {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == name {
				switch sel.Sel.Name {
				case "Command", "CommandContext":
					t.Errorf("%s: %s.%s: build the child with platform.%s, which keeps a Windows wtd's children from opening a console window",
						fset.Position(sel.Pos()), name, sel.Sel.Name, sel.Sel.Name)
				}
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if sel, ok := lit.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Cmd" {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == name {
					t.Errorf("%s: %s.Cmd literal: build the child with platform.Command", fset.Position(lit.Pos()), name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestCommandPipes proves the console rule leaves a child's pipes alone:
// stdin reaches it and stdout comes back, whichever way hideConsole goes
// for the test process. The child is this test binary, so nothing beyond
// it needs to be installed.
func TestCommandPipes(t *testing.T) {
	if os.Getenv("WT_COMMAND_TEST_CHILD") == "1" {
		data, _ := io.ReadAll(os.Stdin)
		os.Stdout.WriteString("echo:" + string(data))
		os.Exit(0)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := Command(exe, "-test.run=^TestCommandPipes$")
	cmd.Env = append(os.Environ(), "WT_COMMAND_TEST_CHILD=1")
	cmd.Stdin = strings.NewReader("hello")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	if got := string(out); got != "echo:hello" {
		t.Fatalf("child output = %q, want %q", got, "echo:hello")
	}
}
