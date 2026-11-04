---
type: spec
project: nightshift
status: draft
created: 2026-10-05T11:15:00-04:00
repo: ~/dev/sbx/devkit/nightshift
verify: test -z "$(gofmt -l cmd internal)" && go vet ./... && go test ./...
max_iter: 40
iter_timeout: 45m
---

# Build nightshift: a standalone, sandboxed spec runner for Linux

<!--
nightshift is the successor to a private tool. The original was one binary inside a
larger module. It ran on macOS under launchd and read its specs from an Obsidian vault.
nightshift is its own module and its own binary, and it keeps everything under
~/.nightshift. It targets Linux (Arch/Hyprland) with a systemd user timer.

The behavioral baseline was the original tool's design doc, which isn't
published in this repo. This file lists every deliberate change from it (the "Deltas").
Where the two disagreed, THIS FILE WON. Anything not changed here was carried over from
the baseline, with the renames in D1 applied.
-->

## Goal

A Go module `github.com/jwbonnell/nightshift` that builds one binary, `nightshift`. It
does what the original tool did: it runs ONE ready design spec through a Ralph loop
inside Docker Sandboxes (`sbx`). The runner, not the agent, enforces verify, red-reset,
stall, iteration caps, timeouts and the `--until` window, and it reports results back.
nightshift also owns its own home directory (`~/.nightshift`) for specs, run state and
results, and has a few small subcommands (`init`, `new`, `ready`, `done`, `status`) so it
works without the companion notes app or Obsidian. It is done when every test named in the Test
checklist exists and passes, and the acceptance criteria below hold.

## Context

- **What it is:** an overnight coding agent with guardrails. You write a spec with
  frontmatter plus Goal / Context / Constraints / Tasks / Acceptance criteria / Out of
  scope, and move it to `specs/ready/`. `nightshift run` then hands it to Claude Code. Each
  iteration gets a fresh context, and the agent's only memory is `PROGRESS.md` plus git
  history. Containment comes from three `sbx` sandboxes (agent / `-ctl` for git /
  `-verify` recreated before every verify) on one clone that has no remotes. The build
  followed the original tool's detailed design (loop pseudocode, sandbox argv, lock
  protocol, copy-back, scaffold symlink safety, verbatim prompts and test harness), which
  isn't published here; the code and docs/nightshift.md now describe that behavior.
- **Platform:** Linux only. `sbx` is installed on the host. `claude` exists only inside
  the sandbox template; the host never runs it.
- **Libraries:** the owner's `github.com/jwbonnell/go-libs` is the FIRST place to look for
  a package. For this tool only `pkg/logx` (structured logging) applies. `dbx` is
  Postgres-only, and nightshift keeps state in files. `web`, `metrics` and `mapper` serve
  an HTTP service, and nightshift has no web surface yet. `validate` would replace the
  verbatim, hand-written frontmatter errors. Everything else is standard library.
- **Not available inside the build sandbox:** the real `sbx`, `claude`, `notify-send` and
  systemd. Each sits behind an interface or an injectable binary path. Tests use stubs and
  fakes, as the reference harness does.

## Constraints

- `go.mod`: `module github.com/jwbonnell/nightshift`, `go 1.25.3` (the go-libs minimum).
  The only direct `require` is `github.com/jwbonnell/go-libs`. Run `go mod vendor` and
  commit `vendor/`, so that `verify` works offline in the fresh verify sandbox.
- Import only `github.com/jwbonnell/go-libs/pkg/logx` from go-libs. Adding another
  third-party dependency needs a one-line justification in PROGRESS.md. A dependency that
  only saves a few lines of std-lib code does not qualify.
- Tests use the std-lib `testing` package only (no testify). Tests must not need network,
  Docker, `sbx`, `claude`, `notify-send`, systemd or a real `$HOME`. Use `t.TempDir()`
  homes and `t.Setenv`.
- Prompts go to the agent on **stdin**, never argv. The agent never runs on the host.
  Never push. Never edit a spec file's content.
- Keep the reference's guarantees: verify is run by the runner; red resets to last green;
  the clone has no remotes; all clone git runs in the `-ctl` sandbox; the agent is killed
  before verify; verify runs in a freshly recreated sandbox; scaffold files are handled
  with `Lstat` + `O_NOFOLLOW`; commit ids are validated; `--dry-run` creates nothing.
