package log

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/httplog/v2"
)

func Middleware(
	producerName string,
	env string,
) func(next http.Handler) http.Handler {
	logLevel, err := EnvironmentLevel(env)
	if err != nil {
		panic(err)
	}

	logger := httplog.NewLogger(producerName, httplog.Options{
		JSON:             true,
		LogLevel:         logLevel,
		Concise:          true,
		RequestHeaders:   true,
		MessageFieldName: MessageKey,
		TimeFieldName:    TimeKey,
		LevelFieldName:   LevelKey,
		Tags: map[string]string{
			"env": env,
		},
		ReplaceAttrsOverride: func(groups []string, attr slog.Attr) slog.Attr {
			sourceKey := attr.Key
			attr = ReplaceAttrs(groups, attr)
			if sourceKey != attr.Key {
				return attr
			}

			if attr.Key == "service" {
				attr.Key = ProducerKey
			}

			return attr
		},
	})

	return httplog.RequestLogger(logger)
}
