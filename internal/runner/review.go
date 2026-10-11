package runner

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/agent"
	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/lock"
)

// reviewDiffMax caps the diff.patch written into a review workspace;
// longer diffs are truncated with a pointer to read repo/ directly.
const reviewDiffMax = 200 << 10

// lensFS holds the built-in review lens prompts, one file per lens name.
// See internal/runner/lenses/.
//
//go:embed lenses/*.md
var lensFS embed.FS

// reviewPrompt is the general lens's prompt: the headless review prompt,
// embedded verbatim and sent on stdin to the reviewing claude. See
// lenses/general.md.
var reviewPrompt = mustLens("general")

func mustLens(name string) string {
	data, err := fs.ReadFile(lensFS, "lenses/"+name+".md")
	if err != nil {
		panic(err)
	}
	return string(data)
}

// lensPrompt resolves a lens's prompt text: a custom <homeDir>/lenses/<name>.md
// file wins over a built-in of the same name, otherwise the embedded
// built-in is used. It returns the same "unknown review lens" error as
// parseReviewSetting when name is neither.
func lensPrompt(homeDir, name string) (string, error) {
	p := filepath.Join(home.LensesDir(homeDir), name+".md")
	if data, err := os.ReadFile(p); err == nil {
		return string(data), nil
	}
	if builtinLenses[name] {
		return mustLens(name), nil
	}
	return "", fmt.Errorf("unknown review lens %q (built-in: general, requirements, quality, security; or add %s)", name, p)
}

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
func buildReviewWorkspace(ctx context.Context, ws, repo, base, ref, specPath, progress, summary string) (string, error) {
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
	diffstat, err := exec.CommandContext(ctx, "git", "-C", repo, "diff", "--no-ext-diff", "--no-textconv", "--stat", base+".."+head).Output()
	if err != nil {
		return "", fmt.Errorf("diffstat %s..%s: %w", base, head, err)
	}
	if err := os.WriteFile(filepath.Join(inputs, "diffstat.txt"), diffstat, 0o644); err != nil {
		return "", err
	}

	patch, err := exec.CommandContext(ctx, "git", "-C", repo, "diff", "--no-ext-diff", "--no-textconv", base+".."+head).Output()
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

// Reviewer runs one headless review with ws mounted and returns the
// model's final text. The real implementation (sbxReviewer) runs it in
// its own sbx sandbox, read-only, with no permission-bypass flag; tests
// substitute a stub.
type Reviewer interface {
	Review(ctx context.Context, ws, model, prompt string) (string, error)
}

// sbxReviewer is the real Reviewer, driving the sbx CLI in a dedicated
// "nightshift-<slug>-review" sandbox, distinct from that spec's own
// agent/ctl/verify sandboxes.
type sbxReviewer struct {
	bin, claudeBin, slug string
	opencodeBin          string
	opencodeConfig       string // compacted JSON content, or "" if unset
}

func (rv sbxReviewer) name() string {
	return "nightshift-" + rv.slug + "-review"
}

// Review creates a fresh sandbox mounting ws, runs the reviewer's agent
// in it read-only (Read/Grep/Glob for claude, the read-only opencode
// permissions for opencode), and always removes the sandbox afterward —
// even when create or exec fails. model is a raw agent selector (see
// internal/agent), which may now name opencode as well as claude.
func (rv sbxReviewer) Review(ctx context.Context, ws, model, prompt string) (string, error) {
	sel, err := agent.ParseSelector(model)
	if err != nil {
		return "", err
	}
	ag, err := agent.New(sel, agent.Settings{ClaudeBin: rv.claudeBin, OpencodeBin: rv.opencodeBin, OpencodeConfig: rv.opencodeConfig})
	if err != nil {
		return "", err
	}
	inv := agent.Invocation{Role: agent.RoleReview, Model: sel.Model, ReadOnly: true}

	name := rv.name()
	rctx, rcancel := context.WithTimeout(ctx, cleanupTimeout)
	_, _ = execStream(rctx, nil, io.Discard, rv.bin, "rm", "-f", name)
	rcancel()
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		_, _ = execStream(cctx, nil, io.Discard, rv.bin, "rm", "-f", name)
	}()

	if out, err := execStream(ctx, nil, io.Discard, rv.bin, "create", "--name", name, ag.SbxAgent(), ws); err != nil {
		return "", fmt.Errorf("create: %w: %s", err, out)
	}

	var stdout, stderr bytes.Buffer
	args := append([]string{"exec", "-w", ws, name}, ag.Argv(inv)...)
	err = execSplit(ctx, strings.NewReader(prompt), &stdout, &stderr, rv.bin, args...)
	out := stripSbxBanner(stdout.String())
	if err != nil {
		return "", fmt.Errorf("exec: %w: %s", err, firstBytes(out, 500))
	}

	res := ag.Parse(out)
	if !res.Found {
		return "", fmt.Errorf("review output missing result: %s", firstBytes(out, 500))
	}
	if res.IsError {
		return "", fmt.Errorf("review reported an error: %s", firstBytes(out, 500))
	}
	return res.Text, nil
}