- `gofmt` clean, `go vet ./...` clean.

## Deltas from the reference

### D1. Names

Names nightshift uses where the original tool used its own:

| Item | nightshift |
|---|---|
| binary and module | binary `nightshift`, module `github.com/jwbonnell/nightshift` |
| runner package | `internal/runner` (same file split) |
| git helper package (subset) | `internal/gitx`: `Run`, `IsRepo`, `GitDir`, `RunRetry`, `Commit` with the reference's semantics; `Commit`'s lock file is `<git-dir>/nightshift-commit.lock` |
| `internal/lock` | `internal/lock` unchanged, except: `boottime_linux.go` + `boottime_other.go` (`//go:build !linux`); no darwin file |
| base / model / planner model / max iterations / iteration timeout / max concurrency env vars | `NIGHTSHIFT_BASE` / `NIGHTSHIFT_MODEL` / `NIGHTSHIFT_PLANNER_MODEL` / `NIGHTSHIFT_MAX_ITER` / `NIGHTSHIFT_ITER_TIMEOUT` / `NIGHTSHIFT_MAX_CONCURRENT` |
| sbx binary, claude binary and home env vars | `NIGHTSHIFT_SBX_BIN`, `NIGHTSHIFT_CLAUDE_BIN`, `NIGHTSHIFT_HOME` |
| lock test child protocol env vars | `NIGHTSHIFT_LOCK_*` |
| sandbox names | `nightshift-<slug>[-ctl\|-verify]` |
| clone identity | `nightshift` / `nightshift@localhost` |
| pending copy-back ref | `refs/nightshift/<slug>` |
| branch `spec/<slug>` | unchanged |
| spec folders in messages | `specs/ready`, `specs/draft`, `specs/done`, `results` |
| error prefix | `nightshift: %v` |
| result-note `type:` | `type: nightshift-result` |

The two prompt preambles (`iterPreamble`, `planPreamble`) and the `buildPrompt` assembly
stay byte-identical to reference §3.8.

### D2. Home directory

`Home` is `$NIGHTSHIFT_HOME` if set, else `~/.nightshift`. Layout:

```
~/.nightshift/
  config                 optional KEY=value file (D3)
  spec-template.md       written by `init` if missing; `new` copies it
  specs/
    draft/               `new` writes here
    ready/               the only place `run` accepts specs from
    done/                `done` moves specs here; the runner never does
  results/               <slug>-<YYYY-MM-DD>[-N].md result notes
  runs/                  StateRoot (was a dot-directory under the old home): .locks/, <slug>/…
  logs/                  nightly.log (written by the systemd wrapper, not the binary)
```

- `Options.VaultPath` becomes `Options.Home`. `StateRoot` defaults to `<Home>/runs`.
- Readiness gate (replaces reference §3.2.1 step 5): after `filepath.Abs` +
  `filepath.Clean`, the spec path must be inside `<Home>/specs/ready/`. A path inside
  `<Home>/specs/draft/` gives the error
  `%s is still in specs/draft/ — run 'nightshift ready %s' when you mean it` (base
  name, slug). Any other location gives `spec must live under %s (got %s)` (ready dir,
  abs). Name resolution order is unchanged: arg as given, then `ready/<arg>`, then
  `ready/<arg>.md`.
- Every run dir, result note and lock lives under `Home`. Nothing is written to the
  source repo except the `spec/<slug>` branch and the `refs/nightshift/<slug>` pending ref.

### D3. Configuration

Precedence becomes **flag > frontmatter > env > config file > default** for `base`,
`model`, `planner_model`, `max_iter` and `iter_timeout`. `pick` gains a config tier,
and its source label is `config`. `max_concurrent` is **flag > env > config file > 2**
(never frontmatter). `NIGHTSHIFT_SBX_BIN` and `NIGHTSHIFT_CLAUDE_BIN` are **env > config
file > `sbx`/`claude`**. `NIGHTSHIFT_NOTIFY` (D6) is **env > config file > on**.

Config file `<Home>/config` (`internal/home`):
- A missing file is not an error. Lines are `KEY=value`. Blank lines and lines whose first
  non-space char is `#` are skipped. Split at the first `=`, then TrimSpace both sides.
  Strip ONE pair of matching surrounding `"` or `'` from the value. A later duplicate key
  overwrites an earlier one.
