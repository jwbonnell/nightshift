package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
)

// LintOptions configures Lint. SpecArg is resolved as a path if it
// exists, else looked up by slug in specs/draft/ then specs/ready/ — no
// readiness gate, unlike run.
type LintOptions struct {
	Home      string
	SpecArg   string
	NoVerify  bool
	Verifier  BaseVerifier // nil = sbx implementation
	SbxBin    string       // "" = env > config > "sbx"
	ClaudeBin string       // "" = env > config > "claude"
	Stdout    io.Writer
	Ctx       context.Context
}

// LintResult tallies FAIL and warn lines across every check.
type LintResult struct{ Fails, Warns int }

// BaseVerifier runs a spec's verify command, mounting dir, in a fresh
// verify sandbox and returns its combined output.
type BaseVerifier interface {
	Verify(ctx context.Context, dir string, egress []string, image, cmd string) (string, error)
}

// allowedFrontmatterKeys are the keys lint does not warn about (check 1).
var allowedFrontmatterKeys = map[string]bool{
	"type": true, "project": true, "status": true, "created": true,
	"repo": true, "verify": true, "model": true, "planner_model": true,
	"max_iter": true, "extend_iter": true, "iter_timeout": true, "base": true, "egress": true,
	"review": true, "review_model": true, "review_rounds": true,
	"depends_on": true, "owns": true, "image": true,
}

// requiredSections is the check-2 FAIL list, in spec order.
var requiredSections = []string{"Goal", "Context", "Tasks", "Acceptance criteria", "Out of scope"}

var (
	emptyCheckboxRe = regexp.MustCompile(`^\s*- \[ \]\s*$`)
	bareBulletRe    = regexp.MustCompile(`^\s*-\s*$`)
	checkboxItemRe  = regexp.MustCompile(`^\s*- \[[ xX]\]`)
	fenceOpenRe     = regexp.MustCompile("^\\s*(`{3,}|~{3,})")
)

const titlePlaceholder = "<short imperative title>"

// lintLine writes one "<label><text>\n" line, label left-padded to 6
// columns as the design's "ok    "/"warn  "/"FAIL  "/"skip  " show.
func lintLine(w io.Writer, label, text string) {
	fmt.Fprintf(w, "%-6s%s\n", label, text)
}

// firstLine returns the first line of out (gitx.Run's combined
// stdout+stderr), falling back to err's own message when out is empty.
func firstLine(out string, err error) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return err.Error()
	}
	return strings.SplitN(out, "\n", 2)[0]
}

// locateLintSpec resolves arg to a spec path: arg itself if it names a
// regular file, else <draft>/<arg>.md, else <ready>/<arg>.md — both
// matched case-insensitively. Unlike locateSpec, it applies no readiness
// gate: lint must be able to check a draft.
func locateLintSpec(homeDir, arg string) (string, error) {
	if arg == "" {
		return "", fmt.Errorf("no spec given")
	}
	if st, err := os.Stat(arg); err == nil && !st.IsDir() {
		return arg, nil
	}
	if p, ok := home.FindFold(home.DraftDir(homeDir), arg+".md"); ok {
		return p, nil
	}
	if p, ok := home.FindFold(home.ReadyDir(homeDir), arg+".md"); ok {
		return p, nil
	}
	return "", fmt.Errorf("no spec named %s in specs/draft/ or specs/ready/", arg)
}

// specBody returns everything in raw after the closing frontmatter "---"
// line, matching ParseFrontmatter's own delimiter search exactly so the
// two agree on where the body starts. A file with no frontmatter block is
// returned whole.
func specBody(raw []byte) string {
	s := string(raw)
	if !strings.HasPrefix(s, "---\n") {
		return s
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return s
	}
	return rest[end+4:]
}

