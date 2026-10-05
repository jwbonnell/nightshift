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
from, and [`docs/reference/`](docs/reference/) for the detailed design this
one supersedes.

Linux only. `sbx` must be installed and configured (`sbx secret ls` should
show an `anthropic` service secret) on the host; `claude` runs only inside
the sandbox template, never on the host.

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