- Keys are the env var names from D1 (minus `NIGHTSHIFT_HOME`, which locates the file),
  plus `NIGHTSHIFT_NOTIFY`. An unknown key, or a line with no `=`, is a hard error:
  `config %s line %d: %s` (path, 1-based line, `unknown key %q` / `expected KEY=value`).
- `env` beats `config` for the same key. An empty env value counts as unset (same as the
  reference).

### D4. CLI: subcommands

Top-level dispatch is on `os.Args[1]`. Unknown command, or no command, prints the usage
below to stderr and exits **2**. `-h`, `--help` and `help` print it to stdout and exit
**0**.

```
nightshift — run design specs through a sandboxed Claude Code loop

usage:
  nightshift init                       create the nightshift home (~/.nightshift or $NIGHTSHIFT_HOME)
  nightshift new <slug> [--repo PATH] [--verify CMD]
                                        start a draft spec from the template
  nightshift ready <slug>               move a draft spec to specs/ready/
  nightshift done <slug>                move a ready spec to specs/done/
  nightshift run <spec> [flags]         run one ready spec (nightshift run -h for flags)
  nightshift run --next [flags]         run the oldest eligible ready spec
  nightshift status [<slug>]            list runs, or show one run in detail
  nightshift version                    print the version
```

Exit codes for every command except `run`: 0 ok, 1 error (message `nightshift: %v` on
stderr), 2 usage. `run` keeps the reference exit codes (§3.1.8), plus `interrupted` = 8
(D8).

- **`init`**: `MkdirAll` (0o755) every directory in D2. Write `spec-template.md` and a
  fully commented `config` (every key listed, commented out, each with its default) only
  if they are missing. Never overwrite. Print one line per path created
  (`created %s`), or `nightshift home ready at %s` if nothing was created. If
  `exec.LookPath(sbxBin)` fails, print to stderr
  `warning: %s not found on PATH — runs will fail until it is installed` and still exit 0.
  Idempotent.
- **`new <slug> [--repo PATH] [--verify CMD]`**: the slug must match
  `^[a-z0-9][a-z0-9._-]*$` (error `invalid slug %q: use lowercase letters, digits, '.', '_' or '-'`).
  It is refused if `<slug>.md` already exists in draft, ready or done
  (`spec %s already exists: %s`). Copy `<Home>/spec-template.md`, falling back to the
  embedded template if the file is missing. Replace the frontmatter `created:` value with
  `time.Now()` in RFC3339. Replace `repo:` / `verify:` when the flags are given.
  Write the result to `specs/draft/<slug>.md` (0o644) and print its path. `new` creates
  missing home dirs as `init` would, but prints nothing extra.
- **`ready <slug>`**: the source is `specs/draft/<slug>.md` (error
  `no draft named %s in %s`). Parse its frontmatter and refuse if `repo:` or `verify:` is
  empty (`%s is missing '%s:' — fill it in before marking it ready`). Refuse if
  `specs/ready/<slug>.md` exists. Then `os.Rename` and print `ready: %s`.
- **`done <slug>`**: the source is `specs/ready/<slug>.md`. Refuse if its spec lock is held
  (`spec %s is running (pid %d) — wait for it to finish`). Refuse if
  `specs/done/<slug>.md` exists. Then `os.Rename` and print `done: %s`. Run state is not
  touched.
- **`run`**: exactly the reference CLI (§3.1.2–3.1.5) under the subcommand. The flag set is
  `flag.NewFlagSet("nightshift run", flag.ExitOnError)`, with the same flags, interleaved
  positional parsing, positional rules and exit mapping. Usage lines:
  ```
  usage: nightshift run <spec-name|path> [--fresh [--yes]] [--max-iter N] [--until HH:MM] [--base REF] [--max-concurrent N] [--dry-run]
         nightshift run --next [--until HH:MM] [--max-concurrent N] [--dry-run]
  ```
  followed by `fs.PrintDefaults()`. In flag usage strings, replace `_specs/ready/` with
  `specs/ready/` and the old env var prefix with the D1 names. Insert `home:        %s` as the
  first `--dry-run` line.
