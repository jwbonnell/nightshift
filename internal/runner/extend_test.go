package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// newExtendRun builds a run whose planning pass is already done, so every
// iteration is a worker iteration, and whose finish() can be called.
func newExtendRun(t *testing.T, fm string, opts Options, sb *stubSandbox) *run {
	t.Helper()
	repo := newSourceRepo(t)
	opts.Stdout = io.Discard
	opts.Notifier = noopNotifierForTest{}
	r := newTestRun(t, repo, fm, opts, sb)
	r.st.Planned = true
	r.st.SpecHash = r.spec.ContentHash
	return r
}

func commitIter(t *testing.T, dir string, iter int) {
	t.Helper()
	commitFile(t, dir, fmt.Sprintf("f%d.go", iter), "package main\n", fmt.Sprintf("work %d", iter))
}

func TestExtendGrantsWhileGreen(t *testing.T) {
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		commitIter(t, dir, iter)
		if iter == 6 {
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = alwaysGreenVerify
	r := newExtendRun(t, "max_iter: 3\nextend_iter: 5\n", Options{}, sb)

	status, err := r.finish(r.loop())
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}
	if r.st.Iterations != 6 {
		t.Fatalf("Iterations = %d, want 6", r.st.Iterations)
	}
	if h := r.st.History[len(r.st.History)-1]; !strings.Contains(h, " extended=3") {
		t.Fatalf("history = %q, want extended=3", h)
	}
	progress, _ := os.ReadFile(r.progressPath())
	if n := strings.Count(string(progress), "past max_iter — granting extra iteration"); n != 3 {
		t.Fatalf("PROGRESS.md has %d grant lines, want 3:\n%s", n, progress)
	}
	want := "iter 4: past max_iter — granting extra iteration 1 of 5 (last iteration was green with a new commit)"
	if !strings.Contains(string(progress), want) {
		t.Fatalf("PROGRESS.md missing %q:\n%s", want, progress)
	}
}

func TestExtendStopsOnNoCommit(t *testing.T) {
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter != 5 { // iter 5 is the 2nd extension: green, no commit
			commitIter(t, dir, iter)
		} else {
			appendProgressRaw(t, dir, "thinking")
		}
		return nil
	}
	sb.verify = alwaysGreenVerify
	r := newExtendRun(t, "max_iter: 3\nextend_iter: 5\n", Options{}, sb)

	status := r.loop()
	if status != StatusCapped {
		t.Fatalf("status = %v, want Capped", status)
	}
	if r.st.Iterations != 5 || r.extended != 2 {
		t.Fatalf("Iterations = %d extended = %d, want 5 and 2", r.st.Iterations, r.extended)
	}
}

func TestExtendStopsOnRed(t *testing.T) {
	t.Run("single red", func(t *testing.T) {
		sb := newStubSandbox()
		sb.agent = func(ctx context.Context, iter int, dir string) error {
			commitIter(t, dir, iter)
			return nil
		}
		sb.verify = func(iter int, dir string) error {
			if iter == 4 {
				return fmt.Errorf("red")
			}
			return nil
		}
		r := newExtendRun(t, "max_iter: 3\nextend_iter: 5\n", Options{}, sb)
		if status := r.loop(); status != StatusCapped {
			t.Fatalf("status = %v, want Capped", status)
		}
		if r.st.Iterations != 4 || r.extended != 1 {
			t.Fatalf("Iterations = %d extended = %d, want 4 and 1", r.st.Iterations, r.extended)
		}
	})
	// Two reds in a row past the cap can't happen: a grant needs the last
	// iteration green, so one red past the cap already ends the run.
	t.Run("red past the cap", func(t *testing.T) {
		sb := newStubSandbox()
		sb.agent = func(ctx context.Context, iter int, dir string) error {
			commitIter(t, dir, iter)
			return nil
		}
		sb.verify = func(iter int, dir string) error {
			if iter == 3 {
				return fmt.Errorf("red")
			}
			return nil
		}
		r := newExtendRun(t, "max_iter: 2\nextend_iter: 5\n", Options{}, sb)
		status, err := r.finish(r.loop())
		if err != nil {
			t.Fatalf("finish: %v", err)
		}
		if status != StatusCapped {
			t.Fatalf("status = %v, want Capped", status)
		}
		if r.st.Iterations != 3 {
			t.Fatalf("Iterations = %d, want 3", r.st.Iterations)
		}
		if h := r.st.History[len(r.st.History)-1]; !strings.Contains(h, " extended=1") {
			t.Fatalf("history = %q, want extended=1", h)
		}
	})
}

func TestExtendBudgetSpent(t *testing.T) {
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		commitIter(t, dir, iter)
		return nil
	}
	sb.verify = alwaysGreenVerify
	r := newExtendRun(t, "max_iter: 3\nextend_iter: 2\n", Options{}, sb)

	status, err := r.finish(r.loop())
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if status != StatusCapped {
		t.Fatalf("status = %v, want Capped", status)
	}
	if r.st.Iterations != 5 {
		t.Fatalf("Iterations = %d, want 5", r.st.Iterations)
	}
	want := "Hit the iteration cap (3) plus 2 extra iteration(s) granted while it kept landing green commits. Re-run to continue."
	if !strings.Contains(r.resultBody(status), want) {
		t.Fatalf("result note missing %q:\n%s", want, r.resultBody(status))
	}
	if _, msg := r.notifyText(status); msg != want {
		t.Fatalf("notify msg = %q, want %q", msg, want)
	}
	if !strings.Contains(r.resultBody(status), "commits: 5\nextended: 2\n") {
		t.Fatalf("result note frontmatter missing extended after commits:\n%s", r.resultBody(status))
	}
}

