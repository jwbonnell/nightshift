# PR/MR pre-review (`nightshift pr`)

`nightshift pr` pre-reviews one pull request or merge request — or every open
one where you're a requested reviewer — on a self-hosted **GitLab** and on
**GitHub** (github.com or GitHub Enterprise). It writes one note per change to
`~/.nightshift/reviews/`. It never posts anything to either forge: every API
call it makes is a `GET`, and there is no `--post` flag.

It's a subcommand of the same `nightshift` binary the Ralph-loop runner uses,
sharing its home directory, lock package, git helpers, notifier, and agent
selectors — but it has no agent sandbox, no `-ctl` sandbox, and never touches
`specs/`, `runs/`, or `results/`. See [`CLAUDE.md`](../CLAUDE.md)'s trust
model for how its one sandbox differs from a run's three.

Platform: Linux only. [Docker Sandboxes](https://sbx) (`sbx`) and `git` must
be on the host; the reviewer (`claude` or `opencode`) exists only inside its
sandbox template.

## Setup

1. **A token for each forge you use.**
   - **GitLab**: a personal access token with **`read_api` + `read_repository`**
     (both read-only) — `read_repository` is needed because `nightshift pr`
     clones over HTTPS, not just calls the API.
   - **GitHub**: a fine-grained personal access token with read-only
     **Contents**, **Pull requests**, and **Metadata**, scoped to the repos
     you review; or a classic token with the `repo` scope if any of those
     repos are private.

   Put each token in its own file, `chmod 600` it, and point
   `NIGHTSHIFT_GITLAB_TOKEN_FILE` / `NIGHTSHIFT_GITHUB_TOKEN_FILE` at it (a
   leading `~/` is expanded). **Tokens never go inline in the config file** —
   a `NIGHTSHIFT_GITLAB_TOKEN=` or `NIGHTSHIFT_GITHUB_TOKEN=` *line in the
   config file* is a hard error naming the line number; the inline env var
   itself still works (and wins over `_FILE`) for one-off use. A token file
   readable by group or other prints a one-time warning
   (`warning: %s is readable by other users — chmod 600 it`) but isn't
   refused.

   `NIGHTSHIFT_GITLAB_URL` has no default — set it to your self-hosted
   instance's base URL (e.g. `https://gitlab.example.com`).
   `NIGHTSHIFT_GITHUB_URL` defaults to `https://github.com`; point it at your
   GitHub Enterprise base URL instead if that's what you use (the API base is
   derived automatically: `https://api.github.com` for github.com itself,
   `<url>/api/v3` otherwise).

   A forge counts as **configured** once it has a non-empty token (GitLab
   also needs a non-empty URL). You can configure one or both; `--pending`
   reviews across every forge that's configured, and an explicit `<repo> <n>`
   needs `--forge` only when both are.

2. **Confirm the token can clone over an HTTP header**, which is exactly how
   `nightshift pr` authenticates internally (not embedded in the URL), so a
   failure here means a real review will fail too:
   ```sh
   TOKEN="$(cat /path/to/gitlab-token)"
   git -c http.extraHeader="Authorization: Basic $(printf 'oauth2:%s' "$TOKEN" | base64)" \
     clone --bare <http_url_to_repo> /tmp/x
   # GitHub: swap oauth2 for x-access-token
   ```
3. **`sbx` is set up** with the `anthropic` secret (`sbx secret ls` should
   show it), same as the Ralph-loop runner. There's no host fallback: if
   `sbx` isn't on `PATH`, `nightshift pr` refuses to run rather than ever
   invoking a reviewer on the host.
4. `nightshift init` creates `reviews/` and `pr/{repos,work,logs}` (the
   latter four `0700`) alongside the rest of the home layout, and adds every
   `NIGHTSHIFT_PR_*`/`NIGHTSHIFT_GITLAB_*`/`NIGHTSHIFT_GITHUB_*` key to the
   commented config template, right after the review-lens keys. It never
   overwrites an existing config.

## Usage

```
nightshift pr <pr-or-mr-url> [--force] [--list]
nightshift pr <owner/repo#n | group/project!n> [--force] [--list]
nightshift pr <repo> <n> [--forge gitlab|github] [--force] [--list]
nightshift pr --pending [--force] [--include-drafts] [--list] [--limit N]
common flags: --dry-run --model M --max-turns N --timeout D --max-lines N
```

