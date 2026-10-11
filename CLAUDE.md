# nightshift

Guidance for Claude Code (or any agent) working on this repo.

## What this is

A Go CLI, `nightshift`, that hands one ready design spec to Claude Code and
drives it through a Ralph loop inside Docker Sandboxes (`sbx`): fresh
context every iteration, memory only via `PROGRESS.md` and git history. The
*runner* enforces everything — the verify command, red-reset-to-last-green,
iteration caps, timeouts, stall detection, the `--until` window — never the
agent. See [`SPEC.md`](SPEC.md) for the authoritative spec this was built
from and [`docs/nightshift.md`](docs/nightshift.md) for user-facing docs.
`docs/reference/` is the behavioral baseline this supersedes — never edit
it, and never edit `SPEC.md`.

## Architecture

```
cmd/nightshift/        CLI dispatch, flag parsing, exit-code mapping
internal/home/         Home resolution, D2 layout, config file, init/new/ready/done
internal/runner/       Everything else: spec resolution, state, the sandbox
                        interface + real sbx implementation, the loop itself,
                        locking/concurrency, notifications, result notes, signals
internal/gitx/         Host git operations (Run/IsRepo/GitDir/RunRetry/Commit)
internal/lock/         Cross-process file locks (std-lib only, no deps on
                        anything else in this module)
internal/notify/       Desktop notifications (notify-send) + a no-op
internal/interview/    `new`/`refine`'s host claude session: settings.json
                        (Edit allowed only on the draft) and prompt.md
                        generation, argv building. The one place nightshift
                        launches a *host* claude — no shell in its tool set,
                        no bypass-permissions flag, ever.
deploy/                systemd units, shipped disabled — never install/enable these
vendor/                github.com/jwbonnell/go-libs/pkg/logx only
```

`internal/lock` has zero internal dependencies so `internal/gitx` can import
it without a cycle. Everything else may import `home`/`gitx`/`lock`/`notify`
freely; `runner` is the only package that imports all four.

