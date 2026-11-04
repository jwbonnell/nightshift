# nightshift

A standalone, sandboxed spec runner for Linux. Hands one ready design spec
to Claude Code and drives it through a Ralph loop inside Docker Sandboxes
(`sbx`): the runner enforces the spec's verify command, resets a red tree to
the last green commit, caps iterations, detects stalls, and reports the
result back — all outside the agent's control.

```sh
go install github.com/jwbonnell/nightshift/cmd/nightshift@latest   # or: make install, from a clone
nightshift init
nightshift new my-spec --repo ~/path/to/checkout --verify 'go test ./...'
# edit ~/.nightshift/specs/draft/my-spec.md
nightshift ready my-spec
nightshift run my-spec --dry-run   # see the plan, nothing runs
nightshift run my-spec
```

See [`docs/nightshift.md`](docs/nightshift.md) for the full subcommand
reference, configuration, the sandbox trust model, concurrency, scheduling,
and recovery. See [`SPEC.md`](SPEC.md) for the spec this build was driven
from.

`nightshift pr` pre-reviews GitLab merge requests and GitHub pull requests
in a read-only sandbox and writes a review note — see
[`docs/pr.md`](docs/pr.md).

## Requirements

- **Linux.** It's developed on Arch. The optional nightly scheduling uses a systemd user
  timer (`deploy/`, shipped disabled).
- **Go 1.25 or newer** to build, and **git**.
- **Docker Sandboxes (`sbx`)**, installed and set up on the host. Every agent and every
  verify command runs inside an `sbx` sandbox. nightshift refuses to run without it.
- **Your own model access.** Claude Code runs only inside the sandbox template, using a
  Claude subscription or Anthropic API key you give `sbx` (`sbx secret ls` should show an
  `anthropic` service secret). opencode agents use whatever provider you configure for
  opencode. You're responsible for using your model access within your provider's terms.
- Optional: `notify-send` for desktop notifications, and bash-completion for
  `nightshift completion bash`.

## Safety

nightshift runs a coding agent unattended, with permission to edit files and run
commands. It's built so that the sandbox, not the agent's restraint, limits what it can do:

- **The agent runs only inside `sbx`, never on the host.** Inside its sandbox it runs with
  `--permission-mode bypassPermissions`, which is safe only because of the sandbox. Don't
  run the agent command outside one.
- **Three sandboxes on one clone.** The agent's sandbox edits code. A separate sandbox does
  every git operation. A third is recreated from a clean image for every verify, so nothing
  persists between iterations.
- **No credentials and no way to push.** The clone the agent works in has no remotes, and
  the sandbox has no host `$HOME`, SSH agent or git credentials. Work reaches your repo only
  through nightshift copying a verified branch back, under a lock, as `spec/<slug>`. It
  never pushes anywhere.
- **Network is allow-listed.** Sandboxes can reach the model API and the usual package
  registries, plus any hosts a spec adds with `egress:`. Keep that list short.
- **Agent-written code does run**, in sandboxes: every verify command, `accept:` command and
  holdout test executes code the agent wrote. That's the point of verifying. It's also why
  none of it runs on the host.
- **Two things do run on the host:** `nightshift new`/`refine` start an interactive Claude
  session that can edit only the draft spec (no shell), and `nightshift pr` makes read-only
  `GET` requests to your forge with the token you configure.
- **Review before you merge.** A green verify and a passing automatic review are evidence,
  not proof. Read `spec/<slug>` before merging it.

The full trust model is in [`CLAUDE.md`](CLAUDE.md) and the Sandbox model section of
[`docs/nightshift.md`](docs/nightshift.md). nightshift is MIT-licensed and comes with no
warranty.

## Skills and backlog

[`skills/nightshift-spec/`](skills/nightshift-spec/) is an agent skill for drafting specs
and reviewing runs; [`skills/README.md`](skills/README.md) says how to install it for
Claude Code, Claude Desktop or opencode. [`docs/backlog.md`](docs/backlog.md) lists
known leftovers, deferred ideas and lessons from running nightshift.

## Build

```sh
make build    # bin/nightshift, version via git describe
make test
make vet
make fmt
```

## Status

Built task by task against [`SPEC.md`](SPEC.md); see `PROGRESS.md` (local,
gitignored) for build history and `SPEC.md`'s own Test checklist and
acceptance criteria for what "done" means.