- A **URL** (anything containing `://`) is matched against each configured
  forge's web host. An unrecognized host, or a target forge that isn't
  configured, is a usage error.
- **Shorthand**: `group/project!42` for GitLab, `owner/repo#42` for GitHub —
  split at the *last* `!`/`#`, so a path containing either character still
  parses correctly.
- **`<repo> <n>`**: two bare positionals. `--forge` picks which forge to ask;
  without it, the one configured forge is used, or — with both configured —
  it's a usage error telling you to add `--forge`.
- **`--pending`** reviews every open change where you're a requested
  reviewer, across every configured forge (GitLab first, then GitHub, each
  oldest-created first). `--forge` and `--include-drafts` (without
  `--pending`) are both usage errors, as is giving `--pending` any positional
  argument.
- **`--force`** re-reviews even if this exact head SHA was already reviewed,
  the change is closed/merged, or it's over the size cap. It never bypasses
  a draft skip.
- **`--limit N`** (with `--pending` only) stops after N changes have been
  sent to the reviewer; `0` (the default) means no limit.

Exit codes: `0` on success (including nothing to review), `1` if any review
failed, `2` on a usage/config error (including a missing `sbx` or no forge
configured), `130` on Ctrl-C/SIGTERM — the change being reviewed when the
signal arrives is reported `interrupted`, not `failed`, and isn't retried as
a failure on the next run; no later change in that `--pending` pass is
touched, and the sandbox is still removed before exit.

### `--list` vs `--dry-run`

They can't be combined (exit `2`) — pick one:

- **`--dry-run`** makes *no* API calls at all. It prints the fully resolved
  settings, the target(s) (or, for `--pending`, a note that targets aren't
  listed because no API calls are made), and the exact `sbx` commands a real
  run would issue — all without touching the network, a clone, or writing
  anything.
- **`--list`** makes the same read-only forge `GET`s a real run would
  (resolving the repo and, for `--pending`, the change itself when the
  listing doesn't already carry a head SHA — true for every GitHub listing),
  applies the exact same skip/already-reviewed decision a real run uses, and
  prints one line per change:

  ```
  team/app!12   would review       Fix auth
  team/app!9    skip (already reviewed at abcd1234)
  team/app!7    skip (draft)
  owner/repo#3  queued (over --limit)
  note: changes over the 3,000-line size cap can't be detected without cloning; a real run writes them as SKIPPED
  would review: 1 · queued: 1 · skipped: 2
  ```

  `--list` never clones, never starts a sandbox, never takes the lock, and
  never writes anything. It can't detect the size cap (that needs a clone to
  measure); a real run writes an over-cap change as a `SKIPPED` note instead
  of reviewing it.

### Skips

A change is skipped (not reviewed, no note written or updated) when, in
order: it collides on note slug with an unrelated project (see below, and
this one is a *failure*, not a skip); it's a draft during `--pending` without
`--include-drafts`; it isn't open (closed/merged/locked) without `--force`;
or its head SHA already has a note from the same project without `--force`.
Only `--force` bypasses the state/already-reviewed checks, never the draft
check.

## Settings

Precedence: **flag > env > config file > default.** (`NIGHTSHIFT_PR_PROMPT`,
`NIGHTSHIFT_PR_MIRROR`, `NIGHTSHIFT_PR_EGRESS`, and every forge key have no
flag — env > config > default.)

| Flag | Env var | Config key | Default |
|---|---|---|---|
| `--model` | `NIGHTSHIFT_PR_MODEL` | same | `sonnet` |
| `--max-turns` | `NIGHTSHIFT_PR_MAX_TURNS` | same | `30` |
| `--timeout` | `NIGHTSHIFT_PR_TIMEOUT` | same | `15m` |
| `--max-lines` | `NIGHTSHIFT_PR_MAX_LINES` | same | `3000` |
| — | `NIGHTSHIFT_PR_PROMPT` | same | unset (embedded prompt) |
| — | `NIGHTSHIFT_PR_MIRROR` | same | unset (off) |
| — | `NIGHTSHIFT_PR_EGRESS` | same | unset |
| — | `NIGHTSHIFT_GITLAB_URL` | same | unset |
| — | `NIGHTSHIFT_GITLAB_TOKEN_FILE` (or inline `_TOKEN`) | same | unset |
| — | `NIGHTSHIFT_GITHUB_URL` | same | `https://github.com` |
| — | `NIGHTSHIFT_GITHUB_TOKEN_FILE` (or inline `_TOKEN`) | same | unset |