// placeholderLines returns the 1-based file line numbers of check-3
// violations: an empty checkbox or bare bullet anywhere in the body, or
// the title placeholder on an H1 heading line. Frontmatter and the inside
// of fenced code blocks (opened by a line of 3+ backticks or tildes,
// closed by a later line starting with at least as many of the same
// character, or never closed before EOF) are skipped entirely.
func placeholderLines(raw []byte) []int {
	body := specBody(raw)
	offset := strings.Count(string(raw[:len(raw)-len(body)]), "\n")

	var result []int
	inFence := false
	fenceMarker := ""
	for i, line := range strings.Split(body, "\n") {
		if inFence {
			if strings.HasPrefix(strings.TrimLeft(line, " \t"), fenceMarker) {
				inFence = false
			}
			continue
		}
		if m := fenceOpenRe.FindStringSubmatch(line); m != nil {
			inFence = true
			fenceMarker = m[1]
			continue
		}
		fileLine := offset + i + 1
		switch {
		case strings.HasPrefix(line, "# ") && strings.Contains(line, titlePlaceholder):
			result = append(result, fileLine)
		case emptyCheckboxRe.MatchString(line) || bareBulletRe.MatchString(line):
			result = append(result, fileLine)
		}
	}
	return result
}

// parseSections splits body into its "## " sections, keyed by the exact
// (trimmed) heading text, mapping to the body lines under that heading up
// to the next "## " line or EOF. Lines before the first heading are
// discarded.
func parseSections(body string) map[string][]string {
	sections := map[string][]string{}
	current := ""
	started := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") {
			current = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			started = true
			if _, ok := sections[current]; !ok {
				sections[current] = nil
			}
			continue
		}
		if started {
			sections[current] = append(sections[current], line)
		}
	}
	return sections
}

