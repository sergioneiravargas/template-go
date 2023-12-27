package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

type Logger struct {
	logger      *slog.Logger
	serviceName string
}

type Handler = slog.Handler

type Level = slog.Level

func NewLogger(
	handler Handler,
	serviceName string,
) *Logger {
	return &Logger{
		logger:      slog.New(handler),
		serviceName: serviceName,
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

func (l *Logger) log(msg string, ctx any, lvl Level) {
	l.logger.LogAttrs(
		context.TODO(),
		lvl,
		msg,
		slog.String(ServiceKey, l.serviceName),
		slog.Any(ContextKey, ctx),
	)
}

const (
	// Default keys
	LevelKey   = "level"
	MessageKey = "message"
	TimeKey    = "timestamp"
	SourceKey  = "source"

	// Custom keys
	ServiceKey = "service"
	ContextKey = "context"
)

func NewHandler(
	w io.Writer,
	env string,
) Handler {
	level, err := EnvironmentLevel(env)
	if err != nil {
		panic(err)
	}

	options := slog.HandlerOptions{
		ReplaceAttr: ReplaceAttrs,
		Level:       level,
	}

	return slog.NewJSONHandler(w, &options)
}

func EnvironmentLevel(env string) (Level, error) {
	switch env {
	case "prod":
		return slog.LevelInfo, nil
	case "dev":
		return slog.LevelDebug, nil
	default:
		return 0, fmt.Errorf("unsupported environment \"%s\"", env)
	}
}

func ReplaceAttrs(groups []string, attr slog.Attr) slog.Attr {
	if attr.Key == slog.LevelKey {
		attr.Key = LevelKey
	} else if attr.Key == slog.MessageKey {
		attr.Key = MessageKey
	} else if attr.Key == slog.TimeKey {
		attr.Key = TimeKey
	} else if attr.Key == slog.SourceKey {
		attr.Key = SourceKey
	}

	return attr
}
