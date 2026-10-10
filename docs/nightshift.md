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
nightshift new <slug> [--repo PATH] [--verify CMD]
                                      start a draft spec from the template
nightshift ready <slug> [--no-verify] [--no-lint]
                                      lint a draft, then move it to specs/ready/
nightshift lint <spec> [--no-verify]  check a draft or ready spec (verify runs on the base commit in sbx)
nightshift done <slug>                move a ready spec to specs/done/
nightshift run <spec> [flags]         run one ready spec (nightshift run -h for flags)
nightshift run --next [flags]         run the oldest eligible ready spec
nightshift status [<slug>]            list runs, or show one run in detail
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

### `new <slug> [--repo PATH] [--verify CMD]`

`<slug>` must match `^[a-z0-9][a-z0-9._-]*$`. Refused if a spec with that
slug already exists in `draft/`, `ready/`, or `done/`. Copies
`~/.nightshift/spec-template.md` (or the binary's embedded default if you
haven't customized it), stamps `created:` to now, and substitutes `repo:`/
`verify:` if given. Flags may come before or after the slug.

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

`blocked`, `red`, `stalled`, `capped`, `window-closed`, and `interrupted` are
all resumable — just run the same spec again (or let `--next` pick it up;
`--next` only skips a spec whose *last* status was `blocked` or `complete`
with an unchanged content hash).

### `status`

With no argument: one table, columns `SLUG STATUS ITER COMMITS UPDATED
BRANCH`, over every run under `~/.nightshift/runs/`. `STATUS` shows `running
(pid N)` while that spec's lock is actually held, regardless of what
`run.json` last recorded. After the table, two more lines appear when
non-empty: `ready, not started: <slugs>` (specs in `specs/ready/` that have
never been run) and `drafts: <slugs>`. With nothing at all yet, it prints a
one-line hint to run `nightshift new`.

With a slug: every `run.json` field, then `state dir`, `clone`, `runner
log`, `agent log`, the newest result note (or `none`), and the exact `git
log`/`diff` command to review the work. An unknown slug exits `1`.

### `version`

Prints `nightshift <version>`.

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
2026-10-05 21:52:05 INFO  start: repo=~/dev/myapp branch=spec/demo base=1a2b3c4d
2026-10-05 21:52:05 INFO  iter 2 verify GREEN at cb9eef57
```

`agent.log` holds the agent's raw output instead, unformatted.

## Configuration

Precedence, highest first: **CLI flag > spec frontmatter > `NIGHTSHIFT_*`
env var > `~/.nightshift/config` > built-in default.** (`max_concurrent` has
no frontmatter tier — flag > env > config > `2`. `NIGHTSHIFT_SBX_BIN`/
`NIGHTSHIFT_CLAUDE_BIN`/`NIGHTSHIFT_NOTIFY` have no flag or frontmatter tier
at all — env > config > default.)

| Spec frontmatter | Env var | Config key | Default |
|---|---|---|---|
| `model:` | `NIGHTSHIFT_MODEL` | `NIGHTSHIFT_MODEL` | `sonnet` |
| `planner_model:` | `NIGHTSHIFT_PLANNER_MODEL` | `NIGHTSHIFT_PLANNER_MODEL` | `opus` |
| `max_iter:` | `NIGHTSHIFT_MAX_ITER` | `NIGHTSHIFT_MAX_ITER` | `10` |
| `iter_timeout:` | `NIGHTSHIFT_ITER_TIMEOUT` | `NIGHTSHIFT_ITER_TIMEOUT` | `30m` |
| `base:` | `NIGHTSHIFT_BASE` | `NIGHTSHIFT_BASE` | repo's `origin/HEAD`, then `main`, then `master` |
| — | — | `NIGHTSHIFT_MAX_CONCURRENT` | `2` |
| — | `NIGHTSHIFT_SBX_BIN` | `NIGHTSHIFT_SBX_BIN` | `sbx` |
| — | `NIGHTSHIFT_CLAUDE_BIN` | `NIGHTSHIFT_CLAUDE_BIN` | `claude` |
| — | `NIGHTSHIFT_NOTIFY` | `NIGHTSHIFT_NOTIFY` | `on` (`off` disables desktop notifications) |
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
`window-closed`/`interrupted` are all outcomes, not failures — they're
resumable, and the next night's run picks the same spec back up via
`--next`.

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
