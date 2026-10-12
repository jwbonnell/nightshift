package review

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDeployPRFiles guards P10: deploy/ must hold the three nightshift-pr
// systemd files exactly as the spec specifies them, byte for byte. Unlike
// internal/runner's TestDeployFilesByteIdenticalToSpec, this spec was never
// checked into SPEC.md (it's the nightshift-pr-review build spec, not the
// original nightshift one), so the expected content is reproduced here
// literally instead of extracted from a fenced block.
func TestDeployPRFiles(t *testing.T) {
	want := map[string]string{
		"nightshift-pr.sh": `#!/bin/sh
# nightshift PR pre-review pass — invoked by systemd (nightshift-pr.timer) during the
# workday. Pre-reviews every open PR/MR where you're a requested reviewer, on every
# configured forge, into ~/.nightshift/reviews/. Never posts to GitLab or GitHub.
# Install: cp to ~/.local/bin/nightshift-pr.sh && chmod +x
# DO NOT enable the timer until you have hand-run ` + "`nightshift pr --pending`" + ` a few times
# and read the notes it writes.
set -u
export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/bin:/usr/bin:/bin"
home="${NIGHTSHIFT_HOME:-$HOME/.nightshift}"
mkdir -p "$home/logs"
log="$home/logs/pr.log"
echo "$(date '+%F %T') start" >>"$log"
nightshift pr --pending >>"$log" 2>&1
rc=$?
echo "$(date '+%F %T') exit $rc" >>"$log"
# A failed review (1) is an outcome, logged above; only usage/config errors (2) fail the unit.
[ "$rc" -eq 2 ] && exit 1
exit 0
`,
		"nightshift-pr.service": `[Unit]
Description=nightshift: pre-review open PRs/MRs awaiting your review
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
ExecStart=%h/.local/bin/nightshift-pr.sh
TimeoutStartSec=3h
Nice=10
`,
		"nightshift-pr.timer": `[Unit]
Description=nightshift PR pre-review passes on weekdays

[Timer]
OnCalendar=Mon..Fri 11:00
OnCalendar=Mon..Fri 14:00
OnCalendar=Mon..Fri 16:30
Persistent=false
Unit=nightshift-pr.service

[Install]
WantedBy=timers.target
`,
	}

	for name, want := range want {
		got, err := os.ReadFile(filepath.Join("..", "..", "deploy", name))
		if err != nil {
			t.Fatalf("read deploy/%s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("deploy/%s does not match the spec byte-for-byte\ngot:  %q\nwant: %q", name, got, want)
		}
	}
}
