package runner

import (
	"bytes"
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

	open func(path string) (logFile, error) // nil = os.Open; tests count bytes read
}

// logFile is the part of *os.File the log streams use.
type logFile interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
	Stat() (os.FileInfo, error)
}

func osOpen(path string) (logFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

const (
	tailChunk      = 64 << 10 // read-back step for tail
	headerScanSpan = 1 << 20  // how far startAtEnd looks back for a header
)

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
	open    func(string) (logFile, error)
	f       logFile
	off     int64
	partial []byte
	// skipLine drops input up to the next newline: startAtEnd landed in
	// the middle of a line longer than its read-back.
	skipLine bool
}

func (o *LogsOptions) newStream(slug string, prefix string) *logStream {
	s := &logStream{slug: slug, prefix: prefix, agent: o.Agent, raw: o.Raw, iter: o.Iter, open: o.open}
	if s.open == nil {
		s.open = osOpen
	}
	name := "runner.log"
	if o.Agent {
		name = "agent.log"
		s.rend = &Renderer{Roots: []string{CloneDir(o.StateRoot, slug)}, Loc: o.Loc}
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
			s.off, s.partial, s.skipLine = 0, nil, false
			s.resetIter()
		}
	}
	if s.f == nil {
		f, err := s.open(s.path)
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
	data := append(s.partial, buf...)
	if s.skipLine {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			s.partial = nil
			return nil
		}
		data, s.skipLine = data[i+1:], false
	}
	lines, rest := splitLines(data)
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

func readRange(f logFile, off, n int64) ([]byte, error) {
	b := make([]byte, n)
	got, err := f.ReadAt(b, off)
	if int64(got) == n {
		return b, nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return nil, err
}

// lastHeader returns the last agent.log header line among whole lines.
func lastHeader(region []byte) (string, bool) {
	lines := bytes.Split(bytes.TrimRight(region, "\n"), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if bytes.HasPrefix(lines[i], []byte("=== iter ")) {
			if _, ok := headerIter(string(lines[i])); ok {
				return string(lines[i]), true
			}
		}
	}
	return "", false
}

// seed primes the stream's iteration state from a header line without
// producing output.
func (s *logStream) seed(header string) {
	if s.raw {
		s.rawIter, _ = headerIter(header)
		return
	}
	s.rend.Line(header)
}

func (s *logStream) resetIter() {
	s.rawIter = 0
	if s.agent && !s.raw {
		s.rend = &Renderer{Roots: s.rend.Roots, Loc: s.rend.Loc}
	}
}

// headerBefore finds the nearest header line among the whole lines ending at
// pos (which must be a line boundary), reading backwards in chunks.
func (s *logStream) headerBefore(f logFile, pos int64) (string, bool, error) {
	var frag []byte
	for end := pos; end > 0; {
		start := max(0, end-tailChunk)
		c, err := readRange(f, start, end-start)
		if err != nil {
			return "", false, err
		}
		buf := append(c, frag...)
		region := buf
		if start > 0 {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				frag = buf
				end = start
				continue
			}
			frag = buf[:i]
			region = buf[i+1:]
		}
		if h, ok := lastHeader(region); ok {
			return h, true, nil
		}
		end = start
	}
	return "", false, nil
}

// countDisplay counts the display lines raw lines produce, without touching
// the stream's own state.
func (s *logStream) countDisplay(cnt *Renderer, lines [][]byte) int {
	n := 0
	for _, l := range lines {
		if cnt != nil {
			n += len(cnt.Line(string(l)))
		} else {
			n++
		}
	}
	return n
}

// tailBack renders the last n display lines of the first size bytes of f by
// reading backwards from the end. It returns them plus the unterminated
// bytes after the last newline.
func (s *logStream) tailBack(f logFile, size int64, n int, follow bool) ([]string, []byte, error) {
	var cnt *Renderer
	if s.agent && !s.raw {
		cnt = &Renderer{Roots: s.rend.Roots, Loc: s.rend.Loc}
	}
	var (
		chunks   [][]byte // newest first
		frag     []byte   // incomplete first line of what has been read
		rest     []byte
		haveRest bool
		count    int
		start    = size
	)
	for start > 0 {
		end := start
		start = max(0, end-tailChunk)
		c, err := readRange(f, start, end-start)
		if err != nil {
			return nil, nil, err
		}
		chunks = append(chunks, c)
		t := append(c[:len(c):len(c)], frag...)
		if !haveRest {
			i := bytes.LastIndexByte(t, '\n')
			if i < 0 {
				frag = t
				continue
			}
			rest = append([]byte(nil), t[i+1:]...)
			t = t[:i+1]
			haveRest = true
			if !follow && len(rest) > 0 {
				count += s.countDisplay(cnt, [][]byte{rest})
			}
		}
		t = bytes.TrimSuffix(t, []byte("\n"))
		parts := bytes.Split(t, []byte("\n"))
		if start > 0 {
			frag = parts[0]
			parts = parts[1:]
		}
		count += s.countDisplay(cnt, parts)
		if count >= n {
			break
		}
	}
	if !haveRest {
		rest = frag // no newline anywhere: the whole file is one partial line
		frag = nil
		chunks = nil
	}
	var data []byte
	for i := len(chunks) - 1; i >= 0; i-- {
		data = append(data, chunks[i]...)
	}
	regionStart := start
	if start > 0 {
		i := bytes.IndexByte(data, '\n')
		data = data[i+1:]
		regionStart += int64(i) + 1
	}
	data = data[:len(data)-len(rest)]
	var lines []string
	if len(data) > 0 {
		lines = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	if !follow && len(rest) > 0 {
		lines = append(lines, string(rest))
	}
	s.resetIter()
	if s.agent && regionStart > 0 {
		first := -1
		for i, l := range lines {
			if _, ok := headerIter(l); ok {
				first = i
				break
			}
		}
		if first > 0 {
			// Lines before the first header only matter if the ones after
			// it don't already fill n.
			if out := s.feed(lines[first:]); len(out) >= n {
				return out, rest, nil
			}
			s.resetIter()
		}
		if first != 0 {
			h, ok, err := s.headerBefore(f, regionStart)
			if err != nil {
				return nil, nil, err
			}
			if ok {
				s.seed(h)
			}
		}
	}
	return s.feed(lines), rest, nil
}

// tail prints the last N display lines of the file and, when following,
// positions the stream just after the last complete line. The file is
// opened once and that handle is kept for following.
func (s *logStream) tail(n int, w io.Writer, follow bool) error {
	f, err := s.open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	keep := false
	defer func() {
		if !keep {
			f.Close()
		}
	}()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	var out []string
	var rest []byte
	if n == 0 || s.iter != 0 || size == 0 {
		data, err := readRange(f, 0, size)
		if err != nil {
			return err
		}
		lines, r := splitLines(data)
		rest = r
		if !follow && len(rest) > 0 {
			lines = append(lines, string(rest))
			rest = nil
		}
		out = s.feed(lines)
	} else if out, rest, err = s.tailBack(f, size, n, follow); err != nil {
		return err
	}
	for _, l := range lastN(out, n) {
		fmt.Fprintln(w, s.prefix+l)
	}
	if follow {
		if _, err := f.Seek(size, io.SeekStart); err != nil {
			return err
		}
		s.f, s.off, keep = f, size, true
		s.partial = append([]byte(nil), rest...)
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
