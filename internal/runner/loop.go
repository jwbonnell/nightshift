package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// validSHA reports an error if s does not look like a full commit id.
func validSHA(what, s string) error {
	if !fullSHA.MatchString(s) {
		return fmt.Errorf("%s returned %q, which is not a commit id — refusing to record it", what, s)
	}
	return nil
}

// fail records err as the run's failure and returns StatusError.
func (r *run) fail(err error) Status {
	r.err = err
	r.logf("error: %v", err)
	return StatusError
}

// appendProgress appends one dated line to PROGRESS.md, stamped with the
// live wall clock at write time (D11.9) rather than the run's frozen
// start time.
func (r *run) appendProgress(line string) {
	r.scaffoldAppend(r.progressPath(), fmt.Sprintf("\n- %s %s\n", r.opts.clockFn()().Format("2006-01-02 15:04"), line))
}

// readFile returns path's contents, or "" on any error.
func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// tailLines keeps the last n lines of s (after trimming any trailing
// newlines), always ending in exactly one newline.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

// parseUntil parses "HH:MM" (24h, local to ref) into the next occurrence
// of that time at or after ref. An empty or unparsable string yields the
// zero time.
func parseUntil(hhmm string, ref time.Time) time.Time {
	hhmm = strings.TrimSpace(hhmm)
	if hhmm == "" {
		return time.Time{}
	}
	t, err := time.ParseInLocation("15:04", hhmm, ref.Location())
	if err != nil {
		return time.Time{}
	}
	d := time.Date(ref.Year(), ref.Month(), ref.Day(), t.Hour(), t.Minute(), 0, 0, ref.Location())
	if !d.After(ref) {
		d = d.Add(24 * time.Hour)
	}
	return d
}

// iterCtx bounds one iteration's agent or verify step: the spec's
// iter_timeout, clamped to whatever remains of the --until window.
func (r *run) iterCtx(deadline time.Time, clock func() time.Time) (context.Context, context.CancelFunc) {
	d := r.spec.IterTimeout
	if !deadline.IsZero() {
		if remaining := deadline.Sub(clock()); remaining < d {
			d = remaining
		}
	}
	if d <= 0 {
		d = time.Millisecond
	}
	// D8: derive from Options.Ctx so a signal-driven cancellation cuts the
	// current agent or verify call short immediately, not just at the
	// next checkpoint.
	base := r.opts.Ctx
	if base == nil {
		base = context.Background()
	}
	return context.WithTimeout(base, d)
}

func (r *run) headSHA() (string, error) {
	out, err := r.git("rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	sha := strings.TrimSpace(out)
	if err := validSHA("git rev-parse HEAD", sha); err != nil {
		return "", err
	}
	return sha, nil
}

func (r *run) commitCount() (int, error) {
	out, err := r.git("rev-list", "--count", r.st.Base+"..HEAD")
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count: %w", err)
	}
	trimmed := strings.TrimSpace(out)
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count returned %q, which is not an integer — refusing to record it", trimmed)
	}
	return n, nil
}

// captureDirty saves any uncommitted changes as attempts/iter-<iter>.dirty.patch,
// then always resets the tree hard (clean -fd preserves excluded scaffold
// files).
func (r *run) captureDirty(iter int) {
	status, _ := r.git("status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		_, _ = r.git("add", "-A")
		diff, _ := r.git("diff", "--cached")
		if strings.TrimSpace(diff) != "" {
			path := filepath.Join(r.attemptsDir(), fmt.Sprintf("iter-%d.dirty.patch", iter))
			_ = os.WriteFile(path, []byte(diff+"\n"), 0o644)
		}
	}
	r.resetHard()
}

func (r *run) resetHard() {
	_, _ = r.git("reset", "--hard", "HEAD")
	_, _ = r.git("clean", "-fd")
}

func (r *run) resetTo(sha string) error {
	if err := validSHA("resetTo", sha); err != nil {
		return err
	}
	if out, err := r.git("reset", "--hard", sha); err != nil {
		return fmt.Errorf("git reset --hard %s: %v: %s", short(sha), err, out)
	}
	if out, err := r.git("clean", "-fd"); err != nil {
		return fmt.Errorf("git clean -fd: %v: %s", err, out)
	}
	return nil
}

