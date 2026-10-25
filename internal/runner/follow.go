package runner

import (
	"io"
	"sync"
	"time"
)

// followPoll is how often `run -f` polls the run's logs. Tests shrink it.
var followPoll = 250 * time.Millisecond

// syncWriter serialises writes so the runner, the --jobs prefix writers and
// the follower can share one stdout.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// startAtEnd positions the stream at the current end of its file, so an
// already-existing log (a resume) isn't replayed.
func (s *logStream) startAtEnd() {
	_ = s.tail(1, io.Discard, true)
	// tail printed at most one line into Discard; the position is what matters.
	s.partial = nil
}

type followed struct {
	slug   string
	prefix string
	runner *logStream
	agent  *logStream
}

// follower tails runner.log and the rendered agent.log of one or more runs
// into a single writer, for `run -f`. It only reads.
type follower struct {
	mu        sync.Mutex
	out       io.Writer
	stateRoot string
	runs      []*followed
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
}

func newFollower(out io.Writer, stateRoot string) *follower {
	f := &follower{out: out, stateRoot: stateRoot, stop: make(chan struct{}), done: make(chan struct{})}
	go f.loop()
	return f
}

// add starts following slug's logs, skipping anything already in the files.
func (f *follower) add(slug, prefix string) {
	o := LogsOptions{StateRoot: f.stateRoot}
	r := o.newStream(slug, prefix)
	o.Agent = true
	a := o.newStream(slug, prefix)
	r.startAtEnd()
	a.startAtEnd()
	f.mu.Lock()
	f.runs = append(f.runs, &followed{slug: slug, prefix: prefix, runner: r, agent: a})
	f.mu.Unlock()
}

func (f *follower) emit(r *followed) {
	for _, l := range r.runner.poll() {
		io.WriteString(f.out, r.prefix+"runner · "+l+"\n")
	}
	for _, l := range r.agent.poll() {
		io.WriteString(f.out, r.prefix+l+"\n")
	}
}

func (f *follower) loop() {
	defer close(f.done)
	t := time.NewTicker(followPoll)
	defer t.Stop()
	for {
		select {
		case <-f.stop:
			return
		case <-t.C:
			f.mu.Lock()
			for _, r := range f.runs {
				f.emit(r)
			}
			f.mu.Unlock()
		}
	}
}

// drain flushes what is left for slug and stops following it.
func (f *follower) drain(slug string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.runs {
		if r.slug == slug {
			f.emit(r)
			r.runner.close()
			r.agent.close()
			f.runs = append(f.runs[:i], f.runs[i+1:]...)
			return
		}
	}
}

// close stops the poller and flushes every remaining run. Safe to repeat.
func (f *follower) close() {
	f.stopOnce.Do(func() {
		close(f.stop)
		<-f.done
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, r := range f.runs {
			f.emit(r)
			r.runner.close()
			r.agent.close()
		}
		f.runs = nil
	})
}
