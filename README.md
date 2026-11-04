# nightshift

A standalone, sandboxed spec runner for Linux. Hands one ready design spec
to Claude Code and drives it through a Ralph loop inside Docker Sandboxes
(`sbx`): the runner enforces the spec's verify command, resets a red tree to
the last green commit, caps iterations, detects stalls, and reports the
result back — all outside the agent's control.

```sh
go install ./cmd/nightshift
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

Linux only. `sbx` must be installed and configured (`sbx secret ls` should
show an `anthropic` service secret) on the host; `claude` runs only inside
the sandbox template, never on the host.

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
