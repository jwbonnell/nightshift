// Package runner drives one ready spec through the sandboxed Ralph loop:
// resolution and readiness (this file, spec.go, and base.go), state and
// setup, the sandbox, the loop itself, and reporting back.
package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/agent"
	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
)

const (
	defaultMaxIter     = 10
	defaultIterTimeout = 30 * time.Minute
	defaultModel       = "sonnet"
	defaultPlanner     = "opus"
	defaultReviewModel = "opus"
)

// Spec is one resolved, ready-to-run spec.
type Spec struct {
	Path         string // absolute path to the spec .md, under <Home>/specs/ready/
	Slug         string
	Repo         string // absolute
	Verify       string
	Model        string // Display() of ModelSel — unchanged for a bare/claude selector
	Planner      string // Display() of PlannerSel
	ModelSel     agent.Selector
	PlannerSel   agent.Selector
	MaxIter      int
	IterTimeout  time.Duration
	Egress       []string
	Review       bool
	Lenses       []string // review lens names, in frontmatter order; empty when Review is false
	ReviewRounds int
	ReviewModel  string // raw selector string, as handed to Reviewer
	ReviewSel    agent.Selector
	Created      string
	ContentHash  string
	Base         string // requested base name
	BaseSHA      string // resolved commit sha (filled by Run via resolveBase)

	// OpencodeCfgPath/OpencodeCfg are set only when some role resolves to
	// opencode: the expanded NIGHTSHIFT_OPENCODE_CONFIG path and its
	// validated, compacted JSON content.
	OpencodeCfgPath string
	OpencodeCfg     string

	src map[string]string
}

// builtinLenses are the review lens names shipped with nightshift.
var builtinLenses = map[string]bool{
	"general":      true,
	"requirements": true,
	"quality":      true,
	"security":     true,
}

// lensNameRe matches a valid lens name, built-in or custom.
var lensNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// parseReviewSetting parses the review frontmatter/env/config value val
// into an ordered, deduplicated list of lens names: "true" means
// ["general"], "false" means no lenses (nil), and anything else is parsed
// as a comma list of lens names, each either built-in or a custom lens
// file at <homeDir>/lenses/<name>.md.
func parseReviewSetting(val, homeDir string) ([]string, error) {
	if b, err := strconv.ParseBool(val); err == nil {
		if b {
			return []string{"general"}, nil
		}
		return nil, nil
	}

	badFormat := fmt.Errorf("review %q: want true, false or a list of lenses", val)

	seen := map[string]bool{}
	var lenses []string
	for _, item := range strings.Split(val, ",") {
		name := home.Unquote(strings.TrimSpace(item))
		if name == "" {
			continue
		}
		if !lensNameRe.MatchString(name) {
			return nil, badFormat
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		lenses = append(lenses, name)
	}
	if len(lenses) == 0 {
		return nil, badFormat
	}

	for _, name := range lenses {
		if builtinLenses[name] {
			continue
		}
		p := filepath.Join(home.LensesDir(homeDir), name+".md")
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return nil, fmt.Errorf("unknown review lens %q (built-in: general, requirements, quality, security; or add %s)", name, p)
		}
	}
	return lenses, nil
}

// pick implements the five-tier precedence flag > frontmatter > env >
// config > default (D3), recording which tier won in src[field].
func pick(src map[string]string, field, flagV, fmV, envV, configV, def string) string {
	switch {
	case flagV != "":
		src[field] = "flag"
		return flagV
	case fmV != "":
		src[field] = "frontmatter"
		return fmV
	case envV != "":
		src[field] = "env"
		return envV
	case configV != "":
		src[field] = "config"
		return configV
	default:
		src[field] = "default"
		return def
	}
}

// parseIterTimeout resolves iter_timeout via the frontmatter > env >
// config > default precedence (there's no flag for it) and validates the
// result as a positive Go duration. Shared by resolveSpecFile and
// Review's CLI so the two can't drift.
func parseIterTimeout(src map[string]string, fmV, envV, configV string) (time.Duration, error) {
	s := pick(src, "iter_timeout", "", fmV, envV, configV, defaultIterTimeout.String())
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("iter_timeout %q: must be a positive Go duration (e.g. 30m)", s)
	}
	return d, nil
}

// parsePositiveInt parses s as a positive decimal integer with no
// surrounding whitespace and no overflow check, matching the reference's
// deliberately simple parser.
func parsePositiveInt(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a positive integer")
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return 0, errors.New("must be > 0")
	}
	return n, nil
}

// expandHome expands a leading "~/" using the real home directory; any
// other path is returned unchanged.
func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	hd, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(hd, p[2:]), nil
}