// firstBytes returns the first n bytes of s, unchanged if shorter.
func firstBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// verdictPattern matches the verdict line the review prompt mandates.
var verdictPattern = regexp.MustCompile(`(?im)^VERDICT:\s*(approve|concerns|reject)\b`)

// parseVerdict extracts the verdict from a reviewer's reply, lowercased,
// or "unknown" if no VERDICT line matches.
func parseVerdict(text string) string {
	m := verdictPattern.FindStringSubmatch(text)
	if m == nil {
		return "unknown"
	}
	return strings.ToLower(m[1])
}

// writeReviewNote writes the review note to <homeDir>/results/ and,
// when homeDir is itself a git repo, commits it there (best-effort, like
// writeResultNote). headShort/baseShort may be empty when the workspace
// never got far enough to resolve a head.
func writeReviewNote(homeDir, slug, verdict, headShort, baseShort, model, body string, now time.Time) (string, error) {
	dir := home.ResultsDir(homeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := slug + "-" + now.Format("2006-01-02") + "-review.md"
	path := uniqueResultPath(dir, name)

	content := fmt.Sprintf(
		"---\ntype: nightshift-review\nspec: %s\nverdict: %s\nreviewed: %s\nbase: %s\nmodel: %s\ncreated: %s\n---\n\n# Review: %s — %s\n\n%s\n",
		slug, verdict, headShort, baseShort, model, now.Format(time.RFC3339), slug, verdict, body,
	)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}

	if gitx.IsRepo(homeDir) {
		rel, err := filepath.Rel(homeDir, path)
		if err != nil {
			rel = path
		}
		_ = gitx.Commit(homeDir, fmt.Sprintf("nightshift: review %s (%s)", slug, verdict), rel)
	}

	return path, nil
}

// reviewParams carries everything runReview needs to build a workspace,
// run the reviewer and write its note. Shared by finish's automatic
// review (Task 4) and the `nightshift review` CLI (Task 5).
type reviewParams struct {
	home, stateRoot, slug string
	repo, base, ref       string
	specPath, progress    string
	summary               string
	model                 string
	now                   time.Time
}