func (r *run) saveRed(iter int, verifyOutput string) {
	diff, _ := r.git("diff", r.st.LastGreen+"..HEAD")
	_ = os.WriteFile(filepath.Join(r.attemptsDir(), fmt.Sprintf("iter-%d.patch", iter)), []byte(diff+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(r.attemptsDir(), fmt.Sprintf("iter-%d.log", iter)), []byte(tailLines(verifyOutput, 200)), 0o644)
}

// writeAgentHeader writes one header line to agent.log before an
// iteration's raw agent stream, per D5.
func (r *run) writeAgentHeader(iter, session int, model string, planning bool, clock func() time.Time) {
	if r.agentLog == nil {
		return
	}
	fmt.Fprintf(r.agentLog, "=== iter %d (session %d) model=%s planning=%v %s ===\n",
		iter, session, model, planning, clock().Format(time.RFC3339))
}

// loop drives the Ralph loop for one invocation, returning a terminal
// status. It is reference §3.5.8's loop(), with D11.1 (a planning
// iteration that commits resets to last_green, not base) and D11.9
// (PROGRESS.md/history use the live wall clock, applied in
// appendProgress above and by the caller for history entries).
func (r *run) loop() Status {
	sp := r.spec
	st := r.st
	clock := r.opts.clockFn()

	needPlan := st.SpecHash != sp.ContentHash || !st.Planned
	deadline := parseUntil(r.opts.Until, clock())
	r.deadline = deadline

	redStreak, noProgress, session := 0, 0, 0

	for session < sp.MaxIter {
		if r.interrupted() { // D8: checked before each iteration
			return StatusInterrupted
		}
		if !deadline.IsZero() && !clock().Before(deadline) {
			r.logf("window closed before iteration")
			return StatusWindowClosed
		}
		session++
		st.Iterations++
		planning := needPlan && session == 1
		model := sp.Model
		if planning {
			model = sp.Planner
		}

		progressBefore := r.scaffoldRead(r.progressPath())
		commitsBefore, err := r.commitCount()
		if err != nil {
			return r.fail(err)
		}

		ctx, cancel := r.iterCtx(deadline, clock)
		r.logf("iter %d (session %d) model=%s planning=%v", st.Iterations, session, model, planning)
		r.writeAgentHeader(st.Iterations, session, model, planning, clock)
		agentErr := r.sb.RunAgent(ctx, model, buildPrompt(sp, planning), r.agentLog)
		cancel()
		switch {
		case agentErr == context.DeadlineExceeded:
			r.logf("iter %d timed out after %s", st.Iterations, sp.IterTimeout)
		case agentErr != nil:
			r.logf("iter %d agent exited: %v", st.Iterations, agentErr)
		}
		if stopErr := r.sb.StopAgent(); stopErr != nil {
			r.logf("iter %d stop agent: %v", st.Iterations, stopErr)
		}

		if r.interrupted() { // D8: the agent/verify ctx was just cut short by a signal
			r.captureDirty(st.Iterations)
			_ = r.saveState()
			return StatusInterrupted
		}
		if !deadline.IsZero() && !clock().Before(deadline) {
			r.captureDirty(st.Iterations)
			_ = r.saveState()
			return StatusWindowClosed
		}
		r.captureDirty(st.Iterations)

		if planning {
			commitsNow, err := r.commitCount()
			if err != nil {
				return r.fail(err)
			}
			if commitsNow > commitsBefore {
				r.logf("planner committed — resetting to last green, logging violation")
				if err := r.resetTo(st.LastGreen); err != nil { // D11.1: last_green, not base
					return r.fail(err)
				}
				r.appendProgress("Runner: planning iteration committed code; reset to last green. The planner must only write PROGRESS.md.")
			}
			st.Planned = true
			_ = r.saveState()
			if r.scaffoldExists(r.questionPath()) {
				return StatusBlocked
			}
			continue
		}

		if err := r.sb.ResetVerify(); err != nil {
			r.logf("iter %d reset verify sandbox: %v", st.Iterations, err)
		}
		vctx, vcancel := r.iterCtx(deadline, clock)
		vout, verr := r.sb.RunVerify(vctx, sp.Verify)
		vcancel()
		green := verr == nil

		if green {
			sha, err := r.headSHA()
			if err != nil {
				return r.fail(err)
			}
			st.LastGreen = sha
			r.logf("iter %d verify GREEN at %s", st.Iterations, short(st.LastGreen))
			if err := r.syncBranchBack(); err != nil {
				r.logf("iter %d sync to %s failed (will retry after the next green iteration): %v", st.Iterations, sp.Repo, err)
				st.SyncOwed = true
			} else {
				st.SyncOwed = false
			}
			redStreak = 0
		} else {
			r.logf("iter %d verify RED (streak %d)", st.Iterations, redStreak+1)
			r.saveRed(st.Iterations, vout)
			if err := r.resetTo(st.LastGreen); err != nil {
				return r.fail(err)
			}
			r.appendProgress(fmt.Sprintf("Runner: iteration %d verify FAILED and was reset to last green %s. See attempts/iter-%d.patch and attempts/iter-%d.log.",
				st.Iterations, short(st.LastGreen), st.Iterations, st.Iterations))
			redStreak++
		}

		if r.scaffoldExists(r.completePath()) {
			commits, err := r.commitCount()
			if err != nil {
				return r.fail(err)
			}
			if green && commits >= 1 {
				st.Commits = commits
				_ = r.saveState()
				return StatusComplete
			}
			r.logf("rejected .spec-complete (green=%v commits=%d)", green, commits)
			r.scaffoldRemove(r.completePath())
			r.appendProgress("Runner: rejected .spec-complete — verify was not green or there were no commits since base.")
		}

		if !green && redStreak >= 2 {
			commits, err := r.commitCount()
			if err != nil {
				return r.fail(err)
			}
			st.Commits = commits
			_ = r.saveState()
			return StatusRed
		}

		if r.scaffoldExists(r.questionPath()) {
			commits, err := r.commitCount()
			if err != nil {
				return r.fail(err)
			}
			st.Commits = commits
			_ = r.saveState()
			return StatusBlocked
		}

		progressAfter := r.scaffoldRead(r.progressPath())
		commitsAfter, err := r.commitCount()
		if err != nil {
			return r.fail(err)
		}
		progressed := (green && commitsAfter > commitsBefore) || progressAfter != progressBefore
		if progressed {
			noProgress = 0
		} else {
			noProgress++
		}
		st.Commits = commitsAfter
		_ = r.saveState()
		if noProgress >= 2 {
			return StatusStalled
		}
	}

	commits, err := r.commitCount()
	if err != nil {
		return r.fail(err)
	}
	st.Commits = commits
	_ = r.saveState()
	return StatusCapped
}
