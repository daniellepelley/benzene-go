// Package logging is the basic request logging/timing middleware ROADMAP.md's "zero new
// dependencies" list describes: one structured log line per pipeline invocation, using only
// the standard library's log/slog. It is deliberately *not* the OpenTelemetry-based
// diagnostics the `diagnostics` module provides - no tracing, no metrics export, no
// dependency - just enough visibility to answer "what ran, how long, and how did it end"
// from stdout before (or without) reaching for full tracing. The three observability
// options compose freely: this middleware, mesh.TraceMiddleware, and diagnostics.Middleware
// each observe independently.
//
// Register it outermost (before healthcheck/mesh/router interception) so it sees every
// invocation, including intercepted ones.
package logging

import (
	"context"
	"log/slog"
	"strings"
	"time"

	benzene "github.com/daniellepelley/benzene-go"
)

// Middleware returns a benzene.Middleware that logs one line per invocation after downstream
// middleware finishes: topic (and version, when versioned), the Benzene status, and the
// duration in milliseconds. Outcomes choose the level: Info for a success status, Warn for a
// non-success status (the errors travel in an "errors" attribute), and Error for a
// pipeline-level Go error (which is propagated untouched - logging observes, never absorbs).
// An invocation that produced no Result at all logs at Warn with an empty status, reporting
// the wiring gap verbatim rather than papering over it - the same posture as the mesh feed.
//
// A nil logger uses slog.Default(), so the zero-config call logging.Middleware(nil) works in
// any application that has (or hasn't) set a default logger.
func Middleware(logger *slog.Logger) benzene.Middleware {
	return func(ctx context.Context, ic *benzene.InvocationContext, next func(context.Context) error) error {
		started := time.Now()
		err := next(ctx)
		elapsedMs := float64(time.Since(started)) / float64(time.Millisecond)

		target := logger
		if target == nil {
			target = slog.Default()
		}

		status := ""
		if ic.Result != nil {
			status = string(ic.Result.ResultStatus())
		}

		attrs := []slog.Attr{
			slog.String("topic", ic.Topic.ID),
			slog.Float64("duration_ms", elapsedMs),
			slog.String("status", status),
		}
		if ic.Topic.Version != "" {
			attrs = append(attrs, slog.String("topic_version", ic.Topic.Version))
		}

		switch {
		case err != nil:
			attrs = append(attrs, slog.String("error", err.Error()))
			target.LogAttrs(ctx, slog.LevelError, "invocation failed", attrs...)
		case status != "" && benzene.Status(status).IsSuccess():
			target.LogAttrs(ctx, slog.LevelInfo, "invocation completed", attrs...)
		default:
			if ic.Result != nil {
				attrs = append(attrs, slog.String("errors", strings.Join(ic.Result.ResultErrors(), ", ")))
			}
			target.LogAttrs(ctx, slog.LevelWarn, "invocation completed", attrs...)
		}

		return err
	}
}
