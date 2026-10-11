# nightshift

nightshift is an overnight coding agent with guardrails. You write a design
spec, move it to `~/.nightshift/specs/ready/`, and `nightshift run` hands it
to Claude Code inside a sandboxed Ralph loop: each iteration gets a fresh
context, and the agent's only memory across iterations is `PROGRESS.md` plus
git history. The *runner* — not the agent — enforces the spec's verify
command, resets a red tree to the last green commit, caps iterations, kills a
hung iteration, detects stalls, and reports the outcome back as a result
note. It never pushes; the only write to your source repo is a `spec/<slug>`
branch (or, if that branch is checked out elsewhere, a pending
`refs/nightshift/<slug>` ref).

Platform: Linux only. [Docker Sandboxes](https://sbx) (`sbx`) must be
installed on the host; `claude` exists only inside the sandbox template — the
host, and this binary, never run `claude` directly.

## Install

```sh
make build          # bin/nightshift
make install         # go install ./cmd/nightshift
```

`version` is baked in via `-ldflags` from `git describe --always --dirty`; a
plain `go build` without the Makefile reports `nightshift dev`.

## First run checklist

1. `nightshift init` — creates `~/.nightshift` (or `$NIGHTSHIFT_HOME`), the
   embedded spec template, and a fully-commented config file. Warns (but
   doesn't fail) if `sbx` isn't on `PATH` yet.
2. `nightshift new <slug> --repo <path-to-your-checkout> --verify '<command>'`
   — starts a draft from the template at `specs/draft/<slug>.md`. Open it and
   fill in `## Goal`, `## Tasks`, `## Acceptance criteria`, `## Out of
   scope`. Make `verify:` strict — it's the only thing keeping an unattended
   run from wandering.
3. `nightshift ready <slug>` — moves it to `specs/ready/`, refusing if
   `repo:` or `verify:` is still blank.
4. `nightshift run <slug> --dry-run` — resolves everything (base branch,
   model, egress, locks already held) and prints the exact plan — sandbox
   names, `sbx` argv, both prompts — without touching anything. Read it.
5. `nightshift run <slug>` — the real thing, in the foreground, so you can
   watch it. Review the diff it leaves on `spec/<slug>` before you merge
   anything.
6. Only once you've hand-run and reviewed several specs: consider the
   [scheduling](#scheduling) section below.

## Subcommands

```
nightshift init                       create the nightshift home (~/.nightshift or $NIGHTSHIFT_HOME)
nightshift new <slug> [--repo PATH] [--verify CMD] [--no-interview]
                                      start a draft and interview you to write it
nightshift refine <slug>              reopen the interview on a draft
nightshift ready <slug> [--no-verify] [--no-lint]
                                      lint a draft, then move it to specs/ready/
nightshift lint <spec> [--no-verify]  check a draft or ready spec (verify runs on the base commit in sbx)
nightshift done <slug>                move a ready spec to specs/done/
nightshift run <spec> [flags]         run one ready spec (nightshift run -h for flags)
nightshift run --next [flags]         run the oldest eligible ready spec
nightshift status [<slug>]            list runs, or show one run in detail
nightshift review <slug> [--model M]  sandboxed Claude review of a run's work
nightshift version                    print the version
```

Every command except `run` exits `0` on success, `1` on error (printed as
`nightshift: <error>` on stderr), `2` on a usage error. `-h`, `--help`, and
`help` print the top-level usage to stdout and exit `0`.

### `init`

Idempotent. Creates every directory under the home layout (below) and, only
if missing, the embedded spec template and a config file with every known
key listed, commented out, next to its default. Never overwrites an
existing template or config.

### `new <slug> [--repo PATH] [--verify CMD] [--no-interview]`

`<slug>` must match `^[a-z0-9][a-z0-9._-]*$`. Refused if a spec with that
slug already exists in `draft/`, `ready/`, or `done/`. Copies
`~/.nightshift/spec-template.md` (or the binary's embedded default if you
haven't customized it), stamps `created:` to now, and substitutes `repo:`/
`verify:` if given. Flags may come before or after the slug.

After the draft is created, `new` interviews you to write it — see
[Interview](#interview) below — but only if every one of these holds:
not `--no-interview`; stdin and stdout are both TTYs; a host `claude` is
found on `PATH` (or `NIGHTSHIFT_HOST_CLAUDE_BIN`); `--repo` was given; and
that path is a git repository. The first one of these that fails prints a
one-line hint to stdout and exits `0` instead, leaving the draft for you
to edit by hand:

| Reason | Hint |
|---|---|
| `--no-interview`, or no TTY | `edit the draft, then: nightshift lint <slug>` |
| no `claude` on `PATH` | `claude not found on PATH — edit the draft by hand, then: nightshift lint <slug>` |
| no `--repo` | `pass --repo PATH to be interviewed, or edit the draft by hand` |
| `--repo` isn't a git repo | `--repo <path> is not a git repository — fix it, or edit the draft by hand` |

### `refine <slug>`

Reopens the interview on an existing draft, this time asking the session
to critique it against the [spec checklist](#interview) before continuing
the conversation. The draft must already exist in `specs/draft/`, else
`nightshift: no draft named <slug> in specs/draft/ — 'refine' only works
on drafts`, exit `1`. Its frontmatter `repo:` is read (not a `--repo`
flag); if it's blank or not a git repository: `nightshift: set 'repo:' in
the draft to a git checkout first`, exit `1`. Unlike `new`, there's no
non-interactive fallback: without a TTY or without `claude` on `PATH`,
`refine` errors `nightshift: refine needs an interactive terminal and
claude on PATH` and exits `1` rather than printing a hint.

### `ready <slug> [--no-verify] [--no-lint]`

Lints the draft (see `lint` below), then moves `specs/draft/<slug>.md` to
`specs/ready/<slug>.md`. The lint output always prints before anything
else. Any `FAIL` line refuses the move: `nightshift: <slug> failed lint —
not moved to specs/ready/`, exit `1`, draft left untouched. `--no-verify`
passes through to lint, skipping check 8. `--no-lint` restores the old
behavior exactly — only the `repo:`/`verify:`-non-empty check, no lint
call at all — for when you already know the spec is fine and don't want
to wait on a sandboxed verify run.

### `lint <spec> [--no-verify]`

Checks a draft or ready spec — by slug (tried in `draft/` then `ready/`)
or by path — without moving it anywhere. Prints one line per check, then
`lint: <N> failed, <N> warnings`; exits `0` with zero FAILs, `1` with any
FAIL or an operational error (spec not found, unreadable), `2` on a usage
error.

| # | check | FAILs when | warns when |
|---|---|---|---|
| 1 | `frontmatter` | `repo:`/`verify:` empty, `max_iter`/`iter_timeout` unparsable, or `repo:` isn't a git repo | an unrecognized frontmatter key (one warn per key) |
| 2 | `sections` | `## Goal`, `## Context`, `## Tasks`, `## Acceptance criteria`, or `## Out of scope` is missing | `## Constraints` missing |
| 3 | `placeholders` | the template's `<short imperative title>`, an empty `- [ ]` line, or a bare `- ` bullet survived into the body | — |
| 4 | `tasks` | `## Tasks` has no `- [ ]` items | more than 8 — consider splitting the spec |
| 5 | `acceptance` | `## Acceptance criteria` has no `- [ ]` items | — |
| 6 | `max_iter` | — | `max_iter` is less than tasks + 2 (no room for a planning iteration plus a retry) |
| 7 | `base` | the spec's `base:` doesn't resolve in `repo:` | — |
| 8 | `verify-on-base` | the verify command fails on the resolved base commit | — (skipped on `--no-verify`, or after a check 1/7 FAIL) |

Check 7 fetches `origin` first, best-effort, the same way `run` does — so
a stale local `origin/main` doesn't make lint resolve and verify a
different commit than a run would start from.

Check 8 is the one that matters most: it's the only check that actually
runs your `verify:` command, inside a throwaway sandbox, against the
exact commit a run would start from. A spec whose verify is already red
on its base fails its first real iteration before any work happens, and
a second red iteration right after ends the run `red` — lint catches
that before you ever start a run. It clones `repo:` with `--no-local`
into a temp dir under `runs/.lint/` (never hardlinked — a `-ctl`-style
verify command must not be able to corrupt the source repo through
shared objects), checks out the base SHA detached, drops the `origin`
remote, and runs verify in a sandbox named `nightshift-lint-<slug>-
verify` — distinct from a real run's `nightshift-<slug>-verify`, so
linting a spec never collides with a run of the same spec in progress.
Both the clone and the sandbox are removed afterward, success or failure.
`--no-verify` skips it outright (useful when `sbx` isn't available, or
you just want the static checks); it's also skipped if the frontmatter
or base checks already failed, since there's nothing valid to verify
against.

### `done <slug>`

Moves `specs/ready/<slug>.md` to `specs/done/<slug>.md`, refusing while that
spec's lock is held (i.e. it's running) or a done spec with that slug
already exists. Run state under `runs/` is never touched or moved.

### `run <spec-name|path> [flags]` / `run --next [flags]`

```
usage: nightshift run <spec-name|path> [--fresh [--yes]] [--max-iter N] [--until HH:MM] [--base REF] [--max-concurrent N] [--dry-run]
       nightshift run --next [--until HH:MM] [--max-concurrent N] [--dry-run]
  -base string
        branch/ref/sha the run starts from (default: spec base:/NIGHTSHIFT_BASE/origin HEAD)
  -dry-run
        resolve, validate, and print the plan; run nothing
  -fresh
        discard prior state for this spec and start over
  -max-concurrent string
        max simultaneous runs (default: NIGHTSHIFT_MAX_CONCURRENT/2)
  -max-iter int
        hard cap on iterations (default: spec/env/10)
  -next
        run the oldest eligible spec in specs/ready/
  -until string
        local HH:MM deadline; the run stops (resumably) at it
  -yes
        assume yes to the --fresh confirmation
```

Flags may appear before or after the spec name. Without `--next`, exactly
one positional (a slug, a bare filename, or a path) is required; with
`--next`, none is. Either violation exits `1` with the usage printed. An
unparsable `--until` (anything that isn't `HH:MM` in 24h local time) is a
usage error too — `nightshift: --until "soon": want HH:MM (24h, local
time)` — and creates nothing.

Name resolution tries, in order: the argument as a literal path (relative to
your current directory), then that name inside `specs/ready/`, then that
name with `.md` appended inside `specs/ready/` — the last two matched
case-insensitively, so `DEMO`, `demo`, and `DEMO.MD` all find the same file.
Resolving outside `specs/ready/` is refused: a path still in `specs/draft/`
says so and names the `ready` command to fix it; anywhere else says `spec
must live under <ready-dir> (got <path>)`.

Re-running a spec that already completed with an unchanged hash is refused
(`spec <slug> already completed on branch spec/<slug> — move it to
specs/done/ or re-run with --fresh`) unless you pass `--fresh`. Any other
prior status resumes automatically: the existing clone, branch, and
`run.json` are reused as-is. `--fresh` discards that state (and, after a
confirmation prompt unless `--yes`, deletes `spec/<slug>` and the pending
sync ref from your source repo too) and starts clean.

#### Exit codes

| Status | Exit code | Meaning |
|---|---|---|
| `complete` | 0 | every acceptance criterion met, verify green |
| (dry-run / nothing eligible for `--next`) | 0 | — |
| error | 1 | the runner itself hit a problem (see stderr) |
| `blocked` | 2 | the agent wrote `QUESTION.md` and stopped |
| `red` | 3 | two consecutive iterations left verify red |
| `stalled` | 4 | two iterations made no progress |
| `capped` | 5 | hit `max_iter` before completing |
| `window-closed` | 6 | the `--until` deadline arrived mid-run |
| `busy` | 7 | another process already holds this spec, or every concurrency slot is full |
| `interrupted` | 8 | SIGINT/SIGTERM arrived; resumable |
| `rate-limited` | 9 | the Claude usage limit's reset falls outside `--until`, is too far away with no `--until`, or hit too many times in a row; resumable |

`blocked`, `red`, `stalled`, `capped`, `window-closed`, `interrupted`, and
`rate-limited` are all resumable — just run the same spec again (or let
`--next` pick it up; `--next` only skips a spec whose *last* status was
`blocked` or `complete` with an unchanged content hash).

A Claude usage limit is detected at the first iteration (worker or
planning) whose agent result reports a 429 or a message matching `hit
your ... limit` — no verify runs that iteration, and the limit message is
saved as `stop_reason` in `run.json`. What happens next depends on when
the limit resets:

- **Inside the run's `--until` window** (and this isn't the
  `NIGHTSHIFT_LIMIT_MAX_HITS`-th limit in a row with no green iteration in
  between — a guard against a pattern that misfires), the run **waits**
  for the reset plus a 2-minute margin, then starts a fresh session and
  carries on. Nothing is lost, and that iteration counts toward neither
  `max_iter` nor the no-progress counter. While waiting, `nightshift
  status` shows that run as `waiting (until 15:04)` instead of `running`.
  A message with no parseable reset time waits `NIGHTSHIFT_LIMIT_FALLBACK`
  from when the limit was hit instead.
- **Outside the window** — the reset is later than `--until`, more than
  `NIGHTSHIFT_LIMIT_MAX_WAIT` away with no `--until` set, or this is the
  `NIGHTSHIFT_LIMIT_MAX_HITS`-th limit in a row with no green iteration
  between — the run stops `rate-limited` instead. The result note's
  summary and runner.log show the reset time when one was found; a
  message with no parseable reset time falls back to the plain "re-run
  after it resets" wording. Either way, re-run the spec (or let `--next`
  pick it up) and it resumes exactly like any other resumable status;
  `--next` treats a stopped `rate-limited` run the same as `stalled`.

Every limit hit, waited-out or not, writes `<state dir>/.usage-limit`, a
small JSON marker recording the reset time, whether it's a weekly limit,
when it was recorded, and which spec hit it (an existing marker with a
later reset is left alone). `run <spec>` and `run --next` check this
marker before creating any sandbox, after taking their locks: an active
limit that can't be waited out prints `usage limit active until <Mon
15:04> (recorded by <slug>); not starting` and exits `9` without touching
sandboxes, a result note, or that run's own `run.json` status; one that
can be waited out prints `usage limit active; waiting until <time>` and
waits exactly like a limit hit mid-run. `--dry-run` prints `usage limit:
none` or `usage limit: active until <Mon 15:04>` and never deletes the
marker, even an expired one. Deleting `.usage-limit` by hand is always
safe — the next run just finds no marker.

An overload or 5xx result (a transient API error, distinct from a usage
limit) retries the same iteration — same session, same prompt — up to
three times, waiting 1m, then 5m, then 15m between attempts through the
same injectable sleeper, so these waits don't count toward `max_iter`
either. A retry whose wait would land after `--until` closes takes the
window-closed path instead of retrying. A fourth consecutive transient
error falls through to the normal failed-agent handling (verify skipped,
counted as no-progress).

Two refinements to "two iterations made no progress": a session only
counts as having planned once the planner's agent call exits cleanly *and*
PROGRESS.md actually changed — a planner that errors out or leaves
PROGRESS.md untouched counts as no-progress like any other failed
iteration, and the *next* session plans again rather than skipping
straight to work with no plan. And a worker iteration whose agent call
failed (error, timeout, or an `is_error` result) and left no commit and no
uncommitted change (other than to `PROGRESS.md`/`QUESTION.md`/
`.spec-complete`) skips verify entirely rather than re-verifying an
unchanged tree — it still counts toward the no-progress total, just
without the wasted verify run.

### `status`

With no argument: one table, columns `SLUG STATUS ITER COMMITS UPDATED
BRANCH`, over every run under `~/.nightshift/runs/`. `STATUS` shows `running
(pid N)` while that spec's lock is actually held, regardless of what
`run.json` last recorded. After the table, two more lines appear when
non-empty: `ready, not started: <slugs>` (specs in `specs/ready/` that have
never been run) and `drafts: <slugs>`. With nothing at all yet, it prints a
one-line hint to run `nightshift new`.

With a slug: every `run.json` field, then `state dir`, `clone`, `runner
log`, `agent log`, the newest result note (or `none`), `latest review:
<verdict> — <path>` (or `none`), and the exact `git log`/`diff` command to
review the work. An unknown slug exits `1`.

### `review <slug> [--model M]`

Runs one headless, read-only Claude review of a run's work, independent of
that run's own status, and writes a review note — see
[Review](#review) below for the trust model and verdict meanings. Needs at
least one commit since base (`nightshift: nothing to review: <slug> has no
commits since base`); takes the same per-spec lock `run` does, so it
refuses with the same busy message and exit `7` while a run is in
progress. Prints `review: <verdict> — <note path>` to stdout once a note
is written — including a failed review, where the note's verdict is
`error` and the exit code is `1`. `--model` takes precedence over the
spec's `review_model:` frontmatter, which takes precedence over
`NIGHTSHIFT_REVIEW_MODEL`, config, then `opus`.

### `version`

Prints `nightshift <version>`. `run` stamps that same version into
`run.json` (`nightshift_version`), the runner.log `start:`/`resume:` lines,
and the result note's `nightshift:` frontmatter field — so a note or state
dir from months ago still says which binary produced it. An empty version
(a `go build` with no `-ldflags`) is recorded as `dev`.

## Interview

`nightshift new` and `nightshift refine` hand off to a host `claude`
session (not the sandboxed `claude` a run uses — this one runs directly
on your machine, interactively) whose only job is to interview you and
write the spec into the one draft file you started from. It replaces the
`nightshift` process (`syscall.Exec`), so once it starts, `nightshift`
prints nothing further — just the one stderr line right before the
handoff: `nightshift: starting spec interview for <slug> (draft: <path>)
— exit Claude when done, then run: nightshift lint <slug>`.

**What the session can and cannot do, and why.** The owner writes specs
in Claude Desktop because Claude Code kept drifting into implementing
them. The fix here is structural, not a prompt instruction the model
could ignore:

- `--tools Read,Grep,Glob,Edit,Write` is the session's entire built-in
  tool set — there is no Bash, so it cannot run a command even if asked.
  It can read and search your repo freely (grounding the spec in real
  file, function, and test names), but editing is the only kind of
  *write* available.
- `--disallowedTools mcp__*` removes every MCP tool, so no MCP server
  configured on your machine can be used to work around the missing
  shell.
- `--settings <Home>/interview/<slug>/settings.json` allows `Edit` (which
  covers `Write` too) only on the one draft file, and explicitly denies
  it everywhere else nightshift keeps state — your repo, `specs/ready/`,
  `specs/done/`, `runs/`, `results/`, `context/`, `interview/`, `config`,
  and `spec-template.md`. Deny beats allow, so even if a future tool
  grants broader access, these paths stay closed. The permission-bypass
  flag (`--dangerously-skip-permissions`/`bypassPermissions`) is never
  passed — the deny rules always apply.
- `--append-system-prompt-file` *adds* to Claude Code's default system
  prompt rather than replacing it, so the usual safety rules and tool
  guidance stay in effect; the appended file carries the interview's own
  role, instructions, and context instead of a full prompt rewrite.
- `--permission-mode dontAsk` makes every call that would otherwise
  prompt you get denied automatically instead — a tool outside the
  built-in set, a read outside the working directories (e.g. the Go
  module cache), or anything not covered by the `permissions.allow`
  Edit rule above is refused outright, with no prompt for you to approve
  by accident. This is what makes the deny rules self-enforcing rather
  than relying on you noticing and declining a permission prompt; it
  also denies the `AskUserQuestion` tool, which this session has no use
  for since it interviews you directly in conversation.

**Context, and how to curate it.** Every launch regenerates
`<Home>/interview/<slug>/prompt.md` from scratch — nothing about it is
state (the file itself does persist on disk, but it's overwritten
completely on the next `new`/`refine`, so there's nothing to clean up by
hand). It has ten sections, always in this order: your role
(`internal/interview/instructions.md`, embedded, verbatim), this
session's slug/draft/repo/date/mode, the exact planning and iteration
preambles a nightshift agent sees during a real run, the spec checklist
(`internal/interview/checklist.md`, embedded, verbatim), the current spec
template, `<Home>/context/sandbox.md`, `<Home>/context/lessons.md`,
`<Repo>/.nightshift.md`, the newest 3 result notes under
`<Home>/results/` whose `repo:` frontmatter matches this repo, and the
newest 2 completed specs under `specs/done/`. A source that doesn't exist
yet renders as `(none yet)`; each file-sourced section is capped (16 KiB
for context/repo-card/done-example sections, 6 KiB per result note) and,
if truncated, ends with a `[truncated — read <path> for the rest]`
pointer back to the full file.

The two you're expected to hand-edit are `context/sandbox.md` and
`context/lessons.md` — `nightshift init` seeds both once from built-in
defaults (see the layout below) and never touches either again, so
anything you add (a sandbox quirk you hit, a lesson from a run that went
sideways) is there for every future interview on every spec, across every
repo. `<Repo>/.nightshift.md` (a "repo card") is the same idea but
per-repo and entirely your responsibility to write — nightshift never
creates one.

**Refine.** `nightshift refine <slug>` reopens the interview on an
existing draft with a different opening move: instead of asking what you
want first, it reads the draft and tells you its weakest points against
the spec checklist before continuing the conversation. See the `refine`
subcommand above for its preconditions.

**Resuming.** Every interview session is named `spec-<slug>` (the
`-n spec-<slug>` flag), so if you exit Claude mid-interview without
finishing, `claude --resume spec-<slug>` picks the conversation back up
with full history — you don't have to start over, and you don't need
`nightshift refine` just to continue a session you haven't left yet.

**Configuration.** Two config keys govern the interview, both env >
config > default like the other bin/model settings: `NIGHTSHIFT_HOST_CLAUDE_BIN`
(default `claude`) names the *host* `claude` binary the interview execs —
deliberately separate from `NIGHTSHIFT_CLAUDE_BIN`, which names `claude`
*inside* the sandbox a run uses and must never be reused here; and
`NIGHTSHIFT_INTERVIEW_MODEL` (default `opus`) sets the interview's
`--model`, independent of a run's `model:`/`planner_model:`.

## Home directory layout

`$NIGHTSHIFT_HOME`, or `~/.nightshift` if unset:

```
~/.nightshift/
  config                 optional KEY=value file
  spec-template.md       written by `init` if missing; `new` copies it
  specs/
    draft/               `new` writes here
    ready/                the only place `run` accepts specs from
    done/                 `done` moves specs here; the runner never does
  context/                sandbox.md/lessons.md; seeded by `init`, hand-edited after
  interview/              <slug>/{settings.json,prompt.md}; regenerated every launch
  results/                <slug>-<YYYY-MM-DD>[-N].md result notes
  runs/                   state dirs + <slug>/run.json/runner.log/agent.log, and .locks/
  logs/                   nightly.log (written by the systemd wrapper, not the binary)
```

Nothing is ever written to your source repo except the `spec/<slug>` branch
and, when that branch happens to be checked out elsewhere, the
`refs/nightshift/<slug>` pending ref. If you `git init` your `~/.nightshift`
directory yourself, result notes get committed there automatically (one
commit per result, touching only that note); if you don't, they're just
written as plain files — either way nothing else about the run depends on
it.

`runner.log` holds plain-text lines, `<date> <time> <LEVEL> <message>`,
written via `logx.NewText`. A `SECURITY:`-prefixed message logs at `WARN`;
`error:`/`hand-back error:` messages log at `ERROR`; everything else is
`INFO`. For example:

```
2026-10-05 21:52:05 INFO  start: repo=~/dev/myapp branch=spec/demo base=1a2b3c4d version=v1.2.3
2026-10-05 21:52:05 INFO  iter 2 verify GREEN at cb9eef57
```

`agent.log` holds the agent's raw output instead, unformatted.

## Configuration

Precedence, highest first: **CLI flag > spec frontmatter > `NIGHTSHIFT_*`
env var > `~/.nightshift/config` > built-in default.** (`max_concurrent` has
no frontmatter tier — flag > env > config > `2`. `NIGHTSHIFT_SBX_BIN`/
`NIGHTSHIFT_CLAUDE_BIN`/`NIGHTSHIFT_NOTIFY`/`NIGHTSHIFT_HOST_CLAUDE_BIN`/
`NIGHTSHIFT_INTERVIEW_MODEL` have no flag or frontmatter tier at all —
env > config > default.)

| Spec frontmatter | Env var | Config key | Default |
|---|---|---|---|
| `model:` | `NIGHTSHIFT_MODEL` | `NIGHTSHIFT_MODEL` | `sonnet` |
| `planner_model:` | `NIGHTSHIFT_PLANNER_MODEL` | `NIGHTSHIFT_PLANNER_MODEL` | `opus` |
| `max_iter:` | `NIGHTSHIFT_MAX_ITER` | `NIGHTSHIFT_MAX_ITER` | `10` |
| `iter_timeout:` | `NIGHTSHIFT_ITER_TIMEOUT` | `NIGHTSHIFT_ITER_TIMEOUT` | `30m` |
| `base:` | `NIGHTSHIFT_BASE` | `NIGHTSHIFT_BASE` | repo's `origin/HEAD`, then `main`, then `master` |
| `review:` | `NIGHTSHIFT_REVIEW` | `NIGHTSHIFT_REVIEW` | `false` |
| `review_model:` | `NIGHTSHIFT_REVIEW_MODEL` | `NIGHTSHIFT_REVIEW_MODEL` | `opus` |
| — | — | `NIGHTSHIFT_MAX_CONCURRENT` | `2` |
| — | `NIGHTSHIFT_SBX_BIN` | `NIGHTSHIFT_SBX_BIN` | `sbx` |
| — | `NIGHTSHIFT_CLAUDE_BIN` | `NIGHTSHIFT_CLAUDE_BIN` | `claude` |
| — | `NIGHTSHIFT_NOTIFY` | `NIGHTSHIFT_NOTIFY` | `on` (`off` disables desktop notifications) |
| — | `NIGHTSHIFT_HOST_CLAUDE_BIN` | `NIGHTSHIFT_HOST_CLAUDE_BIN` | `claude` (the *host* claude the interview execs; see [Interview](#interview)) |
| — | `NIGHTSHIFT_INTERVIEW_MODEL` | `NIGHTSHIFT_INTERVIEW_MODEL` | `opus` (the interview's `--model`) |
| — | `NIGHTSHIFT_LIMIT_FALLBACK` | `NIGHTSHIFT_LIMIT_FALLBACK` | `60m` — wait when a usage-limit message has no parseable reset time |
| — | `NIGHTSHIFT_LIMIT_MAX_WAIT` | `NIGHTSHIFT_LIMIT_MAX_WAIT` | `6h` — longest wait for a usage limit when no `--until` is set |
| — | `NIGHTSHIFT_LIMIT_MAX_HITS` | `NIGHTSHIFT_LIMIT_MAX_HITS` | `3` — consecutive limit hits with no green iteration between before stopping anyway |
| `repo:`, `verify:`, `egress:` | — | — | frontmatter only, required (`repo:`/`verify:`) |

The config file is `KEY=value` lines; blank lines and lines starting with
`#` (after leading whitespace) are skipped; one pair of matching `"` or `'`
around a value is stripped. An unknown key or a line with no `=` is a hard
error naming the file and line number — `init` writes every valid key as a
commented-out example so you always have the full list on hand.

Frontmatter values get the same quote-stripping. `egress:` is a
comma-separated list of extra hosts the sandbox may reach, on top of the
baseline allowlist (the Anthropic API plus the usual language-package
registries).

## Sandbox model

Every run uses three `sbx` sandboxes, all mounting the same clone at the
same path, each with a different trust role:

- **agent** (`nightshift-<slug>`) — runs `claude`. Reused across
  iterations; nothing the runner trusts lives here.
- **control** (`nightshift-<slug>-ctl`) — every git operation on the clone
  runs here, never on the host and never in the agent or verify sandbox.
  The clone's own `.git/config`/hooks are agent-writable and could be
  booby-trapped; running git here, not on the host, contains that.
  The clone itself is also made with `git clone --no-local`, so its
  objects are real copies rather than hardlinks into the source repo's
  `.git/objects` — without that, the agent (root inside its own sandbox)
  could overwrite a shared inode and corrupt the source repo's history.
- **verify** (`nightshift-<slug>-verify`) — the spec's `verify:` command
  runs here, in a sandbox recreated from a clean image before *every*
  single verify call, so nothing a previous iteration (or verify itself)
  did to the toolchain carries forward.

The agent is always stopped (`sbx stop`) before any verify or git op in the
same iteration, including a timed-out one, so a lingering process can't race
or tamper with the result.

`PROGRESS.md`, `QUESTION.md`, and `.spec-complete` are read and written on
the host, but only via `Lstat` + `O_NOFOLLOW` — an agent that replaces one
with a symlink gets the link itself removed/ignored, never the symlink's
target followed.

## Post-run checks

Every result note ends with a `## Post-run checks` section — six checks,
in this order, each printed with lint's own `ok`/`warn`/`FAIL`/`skip`
line format. They run once, after the sandboxes are removed and the final
sync attempt has happened, and they never change the run's status or exit
code — they exist to catch the kind of thing a human used to check by hand
after reviewing a run.

| check | ok when | FAIL / warn / skip |
|---|---|---|
| `sync` | the source repo's branch ref (or pending ref, if a checkout elsewhere means sync is pending) matches `last_green`; or nothing was ever green and the ref is simply missing | FAIL naming both short SHAs; skipped if the final sync attempt itself failed |
| `scaffold` | no commit between the base and `last_green` touches `PROGRESS.md`, `QUESTION.md`, or `.spec-complete` | FAIL listing the offending commits and files |
| `clone-remotes` | the clone has no git remotes | FAIL listing them |
| `clone-objects` | every object file under the clone's `.git/objects` has a link count of 1 (a real copy, not hardlinked into the source repo) | FAIL with the hardlinked count; skipped on non-Linux |
| `sandboxes` | the explicit post-loop `sb.Remove()` returned no error | FAIL with that error |
| `version` | always ok | warns when the version is `dev` or ends in `-dirty` |

A FAIL in any check makes the desktop notification urgent and appends
`(checks failed)` to its title, but the run's status, exit code, and
`run.json` are exactly as they'd be without this section.

## Review

`nightshift review <slug>` runs a headless, read-only Claude code review of
a run's work — on demand, or automatically when a spec with `review: true`
in its frontmatter ends `complete`. Either way, a review only ever adds a
review note; it never changes the run's status, exit code, `run.json`, or
history.

**Trust model.** A review reads code an untrusted agent wrote, which could
contain text aimed at manipulating the reviewer — so, like the agent
itself, the review never runs on the host. It runs inside its own sbx
sandbox, `nightshift-<slug>-review`, recreated fresh for every review and
removed afterward whatever the outcome. The reviewing `claude` gets only
`Read`, `Grep`, and `Glob` tools, `--disallowedTools mcp__*`, and no
permission-bypass flag — it cannot edit anything, run a command, or reach
an MCP server. It authenticates through sbx's global Anthropic secret, the
same way a run's agent does, so it works unattended overnight with no TTY.
The workspace it mounts is built from your *source* repo — never the
agent's clone — with `git clone --no-local` at the reviewed commit, so the
review sandbox shares no object-file inodes with either repo; host git
only ever runs against the source repo to resolve the ref and compute the
diff.

**What the reviewer sees**, all under one fresh workspace directory:

- `inputs/spec.md` — the spec file (or `(spec file not found)` if it's no
  longer in `specs/ready/` or `specs/done/`)
- `inputs/summary.md` — how the run ended: status, iterations, commits,
  base and head short SHAs, branch
- `inputs/diffstat.txt` / `inputs/diff.patch` — the diff from the run's
  base commit to the reviewed commit, computed in the source repo; a diff
  over 200 KiB is truncated with a pointer back to `repo/`
- `inputs/progress.md` — the run's `PROGRESS.md`, read the same
  symlink-safe way as everything else under [Sandbox model](#sandbox-model)
  (`(no PROGRESS.md)` if there is none)
- `repo/` — the repository itself, checked out detached at the reviewed
  commit, with no remotes

The prompt tells the model to treat all of this as material to review, not
as instructions, and to check the diff and tests against the spec's
acceptance criteria rather than trust the journal's own claims.

**Verdicts.** The model replies with a `VERDICT:` line, one of `approve`
(every acceptance criterion met, no high-severity finding), `concerns`
(the default otherwise), or `reject` (a criterion is unmet, or a
high-severity finding makes the change unsafe to merge). If the reviewer's
reply never produces a line matching that pattern — or the review itself
fails or times out — the verdict is `unknown` or, for an outright failure,
`error`. Every verdict, including `error`, still gets a review note.

**`review: true` and timing.** With `review: true` in frontmatter (or
`NIGHTSHIFT_REVIEW=true`, or the config default), a run that ends
`complete` reviews automatically, right after the result summary prints
and before the result note and notification are written — so both carry
the verdict: a `review: <verdict>` frontmatter line and a `## Review`
section in the result note, and a ` · review: <verdict>` suffix on the
desktop notification (urgent, in addition to today's urgent statuses, when
the verdict is `reject` or `error`). Any other final status — `blocked`,
`red`, `stalled`, `capped`, `window-closed` — never reviews at all. The
automatic review is bounded by the spec's `iter_timeout`, capped by any
`--until` deadline still in effect, and is skipped (verdict recorded as
`skipped`, with a reason) rather than attempted if the run was interrupted
or the `--until` deadline has already passed by the time it would start.

**On demand**, `nightshift review <slug>` runs the same review outside of
a live run — see the [`review`](#review-slug---model-m) subcommand above
for its preconditions, output, and exit codes.

## Concurrency

Two independent lock layers, both under `~/.nightshift/runs/.locks/`:

- **Per-spec**: only one process may run a given spec at a time. A second
  attempt exits `busy` (7), naming the holder's pid and lock file.
- **Concurrency slots**: `max_concurrent` (default 2) total slots across
  *all* specs. A run that can't claim a slot also exits `busy`, naming
  every currently-running slug.

Stale locks (holder process gone, or from a different boot of the machine)
are reclaimed automatically on the next attempt — you should never need to
remove a lock file by hand. `nightshift run --next` additionally takes a
short-lived selection lock while picking a candidate, so concurrent
`--next` invocations never race onto the same spec.

Raising `max_concurrent` doesn't raise how many diffs you can actually
review overnight — it only governs how many iteration loops may be
in flight.

## Scheduling

`deploy/` ships three files, **none of them installed or enabled by this
binary or the Makefile**:

- `deploy/nightshift-nightly.sh` — the wrapper systemd actually runs. Copy
  it to `~/.local/bin/nightshift-nightly.sh` and `chmod +x` it.
- `deploy/nightshift.service` / `deploy/nightshift.timer` — a oneshot
  service plus a timer firing at 22:00.

To enable, **only after you've hand-run and reviewed several specs**:

```sh
cp deploy/nightshift-nightly.sh ~/.local/bin/ && chmod +x ~/.local/bin/nightshift-nightly.sh
cp deploy/nightshift.service deploy/nightshift.timer ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now nightshift.timer
```

Two things that are easy to miss:

- The timer only fires while you're logged out if you've run
  `loginctl enable-linger $USER` once.
- Desktop notifications (`NIGHTSHIFT_NOTIFY` other than `off`) from a user
  service need the graphical session's `DBUS_SESSION_BUS_ADDRESS` imported
  into the systemd user manager — uwsm-based sessions already do this;
  other setups may need `systemctl --user import-environment
  DBUS_SESSION_BUS_ADDRESS` (or equivalent) in your session startup.

The wrapper logs to `~/.nightshift/logs/nightly.log` and treats exit `7`
(busy — nothing started) as a non-failure; only exit `1` (a genuine runner
error) marks the systemd unit failed. `blocked`/`red`/`stalled`/`capped`/
`window-closed`/`interrupted`/`rate-limited` are all outcomes, not
failures — they're resumable, and the next night's run picks the same
spec back up via `--next`.

## Recovery

- **See what's running or waiting**: `nightshift status`.
- **Investigate one run**: `nightshift status <slug>` — gives you the
  state dir, both log files, the latest result note, and the exact `git`
  command to review the diff.
- **A run was interrupted or hit a recoverable status**: just run
  `nightshift run <slug>` again; it resumes the same clone and branch.
- **Start a spec over from scratch**: `nightshift run <slug> --fresh`
  (`--yes` to skip the confirmation prompt). This also removes
  `spec/<slug>` and any pending sync ref from your *source* repo — review
  first if you've already looked at that branch.
- **A copy-back (the runner pushing finished work back to your repo)
  failed**: the result note and `nightshift status <slug>` both print the
  exact `git fetch` command to recover the work manually; a resume retries
  the sync automatically on its next green iteration.
- **Stale lock files**: don't delete them by hand — they're reclaimed
  automatically once their holder process is confirmed gone.
- **Which binary produced a run**: `run.json`'s `nightshift_version`, the
  runner.log `start:`/`resume:` lines, and the result note's `nightshift:`
  frontmatter field all record the version that ran — useful when a result
  looks off and you want to rule out a stale install.

### Interrupts

- **One Ctrl+C** stops the run cleanly: nightshift prints
  `nightshift: interrupt received — stopping the agent and saving state
  (press Ctrl+C again to force quit)`, stops the agent, saves state, and
  ends the run `interrupted` with a result note. It's resumable — see
  above.
- **A second Ctrl+C** force-quits immediately, with no cleanup. Any
  sandboxes left behind are removed automatically on the next
  `nightshift run` of that spec; you never need to `sbx rm` them by hand.
- The same automatic sandbox cleanup applies after any other unclean
  exit — a crash, `kill -9`, a reboot, or a systemd timeout.
