package runner

// tailBuffer is an io.Writer that keeps only the last max bytes written to
// it, so a long-running agent's output can be parsed for its final result
// line without holding the whole stream in memory.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
