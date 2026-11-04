package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultWatchSeconds = 2

// Seams: the terminal check, the interval wait and the header clock.
var (
	stdoutIsTerminal = func() bool { return isCharDevice(os.Stdout) }
	watchSleep       = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
	watchNow = time.Now
)

// watchFlag is the -w/--watch flag. It is a bool flag so a bare -w parses;
// mergeWatchArgs rewrites every bare form to --watch=<value> first, so Set
// only ever sees an explicit value. Validation happens after Parse.
type watchFlag struct {
	raw string
}

func (w *watchFlag) String() string     { return w.raw }
func (w *watchFlag) IsBoolFlag() bool   { return true }
func (w *watchFlag) Set(s string) error { w.raw = s; return nil }

// seconds validates the raw value: whole seconds from 1 to 3600.
func (w *watchFlag) seconds() (int, bool) {
	if !allDigits(w.raw) {
		return 0, false
	}
	n, err := strconv.Atoi(w.raw)
	if err != nil || n < 1 || n > 3600 {
		return 0, false
	}
	return n, true
}

func isWatchToken(a string) bool {
	return a == "-w" || a == "--w" || a == "-watch" || a == "--watch"
}

func isWatchAssign(a string) bool {
	for _, p := range []string{"-w=", "--w=", "-watch=", "--watch="} {
		if strings.HasPrefix(a, p) {
			return true
		}
	}
	return false
}

// mergeWatchArgs rewrites bare -w/--watch (optionally followed by an
// all-digit word) to --watch=<value> for flag parsing, and returns the
// original args minus the watch flag for the header.
func mergeWatchArgs(args []string) (parse, display []string) {
	valueFlags := map[string]bool{}
	for _, n := range []string{"since", "done", "repo"} {
		valueFlags["-"+n] = true
		valueFlags["--"+n] = true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			parse = append(parse, args[i:]...)
			display = append(display, args[i:]...)
			return
		case isWatchToken(a):
			v := strconv.Itoa(defaultWatchSeconds)
			if i+1 < len(args) && args[i+1] != "" && allDigits(args[i+1]) {
				v = args[i+1]
				i++
			}
			parse = append(parse, "--watch="+v)
		case isWatchAssign(a):
			parse = append(parse, a)
		case valueFlags[a] && i+1 < len(args):
			parse = append(parse, a, args[i+1])
			display = append(display, a, args[i+1])
			i++
		default:
			parse = append(parse, a)
			display = append(display, a)
		}
	}
	return
}

// watchStatus redraws render's output every interval until the interrupt
// context is cancelled, then returns 0.
func watchStatus(stdout io.Writer, interval time.Duration, display []string, render func(io.Writer) error) int {
	ctx, stop := newInterruptContext()
	defer stop()
	tty := stdoutIsTerminal()
	secs := int(interval / time.Second)
	left := fmt.Sprintf("Every %ds: %s", secs, strings.Join(append([]string{"nightshift status"}, display...), " "))
	for frame := 0; ; frame++ {
		var body bytes.Buffer
		if err := render(&body); err != nil {
			fmt.Fprintf(&body, "nightshift: %v\n", err)
		}
		ts := watchNow().Format("Mon Jan 2 15:04:05")
		pad := 80 - len(left) - len(ts)
		if pad < 1 {
			pad = 1
		}
		var out bytes.Buffer
		if tty {
			out.WriteString("\x1b[H\x1b[2J")
		} else if frame > 0 {
			out.WriteString("\n")
		}
		out.WriteString(left + strings.Repeat(" ", pad) + ts + "\n\n")
		out.Write(body.Bytes())
		stdout.Write(out.Bytes())
		if ctx.Err() != nil || watchSleep(ctx, interval) != nil {
			break
		}
	}
	if tty {
		io.WriteString(stdout, "\n")
	}
	return 0
}
