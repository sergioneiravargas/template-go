package log

import (
	"context"
	"log/slog"
)

type Logger struct {
	logger *slog.Logger
}

type Handler = slog.Handler

func NewLogger(
	handler Handler,
) *Logger {
	return &Logger{
		logger: slog.New(handler),
	}
}

func (l *Logger) Debug(msg string, ctx any) {
	l.log(msg, ctx, slog.LevelDebug)
}

func (l *Logger) Info(msg string, ctx any) {
	l.log(msg, ctx, slog.LevelInfo)
}

func (l *Logger) Warn(msg string, ctx any) {
	l.log(msg, ctx, slog.LevelWarn)
}

func (l *Logger) Error(msg string, ctx any) {
	l.log(msg, ctx, slog.LevelError)
}

func (l *Logger) log(msg string, ctx any, lvl slog.Level) {
	l.logger.LogAttrs(
		context.TODO(),
		lvl,
		msg,
		slog.Any("context", ctx),
	)
}
