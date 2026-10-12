#!/bin/sh
# nightshift PR pre-review pass — invoked by systemd (nightshift-pr.timer) during the
# workday. Pre-reviews every open PR/MR where you're a requested reviewer, on every
# configured forge, into ~/.nightshift/reviews/. Never posts to GitLab or GitHub.
# Install: cp to ~/.local/bin/nightshift-pr.sh && chmod +x
# DO NOT enable the timer until you have hand-run `nightshift pr --pending` a few times
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
