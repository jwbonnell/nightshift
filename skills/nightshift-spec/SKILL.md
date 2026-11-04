---
name: nightshift-spec
description: Draft, fix or review nightshift specs and runs. Use when writing a spec for nightshift (an overnight sandboxed coding-agent runner), turning a run's review findings into a follow-up spec, or reviewing a finished, red, stalled or blocked nightshift run.
---

# Writing and reviewing nightshift specs

nightshift hands one spec to a coding agent in a sandbox and loops: a planning pass writes
PROGRESS.md, then each worker iteration does one task, the runner runs `verify`, keeps the
commit only when it's green, and resets to the last green commit when it isn't. The agent
has no memory between iterations except PROGRESS.md and git. The spec is re-read every
iteration, so it's the only steering you have. Write it for a capable engineer who has never
seen the repo and can't ask you anything.

For reviewing a finished run, see `references/reviewing-runs.md`.

## File and location

Specs live in `~/.nightshift/specs/{draft,ready,done}/<slug>.md`. `nightshift new <slug>`
starts a draft. `nightshift lint <slug>` checks it. `nightshift ready <slug> [--plan]` lints
it, moves it to ready and optionally runs the planning pass now.

## Frontmatter

```yaml
---
type: spec
created: 2026-11-02T09:00:00-05:00
repo: ~/path/to/checkout
verify: <one shell command, hermetic, covers what the tasks change>
max_iter: 8          # at least tasks + 2 (lint warns otherwise)
iter_timeout: 30m
review: requirements, quality      # lenses; add security for anything touching trust or I/O
review_model: opus
review_rounds: 1     # a *reject* verdict is handed back once; "concerns" just completes
---
```

Optional keys: `model`, `planner_model`, `extend_iter`, `base`, `egress`, `image`,
`accept`, `gate_rounds`, `depends_on`, `owns`. The embedded template
(`internal/home/spec-template.md`) is the reference for what each one means. Only add a key
when it changes something. Unknown keys get a lint warning.

For the nightshift repo itself, the verify command is:
`test -z "$(gofmt -l cmd internal)" && go vet ./... && HOME=$(mktemp -d) GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 go test -count=1 ./...`

## Sections, in this order

`# <imperative title>`, then `## Goal`, `## Context`, `## Constraints`, `## Tasks`,
`## Acceptance criteria`, `## Out of scope`. Lint fails without Goal, Context, Tasks,
Acceptance criteria and Out of scope, and warns without Constraints. An HTML comment under
the title is the place for provenance: which review or run it came from, and any merge
prerequisite.

- **Goal:** one paragraph saying what is true when it's done. Needing "and" several times
  means it's two specs.
- **Context:** what the agent can't see quickly. Use real file, function and test names
  from the code, with "around line N" hints. Quote exact error text. Give the evidence
  behind any bug claim (which log or run shows it), and say why the change is worth making.
  Read the code before writing this. Never guess a name you could look up.
- **Constraints:** no edits to `SPEC.md`, no new dependencies,
  hermetic tests (temp homes, never the real `$HOME`), the no-op notify guard stays, and
  "never weaken a test; extend it". State what must stay byte-for-byte unchanged.
- **Tasks:** 3–6 numbered `- [ ] N. **Title.**` items, each one committable on its own.
  Under each, sub-bullets give the behavior, exact messages and flag names, then a
  `Tests:` list naming each test and what it asserts. The last task is always:
  `**Parity pass.** Check every item in tasks 1–N against the code, record each one in
  PROGRESS.md, then touch .spec-complete.`
- **Acceptance criteria:** `- [ ]` items that a task or a named test produces. End with
  "Every test named above exists and passes, and the verify command is green."
- **Out of scope:** the tempting adjacent work, named.

## Traps that have cost real runs

- **`owns` is strict.** Any change outside it resets the whole iteration, including test
  helpers (`harness_test.go`), new files the agent will obviously create (`screenshots.go`,
  `*_test.go`) and `testdata/`. List them, or leave `owns` out.
- **Base.** The run's base is `origin/<base>`, so merging locally isn't enough: push before
  running a spec that builds on new work. When the prerequisite isn't merged, either say so
  in the header comment or use `depends_on: <slug>` (the dependency must have ended
  `complete`).
- **Resumes keep the old base and the old clone.** After main moves, or when a QUESTION.md
  is stale, use `nightshift run <slug> --fresh`.
- **Editing the spec changes its hash.** The next run plans again, and a QUESTION.md from
  before the edit is cleared. Edit the spec to answer a question.
- **Time-dependent tests.** Anything involving `--until`, usage limits or "resets 7am" can
  pass by day and hang at night. Inject the clock and the sleeper, and make sure the test
  harness resolves the same settings `Run` does.
- **Planner discipline.** Planning passes must only write PROGRESS.md. A weak planner
  model may write code, which gets reset, or leave a half plan. Prefer `opus` for planning.
- **Fix the root, not the symptom.** When a review finding comes from older code that also
  affects normal runs, say so and fix it there.
- **Follow-up specs from reviews.** Verify every finding against the code first. Separate
  real bugs from test-strength and style items. Drop findings that are wrong, for example
  "verify not confirmed" when runner.log shows `verify GREEN` at the reviewed commit. Batch
  small leftovers into one spec, and keep `docs/backlog.md` current with what's deferred.

## Before handing it over

Run through the interview checklist (`internal/interview/checklist.md`): verify passes on
the base today, one repo per spec, every acceptance criterion maps to a task or test,
`max_iter` ≥ tasks + 2, nothing silently depends on the machine, and no template
placeholders remain. Then:

```sh
nightshift lint <slug>
nightshift ready <slug> --plan      # plans now, surfaces any QUESTION.md immediately
nightshift run <slug> --dry-run     # check base:, models, review and gate lines
```
