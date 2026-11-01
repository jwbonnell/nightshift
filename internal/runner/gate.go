package runner

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"

	"github.com/jwbonnell/nightshift/internal/home"
)

// gateWSDir is the throwaway gate workspace's parent; the clone lives in
// its repo/ subdirectory. gateLogDir holds each gate's output.
func (r *run) gateWSDir() string  { return filepath.Join(r.dir, "gate-ws") }
func (r *run) gateLogDir() string { return filepath.Join(r.dir, "gate") }

// buildGateWorkspace rebuilds ws and clones the source repo at ref into
// ws/repo (--no-local, detached, no remotes). Returns the clone dir.
func buildGateWorkspace(ws, repo, ref string) (string, error) {
	if err := os.RemoveAll(ws); err != nil {
		return "", err
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return "", err
	}
	dir, _, err := cloneAtRef(ws, "repo", repo, ref)
	return dir, err
}

// overlayHoldouts copies every file under holdDir into repoDir at the same
// relative path, never following a symlink on either side and never
// overwriting. Returns the number of files written. A missing holdDir is
// zero files.
func overlayHoldouts(holdDir, repoDir string) (int, error) {
	if _, err := os.Lstat(holdDir); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	err := filepath.WalkDir(holdDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(holdDir, path)
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("holdout %s is not a regular file", rel)
		}
		if err := overlayOne(path, repoDir, rel); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

func overlayOne(src, repoDir, rel string) error {
	parts := strings.Split(rel, string(filepath.Separator))
	cur := repoDir
	for i, p := range parts {
		cur = filepath.Join(cur, p)
		last := i == len(parts)-1
		fi, err := os.Lstat(cur)
		switch {
		case err == nil:
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("holdout path %s crosses a symlink", rel)
			}
			if last {
				return fmt.Errorf("holdout file %s already exists in the repo", rel)
			}
			if !fi.IsDir() {
				return fmt.Errorf("holdout path %s crosses a non-directory", rel)
			}
		case os.IsNotExist(err):
			if !last {
				if err := os.Mkdir(cur, 0o755); err != nil {
					return err
				}
			}
		default:
			return err
		}
	}
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(cur, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("holdout file %s already exists in the repo", rel)
		}
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// holdoutSlugDir is where the owner keeps this spec's holdout files.
func holdoutSlugDir(h, slug string) string {
	return filepath.Join(home.HoldoutDir(h), slug)
}

// countHoldoutFiles counts non-directory entries under the slug's holdout
// dir; a missing dir is zero.
func countHoldoutFiles(h, slug string) int {
	n := 0
	_ = filepath.WalkDir(holdoutSlugDir(h, slug), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// checkHoldoutOutsideRepo refuses a holdout dir that resolves inside the
// spec's repo, where the agent's clone would carry it.
func checkHoldoutOutsideRepo(h, slug, repo string) error {
	dir := holdoutSlugDir(h, slug)
	holdReal, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil // missing: nothing to protect
	}
	repoReal, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(repoReal, holdReal)
	if err != nil {
		return nil
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return fmt.Errorf("holdout dir %s must be outside the repo", dir)
	}
	return nil
}

const holdoutBullet = "- **holdout** hidden acceptance tests failed. Their details are withheld. Re-read the spec's Goal and Acceptance criteria and test the behaviour your own tests don't cover.\n"

// gateRes is one gate's outcome. out is held in memory only for the
// hand-back tail (accept) and is never logged.
type gateRes struct {
	name, cmd string
	ok        bool
	exit      int
	logPath   string
	out       string
}

// gateOutcome is the last completion attempt's gate result: pass, fail,
// error or skipped.
type gateOutcome struct {
	result string
	gates  []gateRes
	err    error
	reason string
}

func (g *gateOutcome) failed() bool {
	return g != nil && (g.result == "fail" || g.result == "error")
}

func (r *run) gateVerifier() BaseVerifier {
	if r.opts.GateVerifier != nil {
		return r.opts.GateVerifier
	}
	return sbxBaseVerifier{bin: r.opts.SbxBin, name: "nightshift-" + r.spec.Slug + "-gate"}
}

// sanitizeGateOutput makes gate output safe to hand to the agent: invalid
// UTF-8 and control characters other than tab and newline become "?".
func sanitizeGateOutput(s string) string {
	s = strings.ToValidUTF8(s, "?")
	return strings.Map(func(c rune) rune {
		if c != '\t' && c != '\n' && unicode.IsControl(c) {
			return '?'
		}
		return c
	}, s)
}

// fenced wraps body in a code fence longer than any backtick run in it.
func fenced(body string) string {
	longest, cur := 0, 0
	for _, c := range body {
		if c == '`' {
			cur++
			if cur > longest {
				longest = cur
			}
		} else {
			cur = 0
		}
	}
	f := strings.Repeat("`", max(3, longest+1))
	return f + "\n" + body + f + "\n"
}

// runGates checks an accepted completion against the owner's evidence in a
// throwaway workspace. It reports whether it handed the work back to the
// agent, and whether the run was interrupted.
func (r *run) runGates() (handedBack, interrupted bool) {
	sp, st := r.spec, r.st
	clock := r.opts.clockFn()
	holdN := countHoldoutFiles(r.opts.Home, sp.Slug)
	if sp.Accept == "" && holdN == 0 {
		return false, false
	}
	r.gate = nil
	if r.interrupted() {
		return false, true
	}
	windowClosed := func() bool { return !r.deadline.IsZero() && !clock().Before(r.deadline) }
	if windowClosed() {
		r.gate = &gateOutcome{result: "skipped", reason: "window closed"}
		r.logf("gate: skipped (window closed)")
		return false, false
	}

	out := &gateOutcome{}
	// An interrupt mid-gate is not an outcome: leave r.gate unset.
	defer func() {
		if interrupted {
			r.gate = nil
		}
	}()
	gateErr := func(err error) {
		out.result, out.err = "error", err
		r.gate = out
		_ = os.MkdirAll(r.gateLogDir(), 0o755)
		_ = os.WriteFile(filepath.Join(r.gateLogDir(), "error.log"), []byte(err.Error()+"\n"), 0o644)
		r.logf("gate: error (details in gate/error.log)")
	}
	if err := r.publishReviewRef(); err != nil {
		gateErr(err)
		return false, false
	}
	defer r.deleteReviewRef()
	defer os.RemoveAll(r.gateWSDir())
	if err := os.MkdirAll(r.gateLogDir(), 0o755); err != nil {
		gateErr(err)
		return false, false
	}

	ref := reviewRoundRef(sp.Slug)
	v := r.gateVerifier()
	type spec struct {
		name, cmd string
		overlay   bool
	}
	var todo []spec
	if holdN > 0 {
		todo = append(todo, spec{"holdout", sp.Verify, true})
	}
	if sp.Accept != "" {
		todo = append(todo, spec{"accept", sp.Accept, false})
	}
	for _, g := range todo {
		// Rebuilt per gate: accept runs in a holdout-free tree so its
		// output (shown to the agent) can't name holdout files.
		dir, err := buildGateWorkspace(r.gateWSDir(), sp.Repo, ref)
		if err != nil {
			gateErr(err)
			return false, r.interrupted()
		}
		if g.overlay {
			if _, err := overlayHoldouts(holdoutSlugDir(r.opts.Home, sp.Slug), dir); err != nil {
				gateErr(err)
				return false, false
			}
		}
		ctx, cancel := r.iterCtx(r.deadline, clock)
		o, verr := v.Verify(ctx, dir, sp.Egress, sp.Image, g.cmd)
		cancel()
		if r.interrupted() {
			return false, true
		}
		var setup verifierSetupError
		if errors.As(verr, &setup) {
			gateErr(verr)
			return false, false
		}
		res := gateRes{name: g.name, cmd: g.cmd, ok: verr == nil, out: o, logPath: filepath.Join(r.gateLogDir(), g.name+".log")}
		if verr != nil {
			res.exit = exitCodeOf(verr)
		}
		if err := os.WriteFile(res.logPath, []byte(o), 0o644); err != nil {
			gateErr(err)
			return false, false
		}
		out.gates = append(out.gates, res)
		if res.ok {
			r.logf("gate: %s pass", g.name)
		} else {
			r.logf("gate: %s fail (exit %d)", g.name, res.exit)
		}
	}

	var failing []gateRes
	for _, g := range out.gates {
		if !g.ok {
			failing = append(failing, g)
		}
	}
	if len(failing) == 0 {
		out.result = "pass"
		r.gate = out
		return false, false
	}
	out.result = "fail"
	r.gate = out
	if st.GateHandbacks >= sp.GateRounds || windowClosed() {
		return false, false
	}

	var b strings.Builder
	b.WriteString("# Acceptance gate — failed\n\nFix each failure below, note what you changed in PROGRESS.md, then touch .spec-complete again.\n\n")
	for _, g := range failing {
		if g.name == "holdout" {
			b.WriteString(holdoutBullet)
			continue
		}
		fmt.Fprintf(&b, "- **accept** failed (exit %d): %s\n\n", g.exit, sanitizeGateOutput(g.cmd))
		b.WriteString(fenced(tailLines(sanitizeGateOutput(g.out), 40)))
		b.WriteString("\n")
	}
	st.GateHandbacks++
	// A gate result is only reported on a run that ends complete.
	r.gate = nil
	r.handOff(b.String(), fmt.Sprintf("acceptance gate: %d failing gate(s) in REVIEW.md (hand-back %d of %d) — fix them, then touch .spec-complete again", len(failing), st.GateHandbacks, sp.GateRounds))
	r.logf("gate: %d failing, handed back (%d of %d)", len(failing), st.GateHandbacks, sp.GateRounds)
	_ = r.saveState()
	return true, false
}

// writeGateSection adds the result note's "## Acceptance gate" section.
// The note is for the owner, so holdout output is included.
func (r *run) writeGateSection(b *strings.Builder) {
	g := r.gate
	if g == nil {
		return
	}
	fmt.Fprintf(b, "## Acceptance gate\n\nresult: %s\n\n", g.result)
	switch g.result {
	case "skipped":
		fmt.Fprintf(b, "skipped (%s)\n\n", g.reason)
		return
	case "error":
		fmt.Fprintf(b, "gate error: %v\n\n", g.err)
	}
	for _, x := range g.gates {
		if x.ok {
			fmt.Fprintf(b, "- %s: pass\n", x.name)
		} else {
			fmt.Fprintf(b, "- %s: fail (exit %d) — %s\n", x.name, x.exit, x.logPath)
		}
	}
	b.WriteString("\n")
	for _, x := range g.gates {
		if !x.ok {
			fmt.Fprintf(b, "### %s output (last 80 lines)\n\n%s\n", x.name, fenced(tailLines(sanitizeGateOutput(x.out), 80)))
		}
	}
}