`--model`/`NIGHTSHIFT_PR_MODEL` take an **agent selector** — see
[Reviewer model](#reviewer-model) below.

`nightshift pr --dry-run` against a real target prints every resolved value
(token presence, never the token itself):

```
nightshift pr --dry-run: resolved config
  home:         /home/you/.nightshift
  reviews:      /home/you/.nightshift/reviews
  mirror:       off
  gitlab:       unset (token unset)
  github:       https://github.com (token set)
  state dir:    /home/you/.nightshift/pr
  model:        claude/sonnet
  egress:       none
  max turns:    30
  timeout:      15m0s
  max lines:    3000
  sbx bin:      sbx
  prompt:       embedded

targets:
  owner/repo#1

commands that would run:
  sbx create --name nightshift-pr-<run-id> claude /home/you/.nightshift/pr/work
  sbx exec -w /home/you/.nightshift/pr/work/owner-repo-pr1/tree nightshift-pr-<run-id> claude -p --output-format json --model sonnet --max-turns 30 --tools Read,Grep,Glob --permission-mode dontAsk --no-session-persistence --setting-sources user --disable-slash-commands
```

## Note format

Each review writes `~/.nightshift/reviews/<slug>.md` (slug: the repo path
with every character outside `[a-zA-Z0-9._-]` turned into `-`, plus
`-mr<n>`/`-pr<n>`):

```
---
type: nightshift-pr-review
forge: github
project: "owner/repo"
number: 1
title: "Fix auth"
author: "alice"
url: https://github.com/owner/repo/pull/1
sha: <head sha>
base: <base sha>
verdict: LGTM
lines: 42
cost_usd: 0.03
model: sonnet
reviewed: 2026-10-12T18:00:00Z
---

# owner/repo#1 — Fix auth

[Open PR](https://github.com/owner/repo/pull/1) · @alice · 42 lines · head `abcd1234`

<the reviewer's reply, verbatim>
```

`verdict` is one of `LGTM`, `CONCERNS`, `BLOCKING`, or `UNKNOWN` (no parseable
`VERDICT:` line in the reply). `cost_usd` is `n/a` when the agent reports no
cost (always true for opencode today). `model` is the selector's display
form — a bare model name for claude, or the full `agent/provider/model` for
opencode.

If a note already exists at that slug but belongs to a different
project/forge, the change falls back to a `<slug>-p<repo-id>.md` note
instead. If *that* one is also taken by something unrelated, the change
fails outright (`note slug ... collides with ... and ... — rename one by
hand`) rather than silently overwriting either file.

The note is committed into `~/.nightshift`'s own git history
(`review: <label> (<verdict>)`, touching only that file) when
`~/.nightshift` happens to be a git repo; if it isn't, the note is just
written as a plain file. Either way a commit failure never fails the
review — it's printed and the review still counts.

## Mirror

Set `NIGHTSHIFT_PR_MIRROR` to also write the exact same note bytes to
`<mirror>/<slug>.md` (a leading `~/` is expanded). If that directory sits
inside a git work tree, the copy is committed there too, scoped to just that
one file. The mirror is **write-only** — it's never read back for
already-reviewed or collision decisions, so stale notes left there from
another tool don't affect anything except getting overwritten if a review
ever lands on the exact same slug. A mirror failure (unwritable directory,
failed commit) is printed and never fails the review. Changes skipped for
being over the size cap are mirrored too.

## Reviewer model

`--model`/`NIGHTSHIFT_PR_MODEL` takes the same agent selector syntax as the
Ralph-loop runner's `model:`: a bare name (always claude, e.g. `sonnet`,
`opus`) or `opencode/<provider>/<model>`. The default is `sonnet`. A bad
selector fails before anything is created or any API call is made, naming
where it came from (flag/env/config/default).

