package utils

import (
	"context"
	"log"
	"log/slog"
	"os"
	"strings"
)

// The production log this work came from had 1,411 lines, of which exactly 2
// carried a timestamp — and none carried a status code, user id or request
// id. That is why an outage went unnoticed for six days. Everything below
// exists to make the next log dump answerable.

type ctxKey string

const (
	requestIDCtxKey ctxKey = "request_id"
	loggerCtxKey    ctxKey = "logger"
)

// InitLogger installs a JSON slog handler as the default logger and routes
// the standard `log` package through it too, so third-party libraries and
// leftover log.Printf calls end up in the same structured stream.
func InitLogger(level, env, version string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	logger := slog.New(h).With("env", env, "version", version)
	slog.SetDefault(logger)

	// Anything still using the `log` package (including log.Fatalf on a
	// failed boot) becomes a structured line rather than a bare string.
	log.SetFlags(0)
	log.SetOutput(slogWriter{})
}

type slogWriter struct{}

func (slogWriter) Write(p []byte) (int, error) {
	slog.Info(strings.TrimRight(string(p), "\n"), "src", "stdlog")
	return len(p), nil
}

// WithRequestID returns a context carrying the request id and a logger
// pre-bound to it.
func WithRequestID(ctx context.Context, id string) context.Context {
	ctx = context.WithValue(ctx, requestIDCtxKey, id)
	return context.WithValue(ctx, loggerCtxKey, slog.Default().With("request_id", id))
}

// WithLogger replaces the context's logger (used to add user_id once auth has
// run).
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey, l)
}

// RequestIDFromContext returns the request id, or "" when the request didn't
// pass through RequestIDMiddleware (e.g. a background goroutine).
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDCtxKey).(string)
	return id
}

// L returns the request-scoped logger, falling back to the default logger so
// call sites never have to nil-check.
func L(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerCtxKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