// findSection looks up a section case-insensitively, as the design
// requires for section-heading matching.
func findSection(sections map[string][]string, name string) ([]string, bool) {
	for k, v := range sections {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return nil, false
}

// countCheckboxItems counts lines matching "^\s*- \[[ xX]\]" among lines.
func countCheckboxItems(lines []string) int {
	n := 0
	for _, line := range lines {
		if checkboxItemRe.MatchString(line) {
			n++
		}
	}
	return n
}

// Lint runs every check in the design's table against one spec, printing
// one line per check (plus one warn line per unknown frontmatter key) to
// o.Stdout, and returns the tally. The returned error is only for an
// operational failure — the spec could not be located or read — not for
// any check failing.
func Lint(o LintOptions) (LintResult, error) {
	var res LintResult

	path, err := locateLintSpec(o.Home, o.SpecArg)
	if err != nil {
		return res, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return res, fmt.Errorf("read spec %s: %w", path, err)
	}

	body := specBody(raw)
	sections := parseSections(body)

	// Check 1: frontmatter.
	sp, fmErr := resolveSpecFile(Options{Home: o.Home}, path)
	if fmErr != nil {
		lintLine(o.Stdout, "FAIL", "frontmatter: "+fmErr.Error())
		res.Fails++
	} else {
		fm := home.ParseFrontmatter(raw)
		var unknown []string
		for k := range fm {
			if !allowedFrontmatterKeys[k] {
				unknown = append(unknown, k)
			}
		}
		sort.Strings(unknown)
		for _, k := range unknown {
			lintLine(o.Stdout, "warn", fmt.Sprintf("frontmatter: unknown key %q", k))
			res.Warns++
		}
		if len(unknown) == 0 {
			lintLine(o.Stdout, "ok", "frontmatter")
		}
	}

	// Check 2: sections.
	var missing []string
	for _, name := range requiredSections {
		if _, ok := findSection(sections, name); !ok {
			missing = append(missing, "## "+name)
		}
	}
	switch {
	case len(missing) > 0:
		lintLine(o.Stdout, "FAIL", "sections: missing "+strings.Join(missing, ", "))
		res.Fails++
	default:
		if _, ok := findSection(sections, "Constraints"); !ok {
			lintLine(o.Stdout, "warn", "sections: missing ## Constraints")
			res.Warns++
		} else {
			lintLine(o.Stdout, "ok", "sections")
		}
	}

	// Check 3: placeholders.
	if lines := placeholderLines(raw); len(lines) > 0 {
		strs := make([]string, len(lines))
		for i, n := range lines {
			strs[i] = strconv.Itoa(n)
		}
		lintLine(o.Stdout, "FAIL", "placeholders: template placeholders on line(s) "+strings.Join(strs, ", "))
		res.Fails++
	} else {
		lintLine(o.Stdout, "ok", "placeholders")
	}

	// Check 4: tasks.
	taskLines, _ := findSection(sections, "Tasks")
	taskCount := countCheckboxItems(taskLines)
	switch {
	case taskCount == 0:
		lintLine(o.Stdout, "FAIL", `tasks: no "- [ ]" items under ## Tasks`)
		res.Fails++
	case taskCount > 8:
		lintLine(o.Stdout, "warn", fmt.Sprintf("tasks: %d tasks — consider splitting the spec", taskCount))
		res.Warns++
	default:
		lintLine(o.Stdout, "ok", "tasks")
	}

	// Check 5: acceptance.
	acceptanceLines, _ := findSection(sections, "Acceptance criteria")
	if countCheckboxItems(acceptanceLines) == 0 {
		lintLine(o.Stdout, "FAIL", `acceptance: no "- [ ]" items under ## Acceptance criteria`)
		res.Fails++
	} else {
		lintLine(o.Stdout, "ok", "acceptance")
	}

	// Check 6: max_iter.
	if fmErr != nil {
		lintLine(o.Stdout, "skip", "max_iter (earlier failure)")
	} else if sp.MaxIter < taskCount+2 {
		lintLine(o.Stdout, "warn", fmt.Sprintf("max_iter: %d < %d tasks + 2 — allow one planning iteration plus at least one retry", sp.MaxIter, taskCount))
		res.Warns++
	} else {
		lintLine(o.Stdout, "ok", "max_iter")
	}

	// Check 7: base.
	baseOK := false
	var baseSHA string
	if fmErr != nil {
		lintLine(o.Stdout, "skip", "base (earlier failure)")
	} else {
		if _, err := gitx.Run(sp.Repo, "remote", "get-url", "origin"); err == nil {
			if out, ferr := gitx.Run(sp.Repo, "fetch", "origin"); ferr != nil {
				lintLine(o.Stdout, "warn", "base: fetch origin failed (using local refs): "+firstLine(out, ferr))
				res.Warns++
			}
		}
		sha, err := resolveBase(sp.Repo, sp.Base)
		if err != nil {
			lintLine(o.Stdout, "FAIL", "base: "+err.Error())
			res.Fails++
		} else {
			lintLine(o.Stdout, "ok", fmt.Sprintf("base: %s @ %s", sp.Base, short(sha)))
			baseOK = true
			baseSHA = sha
		}
	}

	// Check 9: depends.
	switch {
	case fmErr != nil:
		lintLine(o.Stdout, "skip", "depends (earlier failure)")
	case len(sp.DependsOn) == 0:
		lintLine(o.Stdout, "skip", "depends (no depends_on)")
	default:
		g := loadSpecGraph(o.Home)
		depOpts := Options{Home: o.Home, StateRoot: home.RunsDir(o.Home)}
		fails, warns := depsCheck(depOpts, g, sp.Slug, sp.DependsOn)
		for _, f := range fails {
			lintLine(o.Stdout, "FAIL", "depends: "+f)
			res.Fails++
		}
		if len(fails) == 0 {
			for _, w := range warns {
				lintLine(o.Stdout, "warn", "depends: "+w)
				res.Warns++
			}
			if len(warns) == 0 {
				lintLine(o.Stdout, "ok", "depends")
			}
		}
	}

	// Check 8: verify-on-base.
	switch {
	case o.NoVerify:
		lintLine(o.Stdout, "skip", "verify-on-base (--no-verify)")
	case fmErr != nil || !baseOK:
		lintLine(o.Stdout, "skip", "verify-on-base (earlier failure)")
	default:
		checkVerifyOnBase(o, sp, baseSHA, &res)
	}

	fmt.Fprintf(o.Stdout, "lint: %d failed, %d warnings\n", res.Fails, res.Warns)
	if res.Fails > 0 {
		fmt.Fprintln(o.Stdout, "fix the FAIL lines above before 'nightshift ready'")
	}

	return res, nil
}

// checkVerifyOnBase runs check 8: a throwaway --no-local clone of
// sp.Repo checked out at baseSHA, with sp.Verify run against it through
// a BaseVerifier — o.Verifier if set, else a real sbxBaseVerifier named
// "nightshift-lint-<slug>-verify".
func checkVerifyOnBase(o LintOptions, sp *Spec, baseSHA string, res *LintResult) {
	sweepStaleLintDirs(filepath.Join(home.RunsDir(o.Home), ".lint"))

	verifier := o.Verifier
	if verifier == nil {
		bin := o.SbxBin
		if bin == "" {
			cfg, _ := home.ParseConfig(home.ConfigPath(o.Home))
			bin = envConfigDefault("NIGHTSHIFT_SBX_BIN", cfg, "sbx")
		}
		if _, err := exec.LookPath(bin); err != nil {
			lintLine(o.Stdout, "FAIL", fmt.Sprintf("verify-on-base: %s not found on PATH", bin))
			res.Fails++
			return
		}
		verifier = sbxBaseVerifier{bin: bin, name: "nightshift-lint-" + sp.Slug + "-verify"}
	}

	tmp := filepath.Join(home.RunsDir(o.Home), ".lint", fmt.Sprintf("%s-%d", sp.Slug, time.Now().UnixNano()))
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		lintLine(o.Stdout, "FAIL", "verify-on-base: mkdir: "+err.Error())
		res.Fails++
		return
	}
	defer os.RemoveAll(tmp)

	clone := filepath.Join(tmp, "repo")
	if out, err := gitx.Run(tmp, "clone", "--no-local", sp.Repo, "repo"); err != nil {
		lintLine(o.Stdout, "FAIL", fmt.Sprintf("verify-on-base: clone: %v: %s", err, out))
		res.Fails++
		return
	}
	// A plain clone only carries refs reachable from the default branch,
	// so a base pinned to an older sha may not be present yet.
	if _, err := gitx.Run(clone, "cat-file", "-e", baseSHA+"^{commit}"); err != nil {
		if out, ferr := gitx.Run(clone, "fetch", "origin", baseSHA); ferr != nil {
			lintLine(o.Stdout, "FAIL", fmt.Sprintf("verify-on-base: fetch base: %v: %s", ferr, out))
			res.Fails++
			return
		}
	}
	if out, err := gitx.Run(clone, "checkout", "-q", "--detach", baseSHA); err != nil {
		lintLine(o.Stdout, "FAIL", fmt.Sprintf("verify-on-base: checkout: %v: %s", err, out))
		res.Fails++
		return
	}
	if out, err := gitx.Run(clone, "remote", "remove", "origin"); err != nil {
		lintLine(o.Stdout, "FAIL", fmt.Sprintf("verify-on-base: remove remote: %v: %s", err, out))
		res.Fails++
		return
	}

	base := o.Ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, sp.IterTimeout)
	defer cancel()

	out, err := verifier.Verify(ctx, clone, sp.Egress, sp.Image, sp.Verify)
	if err != nil {
		if o.Ctx != nil && o.Ctx.Err() != nil {
			lintLine(o.Stdout, "FAIL", "verify-on-base: interrupted")
			res.Fails++
			return
		}
		lintLine(o.Stdout, "FAIL", "verify-on-base: exit: "+err.Error())
		res.Fails++
		if trimmed := strings.TrimRight(out, "\n"); trimmed != "" {
			lines := strings.Split(trimmed, "\n")
			if len(lines) > 30 {
				lines = lines[len(lines)-30:]
			}
			for _, line := range lines {
				fmt.Fprintln(o.Stdout, "      "+line)
			}
		}
		return
	}
	lintLine(o.Stdout, "ok", "verify-on-base")
}

// sweepStaleLintDirs removes entries under dir whose modification time is
// more than 24h old, ignoring every error — leftovers from a forced quit
// that skipped checkVerifyOnBase's own defer os.RemoveAll.
func sweepStaleLintDirs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > 24*time.Hour {
			os.RemoveAll(filepath.Join(dir, entry.Name()))
		}
	}
}