- **`status`** (no arg): one `text/tabwriter` table (minwidth 0, tabwidth 8, padding 2,
  `' '`) over `<Home>/runs/*/run.json`, sorted by slug. Columns
  `SLUG STATUS ITER COMMITS UPDATED BRANCH`. STATUS is `running (pid N)` when the spec
  lock is held, otherwise run.json `status` (or `new` if empty). UPDATED is the timestamp
  of the last `history` entry, or `started` if there are none. After the table, print
  `ready, not started: %s` and `drafts: %s` (comma-joined slugs) when non-empty. With no
  runs and no specs, print `no runs yet — nightshift new <slug> to start a spec`.
- **`status <slug>`**: print `run.json` fields as `key: value` lines in struct order. Then
  print `state dir`, `clone`, `runner log`, `agent log`, `latest result` (the newest
  `results/<slug>-*.md` by mtime, or `none`), and the review command
  `git -C <repo> log --oneline <short base>..<reviewRef>`. An unknown slug gives
  `no run state for %s` and exit 1.
- **`version`**: prints `nightshift <version>`. `version` is a `main` package var set
  through `-ldflags "-X main.version=..."` (Makefile uses `git describe --always --dirty`),
  default `dev`.

### D5. Logging (go-libs `logx`)

- `runner.log` is JSON lines written by `logx.New(f, slog.LevelInfo, "nightshift", nil)`.
  Each reference `logf` call becomes `log.Info` (or `log.Warn` for `SECURITY: …` lines,
  `log.Error` for `error: …` and `hand-back error: …`). The message text is the
  reference's formatted string, verbatim after the D1 renames. Each record also gets
  `slug` as an attr, and `iter` where the message has an iteration number. logx stamps
  real time, and that is accepted: the reference's `15:04:05` prefix and
  `TestLogTimestampsAdvance` are dropped.
- Raw agent output (the `sbx exec … claude` stream) no longer goes into `runner.log`,
  because it would break the JSON lines. It goes to `<state dir>/agent.log`
  (O_APPEND, 0o644). Before each iteration's stream, the runner writes a header line:
  `=== iter %d (session %d) model=%s planning=%v %s ===\n`, with clock `RFC3339`.
- User-facing stdout/stderr lines (summary, busy, dry-run plan, prompts) stay plain
  `fmt` output, exactly as in the reference, with the D1 renames.

### D6. Notifications (`internal/notify`)

```go
type Notifier interface{ Notify(title, msg string, urgent bool) }
```
- Default `desktop`: `exec.LookPath("notify-send")`, and silently do nothing if it is
  missing. argv: `notify-send --app-name=nightshift --urgency=<critical|normal> <title> <msg>`.
  `urgent` is true for `error`, `red` and `stalled`. The command is run with
  `context.WithTimeout(5s)` and its result ignored.
- `NIGHTSHIFT_NOTIFY=off` (env or config) selects a no-op notifier. Any other non-empty
  value except `on` is a config error: `NIGHTSHIFT_NOTIFY=%q: want on or off`.
- `Options.Notifier` is injectable; nil means resolve from settings. Title and message come
  from the reference `notifyText` (with `(sync failed)` / `(sync pending)` suffixes).
- No osascript and no macOS code path.

### D7. Result notes

- Written to `<Home>/results/` with the reference's naming, `uniqueResultPath` and body
  (§3.9.2), except the frontmatter `type:` (D1). The Obsidian-style callouts
  (`> [!error]`, `> [!warning]`) stay; they read fine as plain blockquotes.
- Commit: only when `gitx.IsRepo(Home)` is true (the owner may choose to git-track
  `~/.nightshift`). Then `gitx.Commit(Home, "nightshift: result <slug> (<status>)", rel)`.
  Otherwise there is no commit and nothing is logged. A commit failure is logged
  (`commit result note failed: %v`) and never changes the status.
- No daily-note / "Overnight specs" integration. The companion notes app may read `<Home>/results/` later.

### D8. Interrupts (new)

- `run` installs `signal.NotifyContext` for SIGINT and SIGTERM. On the first signal, the
  current agent or verify context is cancelled. The runner then does exactly what the
  window-closed path does: `StopAgent`, `captureDirty`, `saveState`, and `finish` with new
  status `interrupted` (exit **8**, resumable, summary line
  `The run was interrupted. Uncommitted work was saved as a patch; re-run to continue.`).
  Deferred sandbox `Remove` still runs.
