---
type: spec
created: 2026-01-01T09:00:00-05:00
repo: ~/path/to/your/checkout
verify: go vet ./... && go test ./...
---

# <short imperative title>

<!--
`nightshift new <slug>` creates a draft from this file. `nightshift ready
<slug>` / `nightshift done <slug>` move it between specs/draft/ -> ready/ ->
done/.

REQUIRED frontmatter (read by nightshift):
  repo:   the git checkout the agent clones from (a spec/<slug> branch is created)
  verify: the command the RUNNER runs after every iteration; a commit is kept only
          when it is green. Make it strict — the verify gate is what makes an
          unattended run converge instead of wander.

OPTIONAL frontmatter:
  model:         iterator model for most iterations   (default: sonnet)
  planner_model: model for the first planning pass     (default: opus)
  max_iter:      per-run iteration cap                 (default: 10)
  iter_timeout:  Go duration per iteration, e.g. 30m   (default: 30m)
  base:          branch/ref/sha the spec/<slug> branch starts from. Prefers
                 origin/<base> over a same-named local branch; a full ref or a
                 commit SHA is accepted as given. (default: the source repo's
                 origin/HEAD branch, then main, then master)
  egress:        comma-separated extra hosts the sandbox may reach, on top of the
                 default allowlist (Anthropic API + Go/npm/pypi/etc. registries),
                 e.g. "api.example.com,cdn.example.com"

Precedence for the optional keys: CLI flag > this frontmatter > NIGHTSHIFT_* env
> ~/.nightshift/config > default.

The agent runs sandboxed with no host $HOME, no SSH agent, no git credentials, and
a clone with no remotes, so pushing is impossible. If it hits a decision only you
can make, it writes QUESTION.md and the run stops `blocked`; answer by editing this
spec, then re-run (`nightshift run <slug>` resumes by default).
-->

## Goal

One paragraph: what is true when this is done. If you cannot write this without
an "and", the spec is probably two specs.

## Context

Why this is worth doing, and what the agent needs to know that is not obvious
from reading the code.

## Constraints

- <e.g. std-lib only — this module has zero external dependencies>
- <e.g. never rewrite captured note content ("raw is sacred")>

## Tasks

Smallest independently-committable steps, in order. The agent does exactly one
per iteration, so a step that cannot be committed on its own will stall the loop.

- [ ] 
- [ ] 

## Acceptance criteria

Observable and checkable — the agent stops (touches .spec-complete) when all of
these are true and `verify` is green.

- [ ] 
- [ ] `go test ./...` green

## Out of scope

What the agent must NOT touch. Be specific; this is the main defence against
scope drift over many iterations.

- 
