# Backlog

Known leftovers, deferred ideas and lessons from running nightshift. Last updated
2026-11-02. When a review leaves findings that aren't worth a spec yet, add them here.
When a spec fixes one, remove it.

## Shipped 2026-10-25 → 2026-11-02

| Spec | What it added |
|---|---|
| nightshift-plan-ahead (+ fixes) | `run --plan-only`, `ready --plan`, `planned` status; a question is never a plan; `spec_hash` recorded per planning pass; a stale QUESTION.md is cleared when the spec changes |
| nightshift-cap-extend | `extend_iter`: past `max_iter`, one more iteration at a time while each is green with a new commit |
| nightshift-accept-gates | `accept:` and holdout tests under `<Home>/holdout/<slug>/`, run in a throwaway workspace; `gate_rounds` hand-backs; holdout details withheld from the agent |
| nightshift-small-fixes | End-of-run cleanup removes only sandboxes the run created; planning edge cases; exact extension count; `CloneDir` everywhere |
| nightshift-completion | `nightshift completion bash`, hidden `__complete`, a coverage test for every flag; gate outcome fixes |
| nightshift-opencode-transient | opencode errors only from real error events or raw stderr; only 429/5xx/quota/overload retried |
| nightshift-visual-check | `screenshots:` in `.nightshift.md` feeds the iteration, planning and both review prompts |
| nightshift-followups | opencode `Transient` uses Parse's decision; bash completion handles `:`/`=`; gates cut off by `--until` are `timed out`/`not run`, not `fail`; `lint` completion case dedupe |
| nightshift-status-hide-done | `status` hides done rows by default; footer `+N done (--done N, --since <dur> or --all to show)`; `--done N`/`--since`/`--all` unchanged |
| (by hand) | `newTestRun` resolves the limit config, so the rate-limit test no longer hangs between 8pm and 2am Eastern |

## Runner gaps (worth a spec)

- **A planner that edits code isn't flagged.** The runner only logs a violation when a
  planning pass *commits*. Uncommitted edits are captured to `attempts/iter-N.dirty.patch`,
  reset, and the pass still counts as planned. Log and append to PROGRESS.md the same way
  as a planner commit (`loop.go`, the planning branch).
- **A question is lost when the same iteration goes red.** `loop()` checks the red streak
  before QUESTION.md, so an iteration that writes QUESTION.md and leaves verify red ends
  `red`, and the question is missing from the result note. Blocked should win, or the note
  should include the question either way.

## Test and cleanup leftovers

- `cmd/nightshift/complete_test.go`, `TestCompletionBashEndToEnd`: the stub
  `_get_comp_words_by_ref` hardcodes `words=(nightshift run --until 06:00 d)`, so with
  bash-completion loaded the `--until=06:00` case only re-tests the space form. One stub case
  also runs bash and then throws the result away.
- `internal/runner/gate.go`: the "not run" loop appears twice; `gateRes.ok` duplicates
  `status`; `r.gate = nil` after a hand-back is set in both `gate.go` and `loop.go`; the
  holdout path is resolved by both `checkHoldoutOutsideRepo` and `resolveHoldoutDir`, so the
  checked path may not be the walked one.
- `cmd/nightshift/complete.bash`: trims the readline prefix by hand and then also calls
  `__ltrim_colon_completions`. Mostly a no-op, but pick one or explain both in a comment.
- `internal/review/prompt.go` (`nightshift pr` screenshots): reads `.nightshift.md` with
  `os.ReadFile` after `Lstat` (a small TOCTOU gap; the runner uses `ReadScaffold` with
  `O_NOFOLLOW`). Its tests lack a symlinked directory, `.PNG` and nested cases, and the
  section's trailing whitespace differs from the runner's copy.
- `internal/runner/prompt.go`: the screenshots declaration is read from `sp.Repo` (the host
  checkout), not the clone. Add a comment saying this is deliberate. The runner's review
  lists paths with a `repo/` prefix, which is also deliberate; mention it in the docs.
- Gate tests: `TestHoldoutSymlinkedSlugDir` doesn't assert the dry-run `gate:` line; the
  "unresolvable holdout path" case relies on a 5000-character path (ENAMETOOLONG).
- `TestCompleteNoHome`: the "home is a regular file" case doesn't test what its name says.
- `docs/known_issues.md` (custom images): no end-to-end test of `image:` reaching the real
  sandbox, and `real` shadows the builtin in `specrun.go`.

## Ideas not started

| Idea | Notes |
|---|---|
| Shared constraint blocks (`include:`) | Specs for this repo repeat the same verify line and constraints |
| Morning summary in the result note | Per-task commit ranges, tests added, deviations logged in PROGRESS.md |
| `nightshift followup <slug>` | A draft spec prefilled from the latest review's findings |
| `nightshift accept <slug>` | Merge, push, `done` in one command |
| `nightshift stats` | Outcomes, iterations and review verdicts per spec, kept automatically |
| Phone notifications (ntfy) | So the overnight result reaches the phone |
| Per-task budgets / model escalation | After repeated reds on one task. Low priority: red runs are rare |
| Persistent build caches for verify | Speeds up Flutter/Dart verify; must stay read-only to the agent |
| `base_verify: red` for greenfield specs | `ready --no-verify` covers it today |
| Post-mortem on red/stalled | Deferred: those endings are rare |
| `review_rounds: 1` by default for nightshift specs | Most reviews come back `concerns`, which isn't handed back anyway |

## Lessons from running it

- 89 runs to 2026-10-26: 69 complete, 15 capped (all still landing commits; now handled by
  `extend_iter`), 5 blocked (4 at planning; now caught early with `ready --plan`), 1 red.
- Push after merging. A spec's base is `origin/<base>`, so a local-only merge means the next
  spec starts from old code.
- `make install` after merging nightshift itself. The running binary is what changes
  behavior; the repo's tests don't.
- Use `run --fresh` when main has moved under a blocked or red run. Resumes keep the old
  base, and an unchanged spec keeps its QUESTION.md.
- `review_rounds` only hands back a `reject` verdict. `concerns` completes the run. Read
  the review either way.
- Review lenses can't run tests and often say verify wasn't confirmed. runner.log is the
  evidence: `verify GREEN at <sha>` for the reviewed commit settles it.
- Weak planner models (seen with `opencode/big-pickle`) can write code during planning or
  stop mid-plan. Plan with `opus`.
- A test that depends on the wall clock can pass all day and fail every night. Inject the
  clock and the sleeper, and resolve settings the same way `Run` does.

## Skills

`skills/nightshift-spec/` is the agent skill for drafting specs and reviewing runs. See
`skills/README.md` for installing it.