To use a free [OpenCode Zen](https://opencode.ai) model as the reviewer:

```sh
NIGHTSHIFT_PR_MODEL=opencode/opencode/longcat-2.5-preview-free nightshift pr --pending
```

(`opencode models opencode` lists what's currently available.) When the
selector names an OpenCode Zen model, `nightshift pr` automatically allows
the review sandbox to reach `opencode.ai` (`sbx policy allow network
--sandbox <name> opencode.ai`), on top of anything in `NIGHTSHIFT_PR_EGRESS`
(a comma-separated list of extra `host`/`host:port` entries, for another
opencode provider or a homelab model server). Claude needs no extra egress —
the sandbox baseline already allows the Anthropic API. A failed policy call
fails the run before any review and still removes the sandbox.

**A usage limit** (claude only) is checked before any API call: if the
marker from a previous rate-limited run is still active, `nightshift pr`
prints `usage limit active until <time> — skipping` and exits `0` without
touching the network — for both explicit targets and `--pending`. A review
that comes back rate-limited mid-`--pending` stops the rest of that pass
(the change itself isn't counted as failed), writes the marker, and appends
` · limited: until <time>` to the summary. A transient error (an opencode
provider's free-tier rate limit counts as transient too) retries the same
change once after a short wait, then counts as a normal failure if it
recurs. `--list` and `--dry-run` ignore the usage-limit marker.

**Privacy.** The reviewer sees your full diff and source tree. Some free
OpenCode Zen models (for example `big-pickle`, and the `mimo-*`/`ling-*`
free models) let the provider use prompts and code to improve the model
during the free period; others (for example `longcat-2.5-preview-free`,
`step-5-preview-free`, `space-bunny-free`) are zero-retention. For work or
private code, pick a zero-retention model — and recheck OpenCode Zen's own
page before relying on this list, since it changes.

## Automation

`deploy/` ships three files, **none installed or enabled by this binary or
the Makefile**:

- `deploy/nightshift-pr.sh` — the wrapper systemd runs, invoking
  `nightshift pr --pending`.
- `deploy/nightshift-pr.service` / `deploy/nightshift-pr.timer` — a oneshot
  service plus a timer firing at 11:00, 14:00, and 16:30 on weekdays.

To enable, **only after you've hand-run `nightshift pr --pending` a few
times and read the notes it writes**:

```sh
cp deploy/nightshift-pr.sh ~/.local/bin/ && chmod +x ~/.local/bin/nightshift-pr.sh
cp deploy/nightshift-pr.service deploy/nightshift-pr.timer ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now nightshift-pr.timer
```

The wrapper logs to `~/.nightshift/logs/pr.log` and treats a failed review
(exit `1`) as a non-failure outcome — already logged — leaving only a
usage/config error (exit `2`) to mark the systemd unit failed. Running it
alongside `nightshift.timer` (the Ralph-loop nightly run) is fine; they lock
independently (`pr/.lock` vs. a per-spec lock under `runs/.locks/`) and never
touch each other's state.

## Containment

- **No agent ever runs on the host.** The reviewer runs only inside its one
  sandbox, `nightshift-pr-<run-id>`.
- **Every forge API call from the host is a `GET`.** There is no code path
  that sends `POST`/`PUT`/`PATCH`/`DELETE`, and no `--post` flag exists to
  add one.
- **The persistent bare clone under `~/.nightshift/pr/repos/` has no
  credentials in the remote URL or git config.** Fetching the change's head
  and target branch happens once, into `refs/cr/<n>`, using a per-request
  HTTP auth header (`http.<scheme://host>/.extraHeader`) rather than a
  credential embedded in the remote URL or written to the clone's on-disk
  config — so a token never appears in a log, a note, `--dry-run` output, or
  the clone itself.
- **The review sandbox never sees `.git`.** The reviewed commit's tree is
  exported into a scratch directory under `~/.nightshift/pr/work/<slug>/tree/`
  with no `.git` at all, alongside a separately-written `changes.diff` and
  `commits.txt` — there's no git history, hooks, or config for an untrusted
  reviewer to touch even read-only.
- **The reviewer's tools are read-only.** For claude: `--tools
  Read,Grep,Glob`, same as `nightshift review`'s own reviewer. For opencode:
  the agent package's own read-only invocation. Neither gets a
  permission-bypass flag.
- Redirects across hosts are refused during every forge API call — the
  token is never sent off-host — and `checkCloneURL` applies the same rule
  to the git clone/fetch itself.
