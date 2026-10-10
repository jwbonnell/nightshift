# Lessons from past runs (one line each, newest first)

- A spec spanning two repos must be split; gate the second spec on its prerequisite with a QUESTION.md check.
- An acceptance criterion with no task behind it sends the agent building unrequested scaffolding.
- Tests that relied on ~/.gitconfig passed locally and failed in the sandbox — keep test git hermetic.
- A fixed-time test assumed the local zone; the sandbox runs UTC.
- After merging a nightshift change, run `make install` — a stale binary silently kept old behavior.
- An editor with an old buffer open overwrote a corrected spec; reload before saving.
