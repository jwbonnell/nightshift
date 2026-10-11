package agent

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// importsOf parses every non-test .go file in dir with ImportsOnly and
// returns the raw import paths. Hermetic and fast: no go/build, no
// network, no module resolution.
func importsOf(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var imports []string
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			imports = append(imports, strings.Trim(imp.Path.Value, `"`))
		}
	}
	return imports
}

// TestPackageBoundaries enforces the trust/import rules CLAUDE.md
// documents: internal/agent imports only the standard library (so it can
// never pull in sbx, git or notification code), and internal/runner and
// internal/review (if present) never import each other.
func TestPackageBoundaries(t *testing.T) {
	for _, imp := range importsOf(t, ".") {
		if strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") {
			t.Errorf("internal/agent imports %q, want std-lib only", imp)
		}
	}

	runnerDir := filepath.Join("..", "runner")
	reviewDir := filepath.Join("..", "review")
	if _, err := os.Stat(runnerDir); err == nil {
		for _, imp := range importsOf(t, runnerDir) {
			if imp == "github.com/jwbonnell/nightshift/internal/review" {
				t.Errorf("internal/runner imports internal/review, which must not happen")
			}
		}
	}
	if _, err := os.Stat(reviewDir); err == nil {
		for _, imp := range importsOf(t, reviewDir) {
			if imp == "github.com/jwbonnell/nightshift/internal/runner" {
				t.Errorf("internal/review imports internal/runner, which must not happen")
			}
		}
	}
}
