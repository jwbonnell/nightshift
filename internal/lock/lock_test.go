package lock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// reapedPID returns a PID guaranteed to be dead: a child process we spawned
// and already waited on.
func reapedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn helper process: %v", err)
	}
	return cmd.Process.Pid
}

func withBootTime(t *testing.T, fn func() (int64, bool)) {
	t.Helper()
	orig := bootTime
	bootTime = fn
	t.Cleanup(func() { bootTime = orig })
}

func TestAcquireRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.lock")

	l, err := TryAcquire(path, "first")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file missing: %v", err)
	}

	_, err = TryAcquire(path, "second")
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("got %v, want *HeldError", err)
	}
	if held.Holder.PID != os.Getpid() {
		t.Fatalf("holder pid = %d, want %d", held.Holder.PID, os.Getpid())
	}
	if held.Path != path {
		t.Fatalf("holder path = %q, want %q", held.Path, path)
	}

	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock file still exists after Release")
	}

	l2, err := TryAcquire(path, "third")
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	l2.Release()
}

func TestReleaseNeverRemovesSomeoneElsesLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.lock")

	l, err := TryAcquire(path, "first")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}

	newContents := encode(Info{PID: os.Getpid(), Label: "other", Start: time.Now(), Nonce: "replaced"})
	if err := os.WriteFile(path, newContents, 0o644); err != nil {
		t.Fatalf("overwrite lock: %v", err)
	}

	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock after release: %v", err)
	}
	if !bytes.Equal(got, newContents) {
		t.Fatalf("lock contents changed: got %q, want %q", got, newContents)
	}
}

func TestStaleLockReclaimedSameProcessDeadPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.lock")

	deadPID := reapedPID(t)
	stale := Info{PID: deadPID, Label: "gone", Start: time.Now().Add(-time.Hour), Nonce: "stale-nonce"}
	if err := os.WriteFile(path, encode(stale), 0o644); err != nil {
		t.Fatalf("plant stale lock: %v", err)
	}

	l, err := TryAcquire(path, "me")
	if err != nil {
		t.Fatalf("TryAcquire over stale lock: %v", err)
	}
	defer l.Release()

	got, _, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.PID != os.Getpid() || got.Label != "me" {
		t.Fatalf("lock not reclaimed: got %+v", got)
	}
}

func TestBootTimeTolerance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.lock")

	const fakeBoot = int64(1_000_000)
	withBootTime(t, func() (int64, bool) { return fakeBoot, true })

	t.Run("within tolerance stays held", func(t *testing.T) {
		stale := Info{PID: os.Getpid(), Label: "holder", Start: time.Now(), Boot: fakeBoot + 5, Nonce: "n1"}
		if err := os.WriteFile(path, encode(stale), 0o644); err != nil {
			t.Fatalf("plant lock: %v", err)
		}
		_, err := TryAcquire(path, "challenger")
		var held *HeldError
		if !errors.As(err, &held) {
			t.Fatalf("got %v, want *HeldError", err)
		}
	})

	t.Run("beyond tolerance is reclaimed", func(t *testing.T) {
		stale := Info{PID: os.Getpid(), Label: "holder", Start: time.Now(), Boot: fakeBoot + 61, Nonce: "n2"}
		if err := os.WriteFile(path, encode(stale), 0o644); err != nil {
			t.Fatalf("plant lock: %v", err)
		}
		l, err := TryAcquire(path, "challenger")
		if err != nil {
			t.Fatalf("TryAcquire: %v", err)
		}
		l.Release()
	})
}

