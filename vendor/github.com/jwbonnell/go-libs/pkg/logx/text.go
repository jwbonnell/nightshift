package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

var _ slog.Handler = (*textHandler)(nil)

var msgEscaper = strings.NewReplacer("\n", `\n`, "\r", `\r`)

// textHandler is a slog.Handler that writes one human-readable line per
// record:
//
//	2006-01-02 15:04:05 LEVEL message key=value ...
//
// It omits the service name and source location that the JSON handler
// built by newLogger includes.
type textHandler struct {
	w        io.Writer
	mu       *sync.Mutex
	minLevel slog.Level
	pre      []byte
	prefix   string
}

// newTextHandler constructs a textHandler writing to w, emitting records at
// or above minLevel.
func newTextHandler(w io.Writer, minLevel slog.Level) *textHandler {
	return &textHandler{
		w:        w,
		mu:       &sync.Mutex{},
		minLevel: minLevel,
	}
}

func (h *textHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minLevel
}

func (h *textHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	pre := make([]byte, len(h.pre))
	copy(pre, h.pre)
	for _, a := range attrs {
		pre = appendAttr(pre, h.prefix, a)
	}

	return &textHandler{
		w:        h.w,
		mu:       h.mu,
		minLevel: h.minLevel,
		pre:      pre,
		prefix:   h.prefix,
	}
}

func (h *textHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	pre := make([]byte, len(h.pre))
	copy(pre, h.pre)

	return &textHandler{
		w:        h.w,
		mu:       h.mu,
		minLevel: h.minLevel,
		pre:      pre,
		prefix:   h.prefix + name + ".",
	}
}

func (h *textHandler) Handle(_ context.Context, r slog.Record) error {
	var buf []byte

	if !r.Time.IsZero() {
		buf = append(buf, r.Time.Local().Format("2006-01-02 15:04:05")...)
		buf = append(buf, ' ')
	}

	buf = append(buf, fmt.Sprintf("%-5s ", r.Level.String())...)

	buf = append(buf, msgEscaper.Replace(r.Message)...)

	buf = append(buf, h.pre...)

	r.Attrs(func(a slog.Attr) bool {
		buf = appendAttr(buf, h.prefix, a)
		return true
	})

	buf = append(buf, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf)
	return err
}

// appendAttr renders a into buf as " key=value", flattening groups into
// dotted keys under prefix, and returns the extended buffer.
func appendAttr(buf []byte, prefix string, a slog.Attr) []byte {
	a.Value = a.Value.Resolve()

	if a.Equal(slog.Attr{}) {
		return buf
	}

	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		if len(attrs) == 0 {
			return buf
		}

		groupPrefix := prefix
		if a.Key != "" {
			groupPrefix = prefix + a.Key + "."
		}

		for _, ga := range attrs {
			buf = appendAttr(buf, groupPrefix, ga)
		}

		return buf
	}

	buf = append(buf, ' ')
	buf = append(buf, quoteIfNeeded(prefix+a.Key)...)
	buf = append(buf, '=')
	buf = append(buf, quoteIfNeeded(formatValue(a.Value))...)

	return buf
}

// formatValue renders a slog.Value as text.
func formatValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindAny:
		return fmt.Sprint(v.Any())
	default:
		return v.String()
	}
}

// quoteIfNeeded wraps s in strconv.Quote if it is empty or contains a
// space, '=', '"', or any non-printable rune; otherwise it returns s
// unchanged.
func quoteIfNeeded(s string) string {
	if s == "" || strings.ContainsAny(s, ` ="`) {
		return strconv.Quote(s)
	}

	for _, r := range s {
		if !unicode.IsPrint(r) {
			return strconv.Quote(s)
		}
	}

	return s
}

// NewText constructs a Logger that writes one plain-text line per record:
//
//	2006-01-02 15:04:05 LEVEL message key=value ...
//
// It omits the service name and source location that New includes.
func NewText(w io.Writer, minLevel Level, traceIDFn TraceIDFn) *Logger {
	return newLoggerWithHandler(newTextHandler(w, minLevel), w == io.Discard, traceIDFn)
}
