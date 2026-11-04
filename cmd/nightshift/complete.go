package main

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/runner"
)

type slugSource int

const (
	slugsNone slugSource = iota
	slugsReady
	slugsDraft
	slugsDraftThenReady
	slugsRuns
	slugsBashWord
)

type completionCmd struct {
	name  string
	flags []string // a trailing "=" marks a flag that takes a value
	slugs slugSource
	multi bool // several positionals allowed (done)
}

// completionCommands is the single source of truth for tab completion. A
// flag ending in "=" takes a value: the next word is its value, never a
// slug. TestCompleteCoversEveryFlag checks every flags list against the
// subcommand's -h output, so a new flag or subcommand needs an entry here.
var completionCommands = []completionCmd{
	{name: "init"},
	{name: "new", flags: []string{"repo=", "verify=", "no-interview"}},
	{name: "refine", slugs: slugsDraft},
	{name: "ready", flags: []string{"no-verify", "no-lint", "plan"}, slugs: slugsDraft},
	{name: "lint", flags: []string{"no-verify"}, slugs: slugsDraftThenReady},
	{name: "done", flags: []string{"complete"}, slugs: slugsReady, multi: true},
	{name: "run", flags: []string{"next", "fresh", "yes", "max-iter=", "extend-iter=", "until=",
		"dry-run", "base=", "max-concurrent=", "ignore-deps", "f", "follow", "plan-only", "jobs="}, slugs: slugsReady},
	{name: "status", flags: []string{"all", "since=", "done=", "repo=", "w", "watch"}, slugs: slugsRuns},
	{name: "logs", flags: []string{"f", "agent", "raw", "iter=", "verify=", "n="}, slugs: slugsRuns},
	{name: "pr", flags: []string{"pending", "force", "include-drafts", "dry-run", "list", "limit=",
		"model=", "max-turns=", "timeout=", "max-lines=", "forge="}},
	{name: "review", flags: []string{"model=", "lens="}, slugs: slugsRuns},
	{name: "version"},
	{name: "completion", slugs: slugsBashWord},
}

func findCompletionCmd(name string) *completionCmd {
	for i := range completionCommands {
		if completionCommands[i].name == name {
			return &completionCommands[i]
		}
	}
	return nil
}

func (c *completionCmd) flagForm(f string) string {
	n := strings.TrimSuffix(f, "=")
	if len(n) == 1 {
		return "-" + n
	}
	return "--" + n
}

// valueFlag reports whether word (e.g. "--repo") names a value flag of c.
func (c *completionCmd) valueFlag(word string) (string, bool) {
	if !strings.HasPrefix(word, "-") || strings.Contains(word, "=") {
		return "", false
	}
	n := strings.TrimLeft(word, "-")
	for _, f := range c.flags {
		if f == n+"=" {
			return n, true
		}
	}
	return "", false
}

func (c *completionCmd) hasFlag(name string) bool {
	for _, f := range c.flags {
		if strings.TrimSuffix(f, "=") == name {
			return true
		}
	}
	return false
}

// cmdComplete implements the hidden `__complete`: words are the command
// line after "nightshift", ending with the word being completed. It prints
// candidates one per line, never writes to stderr, and always returns 0.
func cmdComplete(words []string, stdout io.Writer) int {
	h, err := home.Home()
	if err != nil {
		return 0
	}
	if len(words) == 0 {
		words = []string{""}
	}
	cur, prev := words[len(words)-1], words[:len(words)-1]
	var out []string
	if len(prev) == 0 {
		for _, c := range completionCommands {
			if strings.HasPrefix(c.name, cur) {
				out = append(out, c.name)
			}
		}
	} else if c := findCompletionCmd(prev[0]); c != nil {
		out = completeFor(h, c, prev[1:], cur)
	}
	for _, l := range out {
		fmt.Fprintln(stdout, l)
	}
	return 0
}

// valueCandidates completes the value of value flag n of c given the text
// typed so far.
func valueCandidates(h string, c *completionCmd, n, cur string) []string {
	switch {
	case c.name == "new" && n == "repo":
		return []string{"__dirs__"}
	case c.name == "review" && n == "lens":
		return completeLenses(h, cur)
	}
	return nil
}