- A second signal exits immediately with code 130, without cleanup. Locks are reclaimed
  as stale on the next run.
- `Options.Ctx context.Context` carries the cancellation (nil = `context.Background()`).
  `main` passes the `NotifyContext`, and tests cancel it directly.
- Cancellation is checked before setup and before each iteration. Before setup →
  `interrupted` with no state dir, sandbox or result note. Setup itself is never
  aborted midway. If cancellation arrives during setup, the loop's first check sees it,
  and the run finishes `interrupted` with its result note.
- For `--next`, `interrupted` and `blocked` are both resumable. `skipInNext` is
  unchanged (it only skips `blocked`/`complete` with an unchanged hash).

### D9. Scheduling: systemd user units (ship disabled)

`deploy/` contains three files. None is installed or enabled by the binary or the Makefile.

`deploy/nightshift-nightly.sh` (install to `~/.local/bin/`, `chmod +x`):
```sh
#!/bin/sh
# nightshift nightly runner — invoked by systemd (nightshift.timer) after hours.
# Runs the OLDEST eligible spec in ~/.nightshift/specs/ready/ through the sandboxed
# Ralph loop, stopping at 06:00 if unfinished (the run resumes the next night).
# Install: cp to ~/.local/bin/nightshift-nightly.sh && chmod +x
# DO NOT enable the timer until you have hand-run several specs and reviewed their diffs.
set -u
export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/bin:/usr/bin:/bin"
home="${NIGHTSHIFT_HOME:-$HOME/.nightshift}"
mkdir -p "$home/logs"
log="$home/logs/nightly.log"
echo "$(date '+%F %T') start" >>"$log"
nightshift run --next --until 06:00 >>"$log" 2>&1
rc=$?
case $rc in
  7) echo "$(date '+%F %T') nothing started (busy)" >>"$log" ;;
  *) echo "$(date '+%F %T') exit $rc" >>"$log" ;;
esac
# Only a runner error (1) marks the unit failed; blocked/red/stalled/capped are outcomes.
[ "$rc" -eq 1 ] && exit 1
exit 0
```

`deploy/nightshift.service`:
```ini
[Unit]
Description=nightshift: run one ready spec overnight
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
ExecStart=%h/.local/bin/nightshift-nightly.sh
TimeoutStartSec=9h
Nice=10
```

`deploy/nightshift.timer`:
```ini
[Unit]
Description=nightshift nightly run at 22:00

[Timer]
OnCalendar=*-*-* 22:00:00
Persistent=false
Unit=nightshift.service

[Install]
WantedBy=timers.target
```

The docs (task 14) must say: copy the units to `~/.config/systemd/user/`, run
`systemctl --user daemon-reload`, and enable with
`systemctl --user enable --now nightshift.timer` only after several hand runs. They must
also say that `loginctl enable-linger $USER` is needed for the timer to fire while logged
out, and that desktop notifications from a user service need the session's
`DBUS_SESSION_BUS_ADDRESS` imported into the systemd user manager (uwsm-based sessions do
this already).

### D10. Spec template

Embedded at `internal/home/spec-template.md` (`go:embed`). `init` writes it to
`<Home>/spec-template.md`. The content is the reference template (§3.13.1) with these
changes: the comment block says `nightshift new <slug>` creates a draft from this file,
`nightshift ready <slug>` / `nightshift done <slug>` move it, precedence reads
`CLI flag > this frontmatter > NIGHTSHIFT_* env > ~/.nightshift/config > default`, and
re-running is `nightshift run <slug>`. Remove the companion notes app's paragraph (daily
agenda / todo listing) and the `[[wikilinks]]` hint.

### D11. Fixes to the reference's known quirks

The reference preserved these on purpose. nightshift fixes them:

1. A planning iteration that commits resets to `last_green`, not `base`. On a fresh run
   these are equal, so behavior only changes on a resume.
2. On resume, if `run.json` `repo` differs from the spec's resolved `repo:`, refuse with
   `repo changed from %s to %s since this run started; re-run with --fresh`.
3. Frontmatter values have ONE pair of matching surrounding `"` or `'` stripped. Each
   comma-separated `egress` item is unquoted the same way.
4. An unparsable `--until` is a usage error: `--until %q: want HH:MM (24h, local time)`,
   exit 1, nothing created. The reference silently ignored it.