func TestReclaimGuardStale(t *testing.T) {
	origWait, origPoll := reclaimGuardWait, reclaimPollInterval
	reclaimGuardWait = 300 * time.Millisecond
	reclaimPollInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		reclaimGuardWait, reclaimPollInterval = origWait, origPoll
	})

	plantStale := func(t *testing.T, dir string) (path string, contents []byte) {
		t.Helper()
		path = filepath.Join(dir, "x.lock")
		deadPID := reapedPID(t)
		stale := Info{PID: deadPID, Label: "gone", Start: time.Now().Add(-time.Hour), Nonce: "stale-nonce"}
		contents = encode(stale)
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			t.Fatalf("plant stale lock: %v", err)
		}
		return path, contents
	}

	t.Run("dead guard holder does not block", func(t *testing.T) {
		dir := t.TempDir()
		path, _ := plantStale(t, dir)
		deadGuardPID := reapedPID(t)
		guard := Info{PID: deadGuardPID, Label: "reclaim", Start: time.Now(), Nonce: "guard-dead"}
		if err := os.WriteFile(path+".reclaim", encode(guard), 0o644); err != nil {
			t.Fatalf("plant dead guard: %v", err)
		}
		l, err := TryAcquire(path, "me")
		if err != nil {
			t.Fatalf("TryAcquire: %v", err)
		}
		l.Release()
	})

	t.Run("old guard does not block", func(t *testing.T) {
		dir := t.TempDir()
		path, _ := plantStale(t, dir)
		guard := Info{PID: os.Getpid(), Label: "reclaim", Start: time.Now().Add(-40 * time.Second), Nonce: "guard-old"}
		if err := os.WriteFile(path+".reclaim", encode(guard), 0o644); err != nil {
			t.Fatalf("plant old guard: %v", err)
		}
		l, err := TryAcquire(path, "me")
		if err != nil {
			t.Fatalf("TryAcquire: %v", err)
		}
		l.Release()
	})

	t.Run("live fresh guard blocks and leaves the stale lock untouched", func(t *testing.T) {
		dir := t.TempDir()
		path, staleContents := plantStale(t, dir)
		guard := Info{PID: os.Getpid(), Label: "reclaim", Start: time.Now(), Nonce: "guard-fresh"}
		if err := os.WriteFile(path+".reclaim", encode(guard), 0o644); err != nil {
			t.Fatalf("plant fresh guard: %v", err)
		}

		start := time.Now()
		_, err := TryAcquire(path, "me")
		elapsed := time.Since(start)
		if err == nil {
			t.Fatalf("expected an error, got nil")
		}
		if elapsed < reclaimGuardWait {
			t.Fatalf("returned after %s, want at least %s", elapsed, reclaimGuardWait)
		}

		got, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read lock: %v", rerr)
		}
		if !bytes.Equal(got, staleContents) {
			t.Fatalf("stale lock bytes changed")
		}
	})
}

func TestPidAliveEPERM(t *testing.T) {
	if !pidAlive(1) {
		t.Errorf("pidAlive(1) = false, want true (EPERM counts as alive)")
	}
	if pidAlive(reapedPID(t)) {
		t.Errorf("pidAlive(reaped pid) = true, want false")
	}
	if pidAlive(0) {
		t.Errorf("pidAlive(0) = true, want false")
	}
	if pidAlive(-1) {
		t.Errorf("pidAlive(-1) = true, want false")
	}
	if !pidAlive(os.Getpid()) {
		t.Errorf("pidAlive(own pid) = false, want true")
	}
}

func TestLockVanishesBeforeRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.lock")

	first := Info{PID: os.Getpid(), Label: "first", Start: time.Now(), Nonce: "n1"}
	if err := os.WriteFile(path, encode(first), 0o644); err != nil {
		t.Fatalf("plant first: %v", err)
	}

	origHook := afterLinkExists
	called := false
	afterLinkExists = func(p string) {
		if called {
			return
		}
		called = true
		second := Info{PID: os.Getpid(), Label: "second", Start: time.Now(), Nonce: "n2"}
		if err := os.WriteFile(p, encode(second), 0o644); err != nil {
			t.Fatalf("plant second: %v", err)
		}
	}
	t.Cleanup(func() { afterLinkExists = origHook })

	_, err := TryAcquire(path, "challenger")
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("got %v, want *HeldError", err)
	}
	if held.Holder.Label != "second" {
		t.Fatalf("holder label = %q, want %q", held.Holder.Label, "second")
	}
	if strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("error mentions 'unreadable': %v", err)
	}
}