func completeFor(h string, c *completionCmd, prev []string, cur string) []string {
	if i := strings.Index(cur, "="); i > 0 && strings.HasPrefix(cur, "-") {
		n, ok := c.valueFlag(cur[:i])
		if !ok {
			return nil
		}
		cands := valueCandidates(h, c, n, cur[i+1:])
		for j, v := range cands {
			if v != "__dirs__" {
				cands[j] = cur[:i+1] + v
			}
		}
		return cands
	}
	if strings.HasPrefix(cur, "-") {
		var out []string
		for _, f := range c.flags {
			if s := c.flagForm(f); strings.HasPrefix(s, cur) {
				out = append(out, s)
			}
		}
		return out
	}
	if len(prev) > 0 {
		if n, ok := c.valueFlag(prev[len(prev)-1]); ok {
			return valueCandidates(h, c, n, cur)
		}
	}
	var positional []string
	flagSeen := map[string]bool{}
	for i := 0; i < len(prev); i++ {
		w := prev[i]
		if !strings.HasPrefix(w, "-") || w == "-" {
			positional = append(positional, w)
			continue
		}
		flagSeen[strings.TrimLeft(strings.SplitN(w, "=", 2)[0], "-")] = true
		if _, ok := c.valueFlag(w); ok {
			i++
		}
	}
	switch c.name {
	case "run":
		if flagSeen["next"] {
			return nil
		}
	case "done":
		if flagSeen["complete"] {
			return nil
		}
	}
	if !c.multi && len(positional) > 0 {
		return nil
	}
	switch c.slugs {
	case slugsBashWord:
		if strings.HasPrefix("bash", cur) {
			return []string{"bash"}
		}
		return nil
	case slugsNone:
		return nil
	}
	var all []string
	switch c.slugs {
	case slugsReady:
		all = specSlugs(home.ReadyDir(h))
	case slugsDraft:
		all = specSlugs(home.DraftDir(h))
	case slugsDraftThenReady:
		all = append(specSlugs(home.DraftDir(h)), specSlugs(home.ReadyDir(h))...)
	case slugsRuns:
		all = runSlugs(h)
	}
	var out []string
	seen := map[string]bool{}
	lc := strings.ToLower(cur)
	for _, s := range all {
		k := strings.ToLower(s)
		if seen[k] || !strings.HasPrefix(k, lc) {
			continue
		}
		seen[k] = true
		typed := false
		if c.multi {
			for _, p := range positional {
				if strings.EqualFold(p, s) {
					typed = true
				}
			}
		}
		if !typed {
			out = append(out, s)
		}
	}
	return out
}

// specSlugs lists the slugs of the .md files in dir (case-insensitive ext).
func specSlugs(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if strings.EqualFold(ext, ".md") {
			out = append(out, strings.TrimSuffix(e.Name(), ext))
		}
	}
	return out
}

// runSlugs lists runs/<slug> folders that hold a run.json.
func runSlugs(h string) []string {
	dir := home.RunsDir(h)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "run.json")); err == nil {
			out = append(out, e.Name())
		}
	}
	return out
}

// completeLenses completes a --lens value, which may be a comma list: only
// the part after the last comma is matched, and candidates keep the part
// before it.
func completeLenses(h, cur string) []string {
	head, tail := "", cur
	if i := strings.LastIndex(cur, ","); i >= 0 {
		head, tail = cur[:i+1], cur[i+1:]
	}
	set := map[string]bool{}
	for _, n := range runner.BuiltinLensNames() {
		set[n] = true
	}
	for _, n := range specSlugs(home.LensesDir(h)) {
		set[n] = true
	}
	var names []string
	for n := range set {
		if strings.HasPrefix(n, tail) {
			names = append(names, head+n)
		}
	}
	sort.Strings(names)
	return names
}

//go:embed complete.bash
var completeBashScript string

// cmdCompletion prints a shell completion script. Add further shells as
// cases of the switch.
func cmdCompletion(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 {
		switch args[0] {
		case "bash":
			bin, err := os.Executable()
			if err != nil || bin == "" {
				bin = "nightshift"
			}
			q := "'" + strings.ReplaceAll(bin, "'", `'\''`) + "'"
			fmt.Fprint(stdout, strings.ReplaceAll(completeBashScript, "__NIGHTSHIFT_BIN__", q))
			return 0
		}
	}
	fmt.Fprintln(stderr, "usage: nightshift completion bash")
	return 2
}
