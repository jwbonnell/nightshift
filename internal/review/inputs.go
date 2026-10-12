package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// reviewDirName is where the computed diff/commits land inside the
// exported tree — P1 renames the reference's .mr-review/ to this.
const reviewDirName = ".pr-review"

// reviewInputs is everything prepareReviewDir computes for one change:
// the full diff/commit log (used to build the prompt and written to
// disk) and the changed-line count (used for the size cap).
type reviewInputs struct {
	Diff, Commits, Stat string
	ChangedLines        int
}

// prepareReviewDir replaces any change-supplied .pr-review/ in the
// exported tree with the real diff and commit log, so a change can never
// ship a fake one to mislead the reviewer.
func prepareReviewDir(ctx context.Context, g gitEnv, repo, base, head, treeDir string) (reviewInputs, error) {
	dir := filepath.Join(treeDir, reviewDirName)
	if err := os.RemoveAll(dir); err != nil {
		return reviewInputs{}, fmt.Errorf("remove existing %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return reviewInputs{}, fmt.Errorf("create %s: %w", dir, err)
	}

	diff, err := changesDiff(ctx, g, repo, base, head)
	if err != nil {
		return reviewInputs{}, err
	}
	commits, err := commitLog(ctx, g, repo, base, head)
	if err != nil {
		return reviewInputs{}, err
	}
	stat, err := diffStatText(ctx, g, repo, base, head)
	if err != nil {
		return reviewInputs{}, err
	}
	numstat, err := numstatText(ctx, g, repo, base, head)
	if err != nil {
		return reviewInputs{}, err
	}

	if err := os.WriteFile(filepath.Join(dir, "changes.diff"), []byte(strings.TrimSpace(diff)), 0o600); err != nil {
		return reviewInputs{}, fmt.Errorf("write changes.diff: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "commits.txt"), []byte(strings.TrimSpace(commits)), 0o600); err != nil {
		return reviewInputs{}, fmt.Errorf("write commits.txt: %w", err)
	}

	return reviewInputs{
		Diff:         diff,
		Commits:      commits,
		Stat:         stat,
		ChangedLines: countChangedLines(numstat),
	}, nil
}

// countChangedLines sums added+deleted lines from `git diff --numstat`
// output, skipping binary entries ("-\t-\t<path>").
func countChangedLines(numstat string) int {
	total := 0
	for _, line := range strings.Split(numstat, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 {
			continue
		}
		added, errA := strconv.Atoi(fields[0])
		deleted, errD := strconv.Atoi(fields[1])
		if errA != nil || errD != nil {
			continue
		}
		total += added + deleted
	}
	return total
}