// slugFromFilename returns name with whatever extension it has removed,
// regardless of the extension's case (D11.6) — e.g. "DEMO.MD" -> "DEMO".
func slugFromFilename(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// findCaseInsensitive looks for a non-directory entry in dir whose name
// matches name case-insensitively (D11.6), returning its real on-disk
// path and name.
func findCaseInsensitive(dir, name string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(e.Name(), name) {
			return filepath.Join(dir, e.Name()), true
		}
	}
	return "", false
}

// isUnder reports whether path is strictly inside dir.
func isUnder(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// applyReadinessGate implements D2's replacement for reference §3.2.1 step
// 5: cand must resolve to somewhere inside <home>/specs/ready/.
func applyReadinessGate(homeDir, cand string) (string, error) {
	absCand, err := filepath.Abs(cand)
	if err != nil {
		return "", err
	}
	absCand = filepath.Clean(absCand)

	readyAbs, err := filepath.Abs(home.ReadyDir(homeDir))
	if err != nil {
		return "", err
	}
	readyAbs = filepath.Clean(readyAbs)
	if isUnder(absCand, readyAbs) {
		return absCand, nil
	}

	draftAbs, err := filepath.Abs(home.DraftDir(homeDir))
	if err != nil {
		return "", err
	}
	draftAbs = filepath.Clean(draftAbs)
	if isUnder(absCand, draftAbs) {
		base := filepath.Base(absCand)
		slug := slugFromFilename(base)
		return "", fmt.Errorf("%s is still in specs/draft/ — run 'nightshift ready %s' when you mean it", base, slug)
	}

	return "", fmt.Errorf("spec must live under %s (got %s)", readyAbs, absCand)
}

// locateSpec resolves opts.SpecArg to an absolute spec path and applies
// the D2 readiness gate. Name resolution order (unchanged from the
// reference): arg as given, then <ready>/arg, then <ready>/arg.md — the
// last two matched case-insensitively (D11.6).
func locateSpec(opts Options) (string, error) {
	if opts.SpecArg == "" {
		return "", errors.New("no spec given")
	}
	readyDir := home.ReadyDir(opts.Home)

	if st, err := os.Stat(opts.SpecArg); err == nil && !st.IsDir() {
		return applyReadinessGate(opts.Home, opts.SpecArg)
	}
	if p, ok := findCaseInsensitive(readyDir, opts.SpecArg); ok {
		return applyReadinessGate(opts.Home, p)
	}
	if p, ok := findCaseInsensitive(readyDir, opts.SpecArg+".md"); ok {
		return applyReadinessGate(opts.Home, p)
	}

	return "", fmt.Errorf("no such spec: %s (looked in %s)", opts.SpecArg, readyDir)
}

// resolveSpec locates, reads, and fully validates one spec's frontmatter.
func resolveSpec(opts Options) (*Spec, error) {
	path, err := locateSpec(opts)
	if err != nil {
		return nil, err
	}
	return resolveSpecFile(opts, path)
}

// resolveSpecFile reads and fully validates one spec's frontmatter at
// path, without locating it or applying the readiness gate (lint.go
// calls this directly on a draft or ready path). Validation order
// matches the reference: repo, base NAME (not yet resolved to a sha —
// that's resolveBase, called later by the caller), verify, model,
// planner, max_iter, iter_timeout, review, review_model, egress.
func resolveSpecFile(opts Options, path string) (*Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spec %s: %w", path, err)
	}

	slug := slugFromFilename(filepath.Base(path))
	sum := sha256.Sum256(raw)
	contentHash := hex.EncodeToString(sum[:])

	fm := home.ParseFrontmatter(raw)
	fmVal := func(key string) string {
		return home.Unquote(strings.TrimSpace(fm[key]))
	}

	cfg, err := home.ParseConfig(home.ConfigPath(opts.Home))
	if err != nil {
		return nil, err
	}

	repoRaw := fmVal("repo")
	if repoRaw == "" {
		return nil, fmt.Errorf("spec %s has no 'repo:' frontmatter — add the checkout the agent should clone", slug)
	}
	expanded, err := expandHome(repoRaw)
	if err != nil {
		return nil, fmt.Errorf("repo %q: %w", repoRaw, err)
	}
	repoAbs, err := filepath.Abs(expanded)
	if err != nil {
		return nil, fmt.Errorf("repo %q: %w", repoRaw, err)
	}
	if !gitx.IsRepo(repoAbs) {
		return nil, fmt.Errorf("repo %s is not a git repository — set 'repo:' to a git checkout", repoAbs)
	}

	src := map[string]string{}
	baseName := pick(src, "base", opts.Base, fmVal("base"), os.Getenv("NIGHTSHIFT_BASE"), cfg["NIGHTSHIFT_BASE"], "")
	if baseName == "" {
		baseName = defaultBaseName(repoAbs)
		src["base"] = "default"
	}

	verifyRaw := fmVal("verify")
	if verifyRaw == "" {
		return nil, fmt.Errorf("spec %s has no 'verify:' frontmatter — add the command that must pass before any commit", slug)
	}

	model := pick(src, "model", "", fmVal("model"), os.Getenv("NIGHTSHIFT_MODEL"), cfg["NIGHTSHIFT_MODEL"], defaultModel)
	modelSel, err := agent.ParseSelector(model)
	if err != nil {
		return nil, fmt.Errorf("%s (from %s): %w", "model", src["model"], err)
	}
	planner := pick(src, "planner_model", "", fmVal("planner_model"), os.Getenv("NIGHTSHIFT_PLANNER_MODEL"), cfg["NIGHTSHIFT_PLANNER_MODEL"], defaultPlanner)
	plannerSel, err := agent.ParseSelector(planner)
	if err != nil {
		return nil, fmt.Errorf("%s (from %s): %w", "planner_model", src["planner_model"], err)
	}

	flagIter := ""
	if opts.MaxIter > 0 {
		flagIter = strconv.Itoa(opts.MaxIter)
	}
	maxIterStr := pick(src, "max_iter", flagIter, fmVal("max_iter"), os.Getenv("NIGHTSHIFT_MAX_ITER"), cfg["NIGHTSHIFT_MAX_ITER"], strconv.Itoa(defaultMaxIter))
	maxIter, err := parsePositiveInt(maxIterStr)
	if err != nil {
		return nil, fmt.Errorf("max_iter %q: %w", maxIterStr, err)
	}

	iterTimeout, err := parseIterTimeout(src, fmVal("iter_timeout"), os.Getenv("NIGHTSHIFT_ITER_TIMEOUT"), cfg["NIGHTSHIFT_ITER_TIMEOUT"])
	if err != nil {
		return nil, err
	}

	reviewStr := pick(src, "review", "", fmVal("review"), os.Getenv("NIGHTSHIFT_REVIEW"), cfg["NIGHTSHIFT_REVIEW"], "false")
	lenses, err := parseReviewSetting(reviewStr, opts.Home)
	if err != nil {
		return nil, err
	}
	review := len(lenses) > 0
	reviewRoundsStr := pick(src, "review_rounds", "", fmVal("review_rounds"), os.Getenv("NIGHTSHIFT_REVIEW_ROUNDS"), cfg["NIGHTSHIFT_REVIEW_ROUNDS"], "0")
	reviewRounds, err := strconv.Atoi(reviewRoundsStr)
	if err != nil || reviewRounds < 0 || reviewRounds > 5 {
		return nil, fmt.Errorf("review_rounds %q: want 0-5", reviewRoundsStr)
	}
	reviewModel := pick(src, "review_model", "", fmVal("review_model"), os.Getenv("NIGHTSHIFT_REVIEW_MODEL"), cfg["NIGHTSHIFT_REVIEW_MODEL"], defaultReviewModel)
	reviewSel, err := agent.ParseSelector(reviewModel)
	if err != nil {
		return nil, fmt.Errorf("%s (from %s): %w", "review_model", src["review_model"], err)
	}

	var egress []string
	for _, item := range strings.Split(fm["egress"], ",") {
		item = home.Unquote(strings.TrimSpace(item))
		if item != "" {
			egress = append(egress, item)
		}
	}

	var opencodeCfgPath, opencodeCfg string
	if modelSel.Agent == "opencode" || plannerSel.Agent == "opencode" || reviewSel.Agent == "opencode" {
		cfgRaw := envConfigDefault("NIGHTSHIFT_OPENCODE_CONFIG", cfg, "")
		if cfgRaw != "" {
			expanded, err := expandHome(cfgRaw)
			if err != nil {
				return nil, fmt.Errorf("NIGHTSHIFT_OPENCODE_CONFIG %q: %w", cfgRaw, err)
			}
			compact, err := agent.LoadOpencodeConfig(expanded)
			if err != nil {
				return nil, err
			}
			opencodeCfgPath = expanded
			opencodeCfg = compact
		}
	}

	return &Spec{
		Path:            path,
		Slug:            slug,
		Repo:            repoAbs,
		Verify:          verifyRaw,
		Model:           modelSel.Display(),
		Planner:         plannerSel.Display(),
		ModelSel:        modelSel,
		PlannerSel:      plannerSel,
		MaxIter:         maxIter,
		IterTimeout:     iterTimeout,
		Egress:          egress,
		Review:          review,
		Lenses:          lenses,
		ReviewRounds:    reviewRounds,
		ReviewModel:     reviewModel,
		ReviewSel:       reviewSel,
		OpencodeCfgPath: opencodeCfgPath,
		OpencodeCfg:     opencodeCfg,
		Created:         fmVal("created"),
		ContentHash:     contentHash,
		Base:            baseName,
		src:             src,
	}, nil
}