`internal/runner`'s own files roughly mirror the reference's
`internal/specrun` split (`spec.go`/`base.go` resolution, `state.go` setup
and clone management, `sandbox.go`/`prompt.go` the sbx backend and prompts,
`loop.go` the Ralph loop, `result.go` the result note, `locks.go`/`next.go`/
`specrun.go` the top-level `Run`/concurrency, `signal.go` D8 interrupts,
`status.go` the `status` command, `lint.go` the `lint` checks and the
`BaseVerifier` interface behind check 8's sandboxed verify-on-base) — see
`PROGRESS.md` (local, gitignored) for exactly how each piece arrived,
including a few deliberate forward-declarations that got relocated once
their target file existed.

`runner.log` (built in `state.go` via `logx.NewText`) is plain-text lines,
`<date> <time> <LEVEL> <message>`, with no attrs and no source location —
see `docs/nightshift.md` for the level mapping and a sample line.

One Ctrl+C ends a run `interrupted` and resumable (state saved, agent
stopped); a second force-quits with no cleanup, and any leftover sandboxes
it left behind are removed automatically on the next run of that spec —
see `docs/nightshift.md`'s Interrupts section. A usage limit that resets
inside the run's `--until` window waits and resumes automatically instead
of stopping `rate-limited` — see the `run` subcommand section there.

## Trust model — read this before touching sandbox.go or loop.go

- **The agent never runs on the host.** It runs only inside the `agent`
  sbx sandbox, via `claude -p` with the prompt on stdin.
- **Three sandboxes, three trust levels**, all mounting the same clone:
  `agent` (untrusted, runs `claude`), `-ctl` (every git op on the clone —
  the clone's own `.git/config`/hooks are agent-writable and must never
  execute on the host or in the agent sandbox), `-verify` (the spec's
  `verify:` command — recreated from a clean image before *every* verify,
  so nothing from a prior iteration or from verify itself persists).
- **The agent is always stopped before verify or any git op**, in the same
  iteration, even a timed-out one — see `TestAgentKilledBeforeVerify`.
- **Scaffold files** (`PROGRESS.md`, `QUESTION.md`, `.spec-complete`) are
  read/written on the host but only via `Lstat` + `O_NOFOLLOW` — an agent
  that symlinks one of these to a host path gets the link itself ignored
  or removed, never the target followed. See `TestScaffoldSymlinkRejected`.
- **The clone has no remotes.** The only way work reaches the source repo
  is the runner's own `syncBranchBack`, under a repo lock, from the host,
  fetching *out of* the clone — never the reverse.
- Never add a fourth trust boundary or let the agent sandbox touch git
  directly; that's the one invariant most of the test suite is built to
  protect. (`review.go`'s `-review` sandbox doesn't count: it never mounts
  the agent's clone at all — only a fresh `--no-local` workspace built from
  the *source* repo, read-only `Read,Grep,Glob` tools, no permission-bypass
  flag — and its verdict can never change a run's status, exit code, or
  history. See `docs/nightshift.md`'s Review section.)

## Running tests

```sh
test -z "$(gofmt -l cmd internal)" && go vet ./... && go test ./...
```

That's the exact verify command from `SPEC.md`'s frontmatter — run it,
green, before any commit.

Also run `make test-hermetic` before any commit that touches a test
spawning git: it re-runs the suite with a throwaway `$HOME` and
`GIT_CONFIG_GLOBAL`/`GIT_CONFIG_NOSYSTEM` pointed at nothing, catching
tests that only pass because the host's `~/.gitconfig` happens to supply
a commit identity — exactly the gap a real sbx verify sandbox's clean
`$HOME` would expose. Every test package that shells out to git sets this
up itself via `internal/testenv.HermeticGit()` in a `TestMain`.

**Tests must never call the real `sbx`, `claude`, `notify-send`, or
systemd, and must never touch your real `$HOME`.** Use:

- `t.TempDir()` for every home/state/repo directory a test needs, and
  `t.Setenv("NIGHTSHIFT_HOME", ...)` (or pass `Home`/`StateRoot` directly
  in `runner.Options`) rather than relying on the real `~/.nightshift`.
- The `stubSandbox` in `internal/runner/harness_test.go` for anything that
  exercises the Ralph loop — it implements `Sandbox` entirely in-process,
  with `agent`/`verify` hook functions tests use to script "what the agent
  does this iteration." Its `Git` method shells out to a *real* clone on
  disk (not a fake), so `captureDirty`/`resetHard`/`resetTo`/
  `syncBranchBack` all exercise real git behavior — only the
  `claude`/`sbx` boundary itself is faked.
- A fake binary on a temp `PATH` (or an injected absolute path, e.g.
  `sbxSandbox{bin: ...}` or `notify.Desktop{Bin: ...}`) for anything that
  tests the *real* `sbxSandbox`/`Desktop` implementations' argv — see
  `internal/runner/sandbox_test.go` and `internal/notify/notify_test.go`
  for the pattern (a tiny `sh` script logging `$*` to a file, controlled
  entirely via env vars).
- If you need a slow/hanging subprocess in a fake script, `exec` the thing
  that should actually block (e.g. `exec sleep "$N"`), don't just call it
  as a plain subcommand — otherwise a context-cancel `SIGKILL` on the
  wrapper shell leaves the real blocking process orphaned, holding the
  output pipe open, and your test "passes" after the full sleep duration
  instead of promptly. This bit a test in Task 8; see `PROGRESS.md`.
- For anything that needs real concurrent processes racing a lock (only
  `internal/lock`'s `TestReclaimRace` needs this), re-exec
  `os.Args[0]` with `-test.run=...` and an env-var protocol — and gate
  every child's *exit*, not just its start, behind a shared barrier, or a
  child that finishes early can get reaped while siblings still hold
  (now-stale) PID references to it.

## Things that look like bugs but aren't

SPEC.md's D11 lists nine deliberate fixes to the reference's own known
quirks (planner-commit reset target, resume repo-change detection, frontmatter
quote-stripping, `--until` validation, `--fresh`-with-no-state-dir ref
cleanup, case-insensitive `.md` matching, git-output trimming, `-ctl`
create-failure cleanup, wall-clock progress stamps). If you're porting
anything from `docs/reference/`, check D11 first — several of its quirks
are *intentionally* reproduced in the reference's own pseudocode and must
NOT be copied as-is.
