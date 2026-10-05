package logx

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type Level = slog.Level

// TraceIDFn represents a function that can return the trace id from
// the specified context.
type TraceIDFn func(ctx context.Context) string

// CITraceID is the static trace ID used by NewCILogger.
const CITraceID = "ci-trace-id"

type Logger struct {
	discard   bool
	handler   slog.Handler
	traceIDFn TraceIDFn
}

// Debug logs at LevelDebug with the given context.
func (l *Logger) Debug(ctx context.Context, msg string, args ...any) {
	l.write(ctx, slog.LevelDebug, 3, msg, args...)
}

// Debugc logs at LevelDebug, skipping callerSkip additional frames beyond
// the default. Pass 0 to get the same source location as Debug; pass 1 if
// Debugc is called from a one-level wrapper.
func (l *Logger) Debugc(ctx context.Context, callerSkip int, msg string, args ...any) {
	l.write(ctx, slog.LevelDebug, 3+callerSkip, msg, args...)
}

// Info logs at LevelInfo with the given context.
func (l *Logger) Info(ctx context.Context, msg string, args ...any) {
	l.write(ctx, slog.LevelInfo, 3, msg, args...)
}

// Infoc logs at LevelInfo, skipping callerSkip additional frames beyond
// the default. Pass 0 to get the same source location as Info; pass 1 if
// Infoc is called from a one-level wrapper.
func (l *Logger) Infoc(ctx context.Context, callerSkip int, msg string, args ...any) {
	l.write(ctx, slog.LevelInfo, 3+callerSkip, msg, args...)
}

// Warn logs at LevelWarn with the given context.
func (l *Logger) Warn(ctx context.Context, msg string, args ...any) {
	l.write(ctx, slog.LevelWarn, 3, msg, args...)
}

// Warnc logs at LevelWarn, skipping callerSkip additional frames beyond
// the default. Pass 0 to get the same source location as Warn; pass 1 if
// Warnc is called from a one-level wrapper.
func (l *Logger) Warnc(ctx context.Context, callerSkip int, msg string, args ...any) {
	l.write(ctx, slog.LevelWarn, 3+callerSkip, msg, args...)
}

// Error logs at LevelError with the given context.
func (l *Logger) Error(ctx context.Context, msg string, args ...any) {
	l.write(ctx, slog.LevelError, 3, msg, args...)
}

// Errorc logs at LevelError, skipping callerSkip additional frames beyond
// the default. Pass 0 to get the same source location as Error; pass 1 if
// Errorc is called from a one-level wrapper.
func (l *Logger) Errorc(ctx context.Context, callerSkip int, msg string, args ...any) {
	l.write(ctx, slog.LevelError, 3+callerSkip, msg, args...)
}

func (l *Logger) write(ctx context.Context, level Level, caller int, msg string, args ...any) {
	// Short-circuit before any allocation when writing to io.Discard.
	if l.discard {
		return
	}

	if !l.handler.Enabled(ctx, level) {
		return
	}

	var pcs [1]uintptr
	runtime.Callers(caller, pcs[:])

	r := slog.NewRecord(time.Now(), level, msg, pcs[0])

	if l.traceIDFn != nil {
		args = append(args, "trace_id", l.traceIDFn(ctx))
	}
	r.Add(args...)

	l.handler.Handle(ctx, r)
}

// New constructs a new Logger for application use.
func New(w io.Writer, minLevel Level, serviceName string, traceIDFn TraceIDFn) *Logger {
	return newLogger(w, minLevel, serviceName, traceIDFn)
}

func NewCILogger(serviceName string) *Logger {
	return newLogger(os.Stdout, slog.LevelDebug, serviceName, func(ctx context.Context) string {
		return CITraceID
	})
}

func NewStdLogger(logger *Logger, level Level) *log.Logger {
	return slog.NewLogLogger(logger.handler, slog.Level(level))
}

func newLogger(w io.Writer, minLevel Level, serviceName string, traceIDFn TraceIDFn) *Logger {
	// Convert the file name to just the name.ext when this key/value will
	// be logged.
	f := func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.SourceKey {
			if source, ok := a.Value.Any().(*slog.Source); ok {
				v := fmt.Sprintf("%s:%d", filepath.Base(source.File), source.Line)
				return slog.Attr{Key: "file", Value: slog.StringValue(v)}
			}
		}

		return a
	}

	// Construct the slog JSON handler for use.
	handler := slog.Handler(slog.NewJSONHandler(w, &slog.HandlerOptions{AddSource: true, Level: minLevel, ReplaceAttr: f}))

	// Attributes to add to every log entry.
	attrs := []slog.Attr{
		{Key: "service", Value: slog.StringValue(serviceName)},
	}

	// Add those attributes to the handler.
	handler = handler.WithAttrs(attrs)

	return &Logger{
		discard:   w == io.Discard,
		handler:   handler,
		traceIDFn: traceIDFn,
	}
}
