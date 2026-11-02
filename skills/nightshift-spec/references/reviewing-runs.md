# Reviewing a nightshift run

Read these first, all under `~/.nightshift/`:

| File | Tells you |
|---|---|
| `runs/<slug>/runner.log` | Every iteration, verify GREEN/RED, resets, owns violations, retries, review round |
| `runs/<slug>/run.json` | status, iterations, commits, base, last_green, spec_hash, planned, history |
| `results/<slug>-<date>[-N].md` | The result note: summary, PROGRESS tail, post-run checks |
| `results/<slug>-<date>-review[-N].md` | The automatic review, one section per lens |
| `runs/<slug>/attempts/iter-N.{patch,log,dirty.patch}` | What a red or reset iteration tried, and the failing verify output |
| `runs/<slug>/repo/PROGRESS.md`, `QUESTION.md` | The plan, the agent's journal, its open question |

## By status

- **complete:** check the review against the code. The lens can't run tests, so treat
  "verify not confirmed" as answered when runner.log shows `verify GREEN at <reviewed sha>`.
  Sort the findings into real bugs, test-strength items and style. Confirm the post-run
  checks are all `ok`.
- **red:** read `attempts/iter-N.log` for the failing test. Ask whether it fails on the base
  too (a broken test on main, often time-dependent), or only because of the agent's change.
  A red run can also hide a QUESTION.md, so check the clone.
- **stalled:** two iterations with no progress. Look at whether planning passes "failed"
  despite writing a plan (a result-parsing problem), or whether the agent kept being reset
  (owns, red).
- **blocked:** read QUESTION.md. Answer it by editing the spec, which changes its hash so
  the next run plans again and clears the stale question.
- **capped:** if it was still landing green commits, re-run it. `extend_iter` should
  normally prevent this.
- **(checks failed) in the notification:** read the `Post-run checks` block. A FAIL there
  doesn't mean the work failed.

## Merging finished work

```sh
cd <repo>
git checkout main
git merge spec/<slug>
git push origin main        # later specs take their base from origin/main
make install                # for nightshift itself: the running binary is what changed
nightshift done <slug>
```

Write findings worth fixing later into the repo's `docs/backlog.md`.
