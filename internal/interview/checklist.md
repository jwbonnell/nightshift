- Verify passes on the base commit today (nightshift lint checks this; flag any doubt).
- One repo per spec. Cross-repo work is split, and the later spec's first task checks its
  prerequisite and writes QUESTION.md if it's missing.
- Each task is one independently committable step and names the tests it adds.
- Every acceptance criterion maps to a task or a named test. Nothing is required that no
  task produces.
- max_iter ≥ number of tasks + 2.
- Context names real files, functions and the exact error text.
- Out of scope lists the tempting adjacent work.
- Nothing silently depends on the machine: time zone, network beyond the sandbox
  allow-list, HOME, global git config, absolute host paths, Docker inside verify.
- The verify command is hermetic and reasonably fast, and it covers the code the tasks change.
- No template placeholders remain.