// runReview builds the review workspace, runs rv over it, writes the
// review note (even on failure, with verdict "error"), and always
// removes the workspace afterward, whatever the outcome. noteErr is set
// only when writeReviewNote itself fails, distinct from err (which also
// covers a workspace or reviewer failure) so a caller can tell a review
// failure apart from a note-write failure and report "note not written"
// instead of an empty note path.
func runReview(ctx context.Context, rv Reviewer, p reviewParams) (verdict, notePath string, noteErr, err error) {
	ws := reviewWSDir(p.stateRoot, p.slug)
	defer os.RemoveAll(ws)

	head, err := buildReviewWorkspace(ctx, ws, p.repo, p.base, p.ref, p.specPath, p.progress, p.summary)
	if err != nil {
		buildErr := err
		notePath, noteErr = writeReviewNote(p.home, p.slug, "error", short(head), short(p.base), p.model, "Review failed: "+buildErr.Error(), p.now)
		return "error", notePath, noteErr, buildErr
	}

	text, err := rv.Review(ctx, ws, p.model, reviewPrompt)
	if err != nil {
		reviewErr := err
		notePath, noteErr = writeReviewNote(p.home, p.slug, "error", short(head), short(p.base), p.model, "Review failed: "+reviewErr.Error(), p.now)
		return "error", notePath, noteErr, reviewErr
	}

	verdict = parseVerdict(text)
	notePath, noteErr = writeReviewNote(p.home, p.slug, verdict, short(head), short(p.base), p.model, text, p.now)
	return verdict, notePath, noteErr, noteErr
}

// ReviewOptions configures `nightshift review <slug>`: a one-shot,
// headless review of a prior (or in-progress) run's work, independent of
// that run's own status.
type ReviewOptions struct {
	Home        string
	StateRoot   string // default: <Home>/runs
	Slug        string
	Model       string   // --model flag; "" falls through to spec frontmatter/env/config/default
	Reviewer    Reviewer // nil = sbxReviewer
	SbxBin      string   // default "sbx"
	ClaudeBin   string   // default "claude"
	OpencodeBin string   // default "opencode"
	Now         time.Time
	Ctx         context.Context
	Stdout      io.Writer
	Stderr      io.Writer
}

func (o *ReviewOptions) fill() {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.StateRoot == "" {
		o.StateRoot = home.RunsDir(o.Home)
	}
	if o.Ctx == nil {
		o.Ctx = context.Background()
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}

	cfg, _ := home.ParseConfig(home.ConfigPath(o.Home))
	if o.SbxBin == "" {
		o.SbxBin = envConfigDefault("NIGHTSHIFT_SBX_BIN", cfg, "sbx")
	}
	if o.ClaudeBin == "" {
		o.ClaudeBin = envConfigDefault("NIGHTSHIFT_CLAUDE_BIN", cfg, "claude")
	}
	if o.OpencodeBin == "" {
		o.OpencodeBin = envConfigDefault("NIGHTSHIFT_OPENCODE_BIN", cfg, "opencode")
	}
}

