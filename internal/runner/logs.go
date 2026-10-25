package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/lock"
)

// LogsOptions configures `nightshift logs`.
type LogsOptions struct {
	StateRoot string
	Slug      string // "" = pick
	Follow    bool
	Agent     bool
	Raw       bool
	Iter      int // 0 = unset
	Verify    int // 0 = unset
	N         int // lines to show; 0 = all
	Stdout    io.Writer
	Ctx       context.Context

	Poll   time.Duration                                    // 0 = 500ms
	Sleep  func(ctx context.Context, d time.Duration) error // nil = real timer
	Active func(slug string) bool                           // nil = spec lock held
	Loc    *time.Location
}

func realSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// headerIter reports the iteration named by an agent.log header line.
func headerIter(line string) (int, bool) {
	m := agentHeaderRE.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

// logStream renders one run's log file into display lines.
type logStream struct {
	slug    string
	prefix  string
	path    string
	agent   bool
	raw     bool
	iter    int
	rend    *Renderer
	rawIter int
	f       *os.File
	off     int64
	partial []byte
}

func (o *LogsOptions) newStream(slug string, prefix string) *logStream {
	s := &logStream{slug: slug, prefix: prefix, agent: o.Agent, raw: o.Raw, iter: o.Iter}
	name := "runner.log"
	if o.Agent {
		name = "agent.log"
		s.rend = &Renderer{Roots: []string{filepath.Join(o.StateRoot, slug, "repo")}, Loc: o.Loc}
	}
	s.path = filepath.Join(o.StateRoot, slug, name)
	return s
}

// feed converts whole raw lines into display lines.
func (s *logStream) feed(raw []string) []string {
	var out []string
	for _, l := range raw {
		switch {
		case !s.agent:
			out = append(out, l)
		case s.raw:
			if n, ok := headerIter(l); ok {
				s.rawIter = n
			}
			if s.iter == 0 || s.rawIter == s.iter {
				out = append(out, l)
			}
		default:
			for _, d := range s.rend.Line(l) {
				if s.iter == 0 || s.rend.Iter() == s.iter {
					out = append(out, d)
				}
			}
		}
	}
	return out
}

func splitLines(data []byte) (lines []string, rest []byte) {
	s := string(data)
	i := strings.LastIndexByte(s, '\n')
	if i < 0 {
		return nil, data
	}
	return strings.Split(s[:i], "\n"), data[i+1:]
}

// poll returns new complete lines appended since the last call, reopening
// the file when it was replaced or truncated.
func (s *logStream) poll() []string {
	st, err := os.Stat(s.path)
	if err != nil {
		return nil
	}
	if s.f != nil {
		if fst, err := s.f.Stat(); err != nil || !os.SameFile(st, fst) || st.Size() < s.off {
			s.f.Close()
			s.f = nil
			s.off, s.partial = 0, nil
			if s.agent && !s.raw {
				s.rend = &Renderer{Roots: s.rend.Roots, Loc: s.rend.Loc}
			}
		}
	}
	if s.f == nil {
		f, err := os.Open(s.path)
		if err != nil {
			return nil
		}
		s.f = f
	}
	buf, err := io.ReadAll(s.f)
	if err != nil || len(buf) == 0 {
		return nil
	}
	s.off += int64(len(buf))
	lines, rest := splitLines(append(s.partial, buf...))
	s.partial = append([]byte(nil), rest...)
	return s.feed(lines)
}

func (s *logStream) close() {
	if s.f != nil {
		s.f.Close()
	}
}

func lastN(lines []string, n int) []string {
	if n > 0 && len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

// tail prints the last N display lines of the file and, when following,
// positions the stream just after the last complete line.
func (s *logStream) tail(n int, w io.Writer, follow bool) error {
	data, err := os.ReadFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines, rest := splitLines(data)
	if !follow && len(rest) > 0 {
		lines = append(lines, string(rest))
		rest = nil
	}
	for _, l := range lastN(s.feed(lines), n) {
		fmt.Fprintln(w, s.prefix+l)
	}
	if follow {
		if f, err := os.Open(s.path); err == nil {
			s.f = f
			s.off = int64(len(data))
			if _, err := f.Seek(s.off, io.SeekStart); err != nil {
				return err
			}
			s.partial = append([]byte(nil), rest...)
		}
	}
	return nil
}

func runStatus(stateRoot, slug string) string {
	data, err := os.ReadFile(filepath.Join(stateRoot, slug, "run.json"))
	if err != nil {
		return "unknown"
	}
	var st struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(data, &st) != nil || st.Status == "" {
		return "unknown"
	}
	return st.Status
}

// Logs implements `nightshift logs`.
func Logs(o LogsOptions) error {
	if o.Ctx == nil {
		o.Ctx = context.Background()
	}
	if o.Poll <= 0 {
		o.Poll = 500 * time.Millisecond
	}
	if o.Sleep == nil {
		o.Sleep = realSleep
	}
	if o.Active == nil {
		o.Active = func(slug string) bool {
			_, held := lock.Probe(specLockPath(o.StateRoot, slug))
			return held
		}
	}

	var slugs []string
	if o.Slug != "" {
		if _, err := os.Stat(filepath.Join(o.StateRoot, o.Slug, "run.json")); err != nil {
			return fmt.Errorf("no run state for %s", o.Slug)
		}
		slugs = []string{o.Slug}
	} else {
		var err error
		if slugs, err = pickSlugs(&o); err != nil {
			return err
		}
	}

	if o.Verify > 0 {
		p := filepath.Join(o.StateRoot, slugs[0], "attempts", fmt.Sprintf("iter-%d.log", o.Verify))
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("no verify log for iter %d (only red iterations keep one)", o.Verify)
		}
		_, err = o.Stdout.Write(data)
		return err
	}

	multi := len(slugs) > 1
	var streams []*logStream
	for _, slug := range slugs {
		prefix := ""
		if multi {
			prefix = "[" + slug + "] "
		}
		s := o.newStream(slug, prefix)
		if err := s.tail(o.N, o.Stdout, o.Follow); err != nil {
			return err
		}
		streams = append(streams, s)
	}
	if !o.Follow {
		return nil
	}
	defer func() {
		for _, s := range streams {
			s.close()
		}
	}()
	if !multi && !o.Active(slugs[0]) {
		fmt.Fprintln(o.Stdout, "(not running)")
		return nil
	}

	for len(streams) > 0 {
		if err := o.Sleep(o.Ctx, o.Poll); err != nil {
			return nil
		}
		if o.Ctx.Err() != nil {
			return nil
		}
		var live []*logStream
		for _, s := range streams {
			lines := s.poll()
			for _, l := range lines {
				fmt.Fprintln(o.Stdout, s.prefix+l)
			}
			if len(lines) == 0 && !o.Active(s.slug) {
				// The lock is released: one more poll for anything flushed
				// just before it dropped.
				last := s.poll()
				for _, l := range last {
					fmt.Fprintln(o.Stdout, s.prefix+l)
				}
				if len(last) == 0 {
					fmt.Fprintf(o.Stdout, "%s— run ended: %s\n", s.prefix, runStatus(o.StateRoot, s.slug))
					s.close()
					continue
				}
			}
			live = append(live, s)
		}
		streams = live
	}
	return nil
}

// pickSlugs chooses which run(s) to show when no slug was given.
func pickSlugs(o *LogsOptions) ([]string, error) {
	matches, _ := filepath.Glob(filepath.Join(o.StateRoot, "*", "run.json"))
	if len(matches) == 0 {
		return nil, fmt.Errorf("no runs yet")
	}
	type run struct {
		slug string
		mod  time.Time
	}
	var all, active []run
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil {
			continue
		}
		r := run{slug: filepath.Base(filepath.Dir(m)), mod: st.ModTime()}
		all = append(all, r)
		if o.Active(r.slug) {
			active = append(active, r)
		}
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("no runs yet")
	}
	sortRuns := func(rs []run) {
		sort.Slice(rs, func(i, j int) bool {
			if !rs[i].mod.Equal(rs[j].mod) {
				return rs[i].mod.After(rs[j].mod)
			}
			return rs[i].slug < rs[j].slug
		})
	}
	if o.Follow && len(active) > 1 {
		var out []string
		for _, r := range active {
			out = append(out, r.slug)
		}
		sort.Strings(out)
		return out, nil
	}
	pool := all
	if len(active) > 0 {
		pool = active
	}
	sortRuns(pool)
	return []string{pool[0].slug}, nil
}