5. `--fresh` with no state dir still deletes `refs/heads/spec/<slug>` and
   `refs/nightshift/<slug>` if they exist (under the repo lock, after the TTY/`--yes`
   confirmation). `--fresh` with state also deletes the pending ref.
6. `.md` matching is case-insensitive everywhere (`--next` scan, slug derivation, name
   resolution). The slug is the file name minus its extension, whatever its case, and the
   file is opened by its real name.
7. `sbxSandbox.Git` returns stdout with only trailing whitespace trimmed
   (`strings.TrimRight(s, " \t\r\n")`). Callers that parse SHAs/counts TrimSpace
   themselves. The result note's `diff --stat` keeps its leading space.
8. If creating the `-ctl` sandbox fails after the agent sandbox was created, the agent
   sandbox is removed (`rm -f <agent>`) before returning the error.
9. `appendProgress` lines and `history` entries use the wall clock (`clockFn()`) at the
   moment they are written, not the run start time. The result-note file name still uses
   the run start date.

### D12. Dropped

- The import-guard test and the read-only relationship to the companion notes app's binary. nightshift is
  a single binary.
- `boottime_darwin.go`, osascript, launchd plist, the old vault env var/vault paths, vault git
  commits (replaced by D7's optional home commit), daily agenda integration.
- `TestLogTimestampsAdvance` (D5).

## Package layout

```
cmd/nightshift/main.go        dispatch, `run` flag parsing, usage, exit mapping, version
internal/home/                Home resolution, D2 layout, config file (D3), init/new/ready/done,
                              embedded spec-template.md
internal/runner/              same file split as the original (run.go, spec.go,
                              base.go, locks.go, next.go, loop.go, state.go, sandbox.go,
                              scaffold.go, prompt.go, result.go) + status.go + signal.go
internal/lock/                reference internal/lock (Linux + other)
internal/gitx/                Run, IsRepo, GitDir, RunRetry, Commit
internal/notify/              Notifier, desktop (notify-send), noop
deploy/                       nightshift-nightly.sh, nightshift.service, nightshift.timer
docs/nightshift.md            user docs
README.md, CLAUDE.md, Makefile, .gitignore, vendor/
```

`Makefile` targets: `build` (`go build -ldflags … -o bin/nightshift ./cmd/nightshift`),
`install` (`go install -ldflags … ./cmd/nightshift`), `test`, `vet`, `fmt`, `vendor`
(`go mod tidy && go mod vendor`). `.gitignore`: `bin/`, `.claude/`, `*.test`.

## Tasks

One committable step per iteration, in this order. Each step ends with its tests from
the checklist written and green.

- [ ] 1. Module skeleton: `go.mod` (D1, Constraints), `go mod vendor`, `Makefile`,
      `.gitignore`, `cmd/nightshift/main.go` with `version` and the D4 top-level usage;
      every other command prints `not implemented` and exits 1.
- [ ] 2. `internal/lock` per reference §3.10 (Linux + other) and the full lock test
      matrix, with `NIGHTSHIFT_LOCK_*` child env.
- [ ] 3. `internal/gitx` per reference §3.11 + tests (incl. `RunRetry` on a held ref lock,
      `Commit` no-op guard and pathspec scoping).
- [ ] 4. `internal/home`: Home resolution, layout, config file parser (D2, D3) + tests.
- [ ] 5. `internal/home`: `Init`, `New`, `Ready`, `Done` and the embedded template (D4,
      D10) + tests.
- [ ] 6. `internal/runner`: `spec.go` and `base.go`: resolution, readiness gate, frontmatter
      (with D11.3, D11.6), the 5-tier `pick`, base resolution + tests.
- [ ] 7. `internal/runner`: `state.go`, `scaffold.go` (run.json, setup, clone, resume
      checks incl. D11.2, `doFresh` incl. D11.5) + tests.
- [ ] 8. `internal/runner`: `sandbox.go` (sbx impl with D1 names, D11.7, D11.8) and
      `prompt.go` (preambles byte-identical, dry-run plan with the `home:` line) + tests.
- [ ] 9. `internal/runner`: `loop.go` per reference §3.5.8 with D11.1 and D11.9, the
      stub-Sandbox harness, the logx runner.log and agent.log (D5) + loop scenario tests.
- [ ] 10. `internal/notify` + `internal/runner/result.go` (D6, D7) + tests.
- [ ] 11. `internal/runner`: `locks.go`, `next.go`, `run.go` (Run / runNext /
      runLocked, concurrency, busy) + remaining runner and reliability tests.
- [ ] 12. `internal/runner/signal.go`: interrupt handling (D8) + tests.
- [ ] 13. `internal/runner/status.go` + `cmd/nightshift` full dispatch: `run` flags and
      usage, `status`, `init`/`new`/`ready`/`done` wiring, `--until` validation (D11.4) +
      CLI tests.
- [ ] 14. `deploy/` files (D9), `docs/nightshift.md` (reference user docs rewritten for D1–D12,
      including a "first run" checklist and the systemd notes), `README.md`, `CLAUDE.md`
      (architecture summary, trust model, test commands, "never call the real sbx in tests").
- [ ] 15. Parity pass: walk the Test checklist and every delta; add any missing test, fix
      any gap. Touch `.spec-complete` only after this.

## Test checklist

Carry over every test from reference §3.14.1–3.14.5 (harness, runner, reliability, base,
lock), with the D1 renames and these adjustments:
- `newVault`/`addSpec` become `newHome`/`addSpec`, writing to `<home>/specs/ready/`.
- Error-text assertions use the D1/D2 wording (`specs/ready/`, `specs/done/`,
  `refs/nightshift/demo`).
- `TestResolveRefusesDraft` asserts the D2 draft message names `nightshift ready`.
- `TestPlannerModelFirstIteration` is unchanged (fresh run: base == last_green).
- Drop `TestImportGuard` and `TestLogTimestampsAdvance`.
- `TestScaffoldSymlinkRejected` checks runner.log JSON records with `level` `WARN` and
  `msg` containing `not a regular file`.

New tests:

| Test | Asserts |
|---|---|
| `TestHomeFromEnv` | `NIGHTSHIFT_HOME` wins; unset gives `~/.nightshift` under `t.Setenv("HOME", tmp)`. |
| `TestInitCreatesLayout` | Every D2 dir exists; template and config are written; a second `Init` creates nothing and keeps a hand-edited config byte-identical. |
| `TestInitWarnsWithoutSbx` | Missing sbx bin gives the stderr warning and a nil error. |
| `TestConfigFileParse` | Comments, blanks, quotes, duplicate keys; unknown key and missing `=` give the exact errors with line numbers. |
| `TestConfigTierPrecedence` | For model/max_iter/iter_timeout/base: flag > frontmatter > env > config > default, with src labels `flag/frontmatter/env/config/default`; `max_concurrent` ignores frontmatter and honors config. |
| `TestNewFromTemplate` | Draft written with `created:` set to now (RFC3339) and `--repo`/`--verify` substituted; uses the home template if present, the embedded one if not. |
| `TestNewRefusesDuplicateAndBadSlug` | An existing slug in draft/ready/done and an invalid slug both give their exact errors and write nothing. |
| `TestReadyValidatesFrontmatter` | Missing `repo:` or `verify:` refuses with the D4 message; a valid draft moves to ready; an existing ready target refuses. |
| `TestDoneRefusesRunning` | A held spec lock refuses with pid; otherwise the spec moves to done and run state is untouched. |
| `TestRunRefusesOutsideHome` | A spec in some other `ready/` dir outside `<Home>/specs/ready` gives `spec must live under`. |
| `TestRunnerLogIsJSON` | Every runner.log line parses as JSON with `service=nightshift`, `msg`, `slug`; the `iter %d verify GREEN at %s` record has `iter`. |
| `TestAgentLogSeparate` | Agent output appears in agent.log after an `=== iter 1 (session 1)` header and never in runner.log. |
| `TestNotifyArgvAndUrgency` | A fake `notify-send` on PATH records argv: `--app-name=nightshift`, `--urgency=critical` for red, `normal` for complete; title has `(sync pending)` when applicable. |
| `TestNotifyOffAndMissing` | `NIGHTSHIFT_NOTIFY=off` never execs; a missing binary is silent; `NIGHTSHIFT_NOTIFY=maybe` is a config error. |
| `TestResultCommittedOnlyWhenHomeIsRepo` | Plain home: note written, no git. Home as a git repo: exactly one commit `nightshift: result demo (complete)` touching only the note; unrelated dirty files stay uncommitted. |
| `TestInterruptIsResumable` | A cancelled run ctx during iter 2 gives `interrupted` / exit 8, agent stopped, dirty patch saved, sandboxes removed, result note with the interrupted summary; a re-run resumes the same clone. |
| `TestInterruptBeforeLoop` | Cancellation before setup gives `interrupted`, no state dir, no note. |
| `TestUntilInvalidIsUsageError` | `--until 25:99` and `--until soon` give exit 1 with the D11.4 message and create nothing. |
| `TestPlannerCommitOnResumeResetsToLastGreen` | Resume after a spec edit whose planner commits keeps earlier green commits (reset target = last_green) and the run completes. |
| `TestResumeRepoChangedRefuses` | Changing `repo:` between runs gives the D11.2 error with state unchanged. |
| `TestFrontmatterQuotesStripped` | `model: "opus"` gives `opus`; `egress: 'a.com', "b.com"` gives `[a.com b.com]`; mismatched quotes are kept. |
| `TestFreshWithoutStateDeletesRefs` | No state dir + existing `spec/demo` and `refs/nightshift/demo` → both deleted by `--fresh --yes`. |
| `TestUppercaseMDSpec` | `DEMO.MD` in ready is found by name and by `--next`, the slug is `DEMO`, the lock label is `DEMO`, and the run completes. |
| `TestDiffstatKeepsLeadingSpace` | The result note `## Changes` block's first line starts with a space. |
| `TestCtlCreateFailureRemovesAgent` | A fake sbx failing on `create --name nightshift-demo-ctl` makes `Create` error, and the call log ends with `rm -f nightshift-demo`. |
| `TestProgressStampsUseWallClock` | With an advancing clock, two runner PROGRESS.md lines in one run carry different minutes. |
| `TestStatusTable` | Runs complete/blocked plus one ready-not-started spec and one draft give the exact table and trailer lines; a held spec lock shows `running (pid N)`. |
| `TestStatusDetail` | `status demo` prints run.json keys in order, the paths, the newest result note and the review command; an unknown slug exits 1. |
| `TestCLIUsageAndExitCodes` | No args → usage on stderr, exit 2; `help` → stdout, exit 0; `run` with two positionals → exit 1; `run --bogus` → exit 2; `version` prints `nightshift dev`. |

## Acceptance criteria

- [ ] `go.mod` has exactly one direct `require` (`github.com/jwbonnell/go-libs`);
      `go list -deps ./cmd/nightshift` shows only std-lib, `github.com/jwbonnell/nightshift/...`
      and `github.com/jwbonnell/go-libs/pkg/logx`. `vendor/` is committed and
      `go build -mod=vendor ./...` works with `GOPROXY=off`.
- [ ] Every test in the Test checklist exists, asserts what it says, and passes.
- [ ] The prompt preambles are byte-identical to reference §3.8.1 / §3.8.2.
- [ ] A temp-home smoke (`NIGHTSHIFT_HOME=<tmp>`) against a source repo with one commit:
      `nightshift init` → `new demo --repo <src> --verify true` → `ready demo` →
      `run demo --dry-run` prints the plan (with `home:`) and creates nothing →
      `status` lists `demo` under `ready, not started`.
- [ ] `nightshift -h`, `nightshift run -h` match D4.
- [ ] `deploy/` holds the three D9 files exactly as written above; `docs/nightshift.md`
      covers install, first run, config, every subcommand, exit codes, sandbox model,
      concurrency, scheduling and recovery commands.
- [ ] `test -z "$(gofmt -l cmd internal)" && go vet ./... && go test ./...` green.

## Out of scope

- Any web/API/dashboard, database or metrics (go-libs `web`, `dbx`, `metrics`). Keep
  `internal/runner` free of `fmt.Print*` to stdout outside the reference's user-facing
  output points, so a server can wrap it later.
- The companion notes app and its MR reviewer, GitLab, Obsidian, the daily agenda.
- macOS support.
- Auto-moving specs between draft/ready/done (only the explicit subcommands move them).
- A sandbox backend other than `sbx`.
- Running the real `sbx`/`claude`/`notify-send`, enabling any systemd unit, pushing
  anywhere, or editing this spec.
