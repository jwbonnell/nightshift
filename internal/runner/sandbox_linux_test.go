package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHelperProcess is not a real test. It is re-exec'd as a subprocess by
// TestChildHasOwnProcessGroup and TestCancelStillKillsChild via
// os.Args[0], gated on NIGHTSHIFT_TEST_HELPER so a normal `go test` run is
// a no-op.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("NIGHTSHIFT_TEST_HELPER") {
	case "":
		return
	case "pgrp":
		fmt.Println(syscall.Getpgrp())
		os.Exit(0)
	case "sleep":
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
}

func TestChildHasOwnProcessGroup(t *testing.T) {
	t.Setenv("NIGHTSHIFT_TEST_HELPER", "pgrp")
	var out, errb strings.Builder
	err := execSplit(context.Background(), nil, &out, &errb, os.Args[0], "-test.run=^TestHelperProcess$")
	if err != nil {
		t.Fatalf("execSplit: %v: %s", err, errb.String())
	}
	childPgrp, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatalf("parse child pgrp %q: %v", out.String(), err)
	}
	if childPgrp == syscall.Getpgrp() {
		t.Fatalf("child pgrp %d == test process pgrp, want its own group", childPgrp)
	}
}

func TestCancelStillKillsChild(t *testing.T) {
	t.Setenv("NIGHTSHIFT_TEST_HELPER", "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	_, err := execStream(ctx, nil, io.Discard, os.Args[0], "-test.run=^TestHelperProcess$")
	elapsed := time.Since(start)

	if elapsed >= 5*time.Second {
		t.Fatalf("execStream took %v, want < 5s", elapsed)
	}
	if err == nil {
		t.Fatal("execStream err = nil, want non-nil (child killed)")
	}
}