func TestReclaimLostToAnotherReclaimer(t *testing.T) {
	t.Run("replaced by a live winner", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "x.lock")
		deadPID := reapedPID(t)
		stale := Info{PID: deadPID, Label: "gone", Start: time.Now().Add(-time.Hour), Nonce: "stale"}
		if err := os.WriteFile(path, encode(stale), 0o644); err != nil {
			t.Fatalf("plant stale: %v", err)
		}

		origHook := beforeReclaimRemove
		called := false
		beforeReclaimRemove = func(p string) {
			if called {
				return
			}
			called = true
			winner := Info{PID: os.Getpid(), Label: "winner", Start: time.Now(), Nonce: "winner-nonce"}
			if err := os.WriteFile(p, encode(winner), 0o644); err != nil {
				t.Fatalf("plant winner: %v", err)
			}
		}
		t.Cleanup(func() { beforeReclaimRemove = origHook })

		_, err := TryAcquire(path, "me")
		var held *HeldError
		if !errors.As(err, &held) {
			t.Fatalf("got %v, want *HeldError", err)
		}
		if held.Holder.Label != "winner" {
			t.Fatalf("holder label = %q, want %q", held.Holder.Label, "winner")
		}
		if strings.Contains(err.Error(), "reclaim contention") {
			t.Fatalf("error should name the winner, not report contention: %v", err)
		}
	})

	t.Run("removed with no replacement", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "x.lock")
		deadPID := reapedPID(t)
		stale := Info{PID: deadPID, Label: "gone", Start: time.Now().Add(-time.Hour), Nonce: "stale"}
		if err := os.WriteFile(path, encode(stale), 0o644); err != nil {
			t.Fatalf("plant stale: %v", err)
		}

		origHook := beforeReclaimRemove
		called := false
		beforeReclaimRemove = func(p string) {
			if called {
				return
			}
			called = true
			os.Remove(p)
		}
		t.Cleanup(func() { beforeReclaimRemove = origHook })

		l, err := TryAcquire(path, "me")
		if err != nil {
			t.Fatalf("TryAcquire: %v", err)
		}
		defer l.Release()

		got, _, err := Read(path)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got.Label != "me" {
			t.Fatalf("label = %q, want %q", got.Label, "me")
		}
	})
}

func TestAcquireTimesOutWithHeldError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.lock")

	l, err := TryAcquire(path, "holder")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer l.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_, err = Acquire(ctx, path, "challenger")
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("got %v, want *HeldError", err)
	}
	if held.Holder.PID != os.Getpid() {
		t.Fatalf("holder pid = %d, want %d (own pid)", held.Holder.PID, os.Getpid())
	}
}

func TestAcquireSucceedsOnceReleased(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.lock")

	l, err := TryAcquire(path, "holder")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		l.Release()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	l2, err := Acquire(ctx, path, "challenger")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	l2.Release()
}

func TestProbe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.lock")

	if _, live := Probe(path); live {
		t.Fatalf("Probe on missing file reports live")
	}

	l, err := TryAcquire(path, "holder")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	info, live := Probe(path)
	if !live {
		t.Fatalf("Probe on held lock reports not live")
	}
	if info.PID != os.Getpid() || info.Label != "holder" {
		t.Fatalf("Probe info = %+v, want pid=%d label=holder", info, os.Getpid())
	}

	l.Release()
	if _, live := Probe(path); live {
		t.Fatalf("Probe after Release reports live")
	}
}

