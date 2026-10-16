#!/bin/sh
# nightshift nightly runner — invoked by systemd (nightshift.timer) after hours.
# Runs ready specs overnight, one after another, through the sandboxed Ralph
# loop, stopping at 06:00 if unfinished (the run resumes the next night).
# Install: cp to ~/.local/bin/nightshift-nightly.sh && chmod +x
# DO NOT enable the timer until you have hand-run several specs and reviewed their diffs.
set -u
export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/bin:/usr/bin:/bin"
home="${NIGHTSHIFT_HOME:-$HOME/.nightshift}"
mkdir -p "$home/logs"
log="$home/logs/nightly.log"
echo "$(date '+%F %T') start" >>"$log"
nightshift run --next --until 06:00 >>"$log" 2>&1
rc=$?
case $rc in
  7) echo "$(date '+%F %T') nothing started (busy)" >>"$log" ;;
  *) echo "$(date '+%F %T') exit $rc" >>"$log" ;;
esac
# Only a runner error (1) marks the unit failed; blocked/red/stalled/capped/rate-limited are outcomes.
[ "$rc" -eq 1 ] && exit 1
exit 0
