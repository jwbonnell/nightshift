package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jwbonnell/nightshift/internal/gitx"
)

// reviewDiffMax caps the diff.patch written into a review workspace;
// longer diffs are truncated with a pointer to read repo/ directly.
const reviewDiffMax = 200 << 10

// reviewWSDir is where a review's workspace lives: rebuilt from scratch
// for every review and removed afterwards, whatever the outcome.
func reviewWSDir(stateRoot, slug string) string {
	return filepath.Join(stateRoot, slug, "review-ws")
}

// buildReviewWorkspace rebuilds ws as a review sandbox mount: a
// --no-local clone of the source repo, checked out detached with no
// remotes at ref's resolved commit, plus the review prompt's inputs/. It
// never touches the agent's clone — repo is always the source repo,
// cloned with --no-local so the review sandbox never shares object-file
// inodes with it. Returns the resolved head commit.
func buildReviewWorkspace(ws, repo, base, ref, specPath, progress, summary string) (string, error) {
	if err := os.RemoveAll(ws); err != nil {
		return "", err
	}
	inputs := filepath.Join(ws, "inputs")
	if err := os.MkdirAll(inputs, 0o755); err != nil {
		return "", err
	}

	head, err := gitx.Run(repo, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve %s: %v: %s", ref, err, head)
	}
	if err := validSHA("git rev-parse --verify "+ref+"^{commit}", head); err != nil {
		return "", err
	}

	repoDir := filepath.Join(ws, "repo")
	if out, err := gitx.Run(ws, "clone", "--no-local", repo, "repo"); err != nil {
		return "", fmt.Errorf("clone %s: %v: %s", repo, err, out)
	}
	// git clone --no-local only copies refs/heads and tags: when ref is
	// the pending refs/nightshift/<slug> ref, head isn't reachable from
	// any branch and needs an explicit fetch, same as cloneRepo's base.
	if _, err := gitx.Run(repoDir, "cat-file", "-e", head+"^{commit}"); err != nil {
		if out, ferr := gitx.Run(repoDir, "fetch", "origin", head); ferr != nil {
			return "", fmt.Errorf("fetch %s from %s: %v: %s", head, repo, ferr, out)
		}
	}
	if out, err := gitx.Run(repoDir, "checkout", "--detach", head); err != nil {
		return "", fmt.Errorf("checkout %s: %v: %s", head, err, out)
	}
	if out, err := gitx.Run(repoDir, "remote", "remove", "origin"); err != nil {
		return "", fmt.Errorf("remove remote: %v: %s", err, out)
	}

	specText := "(spec file not found)"
	if specPath != "" {
		if data, err := os.ReadFile(specPath); err == nil {
			specText = string(data)
		}
	}
	if err := os.WriteFile(filepath.Join(inputs, "spec.md"), []byte(specText), 0o644); err != nil {
		return "", err
	}

	progressText := progress
	if progressText == "" {
		progressText = "(no PROGRESS.md)"
	}
	if err := os.WriteFile(filepath.Join(inputs, "progress.md"), []byte(progressText), 0o644); err != nil {
		return "", err
	}

	if err := os.WriteFile(filepath.Join(inputs, "summary.md"), []byte(summary), 0o644); err != nil {
		return "", err
	}

	// gitx.Run trims its output, which would strip diffstat's leading
	// space and the patch's trailing newline — shell out directly so
	// stdout reaches disk untouched.
	diffstat, err := exec.Command("git", "-C", repo, "diff", "--stat", base+".."+head).Output()
	if err != nil {
		return "", fmt.Errorf("diffstat %s..%s: %w", base, head, err)
	}
	if err := os.WriteFile(filepath.Join(inputs, "diffstat.txt"), diffstat, 0o644); err != nil {
		return "", err
	}

	patch, err := exec.Command("git", "-C", repo, "diff", base+".."+head).Output()
	if err != nil {
		return "", fmt.Errorf("diff %s..%s: %w", base, head, err)
	}
	if len(patch) > reviewDiffMax {
		truncated := patch[:reviewDiffMax]
		if len(truncated) == 0 || truncated[len(truncated)-1] != '\n' {
			truncated = append(truncated, '\n')
		}
		truncated = append(truncated, []byte("[diff truncated at 200 KiB — read the changed files under repo/ directly]\n")...)
		patch = truncated
	}
	if err := os.WriteFile(filepath.Join(inputs, "diff.patch"), patch, 0o644); err != nil {
		return "", err
	}

	return head, nil
}

// reviewSummary formats the run-summary text written to inputs/summary.md.
func reviewSummary(status, iterations, commits, baseShort, headShort, branch string) string {
	return fmt.Sprintf(
		"- status: %s\n- iterations: %s\n- commits: %s\n- base: %s\n- head: %s\n- branch: %s\n",
		status, iterations, commits, baseShort, headShort, branch,
	)
}