// maskRunVolatile blanks everything that differs between two otherwise
// identical runs, so their history entries and result-note frontmatter can be
// compared byte for byte.
func maskRunVolatile(s string) string {
	for _, m := range []struct{ re, with string }{
		{`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2})?([.]\d+)?(Z|[+-]\d{2}:?\d{2})?`, "<time>"}, // timestamps
		{`\b[0-9a-f]{7,40}\b`, "<sha>"},                        // commit/base SHAs, spec hash
		{`/[^\s"']*(TestExtend|nightshift)[^\s"']*`, "<path>"}, // temp paths
		{`\d+(\.\d+)?(ms|s|m|h)\b`, "<dur>"},                   // elapsed times
	} {
		s = regexp.MustCompile(m.re).ReplaceAllString(s, m.with)
	}
	return s
}

func frontmatterOf(body string) string {
	if i := strings.Index(body[3:], "\n---"); strings.HasPrefix(body, "---") && i >= 0 {
		return body[:i+3]
	}
	return body
}

func TestExtendZeroUnchanged(t *testing.T) {
	drive := func(fm string, opts Options) (*run, Status) {
		sb := newStubSandbox()
		sb.agent = func(ctx context.Context, iter int, dir string) error {
			commitIter(t, dir, iter)
			return nil
		}
		sb.verify = alwaysGreenVerify
		r := newExtendRun(t, fm, opts, sb)
		status, err := r.finish(r.loop())
		if err != nil {
			t.Fatalf("finish: %v", err)
		}
		return r, status
	}
	r, status := drive("max_iter: 3\nextend_iter: 0\n", Options{})
	// extend_iter defaults to 10, so "no setting" is not the same as off. The
	// baseline turns the feature off through the flag instead of the spec.
	base, baseStatus := drive("max_iter: 3\n", Options{ExtendIter: "0"})

	if status != StatusCapped || r.st.Iterations != 3 || r.extended != 0 {
		t.Fatalf("status=%v iterations=%d extended=%d", status, r.st.Iterations, r.extended)
	}
	if h := r.st.History[len(r.st.History)-1]; strings.Contains(h, "extended") {
		t.Fatalf("history = %q, want no extended", h)
	}
	if body := r.resultBody(status); strings.Contains(body, "extended") || !strings.Contains(body, "Hit the iteration cap (3) before completing. Re-run to continue.") {
		t.Fatalf("result note changed:\n%s", body)
	}
	if progress, _ := os.ReadFile(r.progressPath()); strings.Contains(string(progress), "granting") {
		t.Fatalf("PROGRESS.md mentions a grant:\n%s", progress)
	}
	h1 := maskRunVolatile(r.st.History[len(r.st.History)-1])
	h2 := maskRunVolatile(base.st.History[len(base.st.History)-1])
	if h1 != h2 {
		t.Fatalf("history differs from a run with no extend_iter:\n%q\n%q", h1, h2)
	}
	f1 := maskRunVolatile(frontmatterOf(r.resultBody(status)))
	f2 := maskRunVolatile(frontmatterOf(base.resultBody(baseStatus)))
	if f1 != f2 {
		t.Fatalf("result frontmatter differs from a run with no extend_iter:\n%s\n---\n%s", f1, f2)
	}
}

func TestExtendRespectsUntil(t *testing.T) {
	now := time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		commitIter(t, dir, iter)
		if iter == 3 { // the first extension iteration
			now = now.Add(2 * time.Hour) // past the 06:00 deadline
		}
		return nil
	}
	sb.verify = alwaysGreenVerify
	opts := Options{Until: "06:00"}
	opts.clock = func() time.Time { return now }
	r := newExtendRun(t, "max_iter: 2\nextend_iter: 5\n", opts, sb)

	if status := r.loop(); status != StatusWindowClosed {
		t.Fatalf("status = %v, want WindowClosed", status)
	}
	if r.extended != 1 {
		t.Fatalf("extended = %d, want 1", r.extended)
	}
}

func TestExtendRateLimitedGrantNotCounted(t *testing.T) {
	t.Setenv("NIGHTSHIFT_LIMIT_MAX_WAIT", "1ns")
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 3 {
			return fmt.Errorf("exit status 1")
		}
		commitIter(t, dir, iter)
		return nil
	}
	sb.output = func(iter int) string {
		if iter == 3 {
			return rateLimitLine
		}
		return "AGENT RAW OUTPUT\n"
	}
	sb.verify = alwaysGreenVerify
	r := newExtendRun(t, "max_iter: 2\nextend_iter: 5\n", Options{}, sb)

	status, err := r.finish(r.loop())
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if status != StatusRateLimited {
		t.Fatalf("status = %v, want RateLimited", status)
	}
	if h := r.st.History[len(r.st.History)-1]; strings.Contains(h, " extended=") {
		t.Fatalf("history = %q, want no extended=", h)
	}
	if body := r.resultBody(status); strings.Contains(body, "extended:") {
		t.Fatalf("result note has extended:\n%s", body)
	}
}
