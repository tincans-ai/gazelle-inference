package logutil

import (
	"context"
	"log/slog"
)

type ctxLogger struct{}

// ContextWithLogger adds logger to context
func ContextWithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxLogger{}, l)
}

// LoggerFromContext returns logger from context
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxLogger{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
