package review

import (
	"os/exec"
	"strings"
	"testing"
)

// deps runs `go list -deps` for pkg from the module root and returns the
// resulting import paths, one per line.
func deps(t *testing.T, pkg string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", pkg)
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	return strings.Fields(string(out))
}

// TestPackageBoundaries enforces CLAUDE.md's trust-boundary rule: review
// and forge never import runner, and runner never imports review or
// forge. internal/agent is the only package both runner and review may
// share.
func TestPackageBoundaries(t *testing.T) {
	for _, pkg := range []string{"./internal/review", "./internal/forge/..."} {
		for _, imp := range deps(t, pkg) {
			if imp == "github.com/jwbonnell/nightshift/internal/runner" {
				t.Errorf("%s depends on internal/runner, which must not happen", pkg)
			}
		}
	}

	for _, imp := range deps(t, "./internal/runner") {
		if imp == "github.com/jwbonnell/nightshift/internal/review" ||
			strings.HasPrefix(imp, "github.com/jwbonnell/nightshift/internal/forge") {
			t.Errorf("internal/runner depends on %s, which must not happen", imp)
		}
	}
}
