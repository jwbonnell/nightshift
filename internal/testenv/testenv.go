// Package testenv holds test-only helpers shared across nightshift's test
// packages, kept separate so nothing in the production binary imports it.
package testenv

import "os"

// HermeticGit points every git invocation a test makes at /dev/null for
// global/system config and supplies a commit identity via env, so tests
// never depend on the host's ~/.gitconfig (which is what let tests pass
// here while still failing inside a real sbx verify sandbox's clean HOME).
// Call it once from each affected package's TestMain, before m.Run(): a
// process-wide os.Setenv is safe even if tests later add t.Parallel(),
// unlike t.Setenv.
func HermeticGit() {
	os.Setenv("GIT_AUTHOR_NAME", "nightshift-test")
	os.Setenv("GIT_AUTHOR_EMAIL", "nightshift-test@localhost")
	os.Setenv("GIT_COMMITTER_NAME", "nightshift-test")
	os.Setenv("GIT_COMMITTER_EMAIL", "nightshift-test@localhost")
	os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}