// TestReclaimRace re-execs itself as 8 child processes (the
// NIGHTSHIFT_LOCK_CHILD=1 protocol below), each racing 2 goroutines to
// reclaim one shared stale lock — 16 concurrent contenders in total. Exactly
// one must win.
func TestReclaimRace(t *testing.T) {
	if os.Getenv("NIGHTSHIFT_LOCK_CHILD") == "1" {
		runReclaimRaceChild(t)
		return
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "race.lock")
	deadPID := reapedPID(t)
	stale := Info{PID: deadPID, Label: "gone", Start: time.Now().Add(-time.Hour), Nonce: "stale"}
	if err := os.WriteFile(path, encode(stale), 0o644); err != nil {
		t.Fatalf("plant stale lock: %v", err)
	}

	const n = 8
	barrier := filepath.Join(dir, "barrier")
	var cmds []*exec.Cmd
	var results []string

	for i := 0; i < n; i++ {
		done := filepath.Join(dir, fmt.Sprintf("ready-%d", i))
		result := filepath.Join(dir, fmt.Sprintf("result-%d.txt", i))
		results = append(results, result)

		cmd := exec.Command(os.Args[0], "-test.run=^TestReclaimRace$")
		cmd.Env = append(os.Environ(),
			"NIGHTSHIFT_LOCK_CHILD=1",
			"NIGHTSHIFT_LOCK_PATH="+path,
			"NIGHTSHIFT_LOCK_BARRIER="+barrier,
			"NIGHTSHIFT_LOCK_DONE="+done,
			"NIGHTSHIFT_LOCK_RESULT="+result,
		)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			t.Fatalf("start child %d: %v", i, err)
		}
		cmds = append(cmds, cmd)
	}

	for i := 0; i < n; i++ {
		done := filepath.Join(dir, fmt.Sprintf("ready-%d", i))
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(done); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("child %d never signaled ready", i)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	if err := os.WriteFile(barrier, nil, 0o644); err != nil {
		t.Fatalf("release barrier: %v", err)
	}

	// Wait for every child to finish racing and write its result BEFORE
	// reaping any of them: reaping a winner early frees its PID, which
	// would let a still-racing child wrongly see it as stale.
	for i, rp := range results {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(rp); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("child %d never wrote a result", i)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := os.WriteFile(barrier+".release", nil, 0o644); err != nil {
		t.Fatalf("release exit barrier: %v", err)
	}

	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child %d exited with error: %v", i, err)
		}
	}

	var winners []int
	var lostWinners []int
	for i, rp := range results {
		data, err := os.ReadFile(rp)
		if err != nil {
			t.Fatalf("read result %d: %v", i, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) != 2 {
				t.Fatalf("malformed result line from child %d: %q", i, line)
			}
			pid, err := strconv.Atoi(fields[1])
			if err != nil {
				t.Fatalf("malformed pid in result line from child %d: %q", i, line)
			}
			switch fields[0] {
			case "WIN":
				winners = append(winners, pid)
			case "LOST":
				lostWinners = append(lostWinners, pid)
			default:
				t.Fatalf("unexpected result line from child %d: %q", i, line)
			}
		}
	}

	if got, want := len(winners)+len(lostWinners), n*2; got != want {
		t.Fatalf("got %d racer outcomes, want %d", got, want)
	}
	if len(winners) != 1 {
		t.Fatalf("got %d winners (%v), want exactly 1", len(winners), winners)
	}
	winnerPID := winners[0]

	for _, named := range lostWinners {
		if named != winnerPID {
			t.Fatalf("a LOST outcome named pid %d, want winner pid %d", named, winnerPID)
		}
	}

	got, _, err := Read(path)
	if err != nil {
		t.Fatalf("Read final lock: %v", err)
	}
	if got.PID != winnerPID {
		t.Fatalf("lock file pid = %d, want winner pid %d", got.PID, winnerPID)
	}
}

// runReclaimRaceChild implements the NIGHTSHIFT_LOCK_CHILD=1 side of
// TestReclaimRace: signal ready, wait for the barrier, then race two
// goroutines against the shared lock and report the outcome.
func runReclaimRaceChild(t *testing.T) {
	path := os.Getenv("NIGHTSHIFT_LOCK_PATH")
	barrier := os.Getenv("NIGHTSHIFT_LOCK_BARRIER")
	done := os.Getenv("NIGHTSHIFT_LOCK_DONE")
	result := os.Getenv("NIGHTSHIFT_LOCK_RESULT")

	if err := os.WriteFile(done, nil, 0o644); err != nil {
		t.Fatalf("signal ready: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(barrier); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("barrier never released")
		}
		time.Sleep(2 * time.Millisecond)
	}

	const goroutines = 2
	var wg sync.WaitGroup
	lines := make([]string, goroutines)
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			l, err := TryAcquire(path, fmt.Sprintf("pid%d-g%d", os.Getpid(), g))
			var held *HeldError
			switch {
			case err == nil:
				lines[g] = fmt.Sprintf("WIN %d", os.Getpid())
				_ = l // not released: the orchestrator inspects the final lock file
			case errors.As(err, &held):
				lines[g] = fmt.Sprintf("LOST %d", held.Holder.PID)
			default:
				lines[g] = fmt.Sprintf("ERR %v", err)
			}
		}(g)
	}
	wg.Wait()

	if err := os.WriteFile(result, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write result: %v", err)
	}

	// Do not exit yet: exiting frees this process's PID, which would let a
	// still-racing sibling wrongly treat a just-decided winner as stale.
	// Wait until the orchestrator confirms every child has a result.
	releaseDeadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(barrier + ".release"); err == nil {
			break
		}
		if time.Now().After(releaseDeadline) {
			t.Fatalf("exit barrier never released")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
