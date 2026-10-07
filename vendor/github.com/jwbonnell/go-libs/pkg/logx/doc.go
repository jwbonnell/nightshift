// Package logx provides a structured logger built on top of slog.
//
// New and NewCILogger produce a Logger that writes one JSON object per
// record, including a "service" attribute and a shortened "file" source
// location.
//
// # Text output
//
// NewText produces a Logger that writes one human-readable line per record
// instead, omitting the service name and source location:
//
//	logger := logx.NewText(os.Stderr, slog.LevelInfo, nil)
//	logger.Info(ctx, "iter 2 verify GREEN", "commit", "cb9eef57")
//
// which writes:
//
//	2026-10-05 21:52:05 INFO  iter 2 verify GREEN commit=cb9eef57
package logx