// Review runs `nightshift review <slug>`: it loads the run's state,
// builds a review workspace at the reviewed ref, runs the reviewer, and
// writes a review note — exactly like an automatic review in finish,
// except there's no live *run and the outcome never touches anything but
// the note and this function's own return. A busy spec lock mirrors
// Run's own busy handling (same message, StatusBusy); any other failure
// before a note could be attempted (no run state, no commits, a lock
// error) is StatusError with a descriptive err. Once runReview is
// reached, the "review: <verdict> — <note path>" line is printed to
// Stdout whatever the outcome — including a failed review, which still
// returns StatusError (exit 1) alongside the written "error" note.
func Review(opts ReviewOptions) (Status, error) {
	opts.fill()

	specLock, err := lock.TryAcquire(specLockPath(opts.StateRoot, opts.Slug), opts.Slug)
	if err != nil {
		var held *lock.HeldError
		if errors.As(err, &held) {
			fmt.Fprintf(opts.Stderr, "spec %s is already running (pid %d, since %s). Wait for it to finish. (%s)\n",
				opts.Slug, held.Holder.PID, held.Holder.Start.Format("15:04"), held.Path)
			return StatusBusy, nil
		}
		return StatusError, err
	}
	defer specLock.Release()

	data, err := os.ReadFile(filepath.Join(opts.StateRoot, opts.Slug, "run.json"))
	if err != nil {
		return StatusError, fmt.Errorf("no run state for %s", opts.Slug)
	}
	var st runState
	if err := json.Unmarshal(data, &st); err != nil {
		return StatusError, fmt.Errorf("no run state for %s", opts.Slug)
	}
	if st.Commits < 1 {
		return StatusError, fmt.Errorf("nothing to review: %s has no commits since base", opts.Slug)
	}

	specPath := ""
	if p, ok := findCaseInsensitive(home.ReadyDir(opts.Home), opts.Slug+".md"); ok {
		specPath = p
	} else if p, ok := findCaseInsensitive(home.DoneDir(opts.Home), opts.Slug+".md"); ok {
		specPath = p
	}

	var fm map[string]string
	if specPath != "" {
		if raw, rerr := os.ReadFile(specPath); rerr == nil {
			fm = home.ParseFrontmatter(raw)
		}
	}

	cfg, _ := home.ParseConfig(home.ConfigPath(opts.Home))

	model := opts.Model
	modelSrc := "flag"
	if model == "" {
		msrc := map[string]string{}
		fmModel := home.Unquote(strings.TrimSpace(fm["review_model"]))
		model = pick(msrc, "review_model", "", fmModel, os.Getenv("NIGHTSHIFT_REVIEW_MODEL"), cfg["NIGHTSHIFT_REVIEW_MODEL"], defaultReviewModel)
		modelSrc = msrc["review_model"]
	}
	sel, err := agent.ParseSelector(model)
	if err != nil {
		return StatusError, fmt.Errorf("%s (from %s): %w", "review_model", modelSrc, err)
	}
	opencodeCfg := ""
	if sel.Agent == "opencode" {
		if cfgRaw := envConfigDefault("NIGHTSHIFT_OPENCODE_CONFIG", cfg, ""); cfgRaw != "" {
			expanded, err := expandHome(cfgRaw)
			if err != nil {
				return StatusError, fmt.Errorf("NIGHTSHIFT_OPENCODE_CONFIG %q: %w", cfgRaw, err)
			}
			opencodeCfg, err = agent.LoadOpencodeConfig(expanded)
			if err != nil {
				return StatusError, err
			}
		}
	}

	fmIterTimeout := home.Unquote(strings.TrimSpace(fm["iter_timeout"]))
	iterTimeout, err := parseIterTimeout(map[string]string{}, fmIterTimeout, os.Getenv("NIGHTSHIFT_ITER_TIMEOUT"), cfg["NIGHTSHIFT_ITER_TIMEOUT"])
	if err != nil {
		return StatusError, err
	}

	progress, _ := ReadScaffold(filepath.Join(opts.StateRoot, opts.Slug, "repo", "PROGRESS.md"))

	rv := opts.Reviewer
	if rv == nil {
		rv = sbxReviewer{bin: opts.SbxBin, claudeBin: opts.ClaudeBin, slug: opts.Slug, opencodeBin: opts.OpencodeBin, opencodeConfig: opencodeCfg}
	}

	ctx, cancel := context.WithTimeout(opts.Ctx, iterTimeout)
	defer cancel()

	params := reviewParams{
		home:      opts.Home,
		stateRoot: opts.StateRoot,
		slug:      opts.Slug,
		repo:      st.Repo,
		base:      st.Base,
		ref:       reviewRefFor(&st, opts.Slug),
		specPath:  specPath,
		progress:  progress,
		summary: reviewSummary(st.Status, strconv.Itoa(st.Iterations), strconv.Itoa(st.Commits),
			short(st.Base), short(st.LastGreen), st.Branch),
		model: model,
		now:   opts.Now,
	}

	verdict, notePath, _, err := runReview(ctx, rv, params)
	if notePath != "" {
		fmt.Fprintf(opts.Stdout, "review: %s — %s\n", verdict, notePath)
	}
	if err != nil {
		return StatusError, err
	}
	return StatusComplete, nil
}
