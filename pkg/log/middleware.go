package log

import (
	"net/http"

	"github.com/go-chi/httplog/v2"
)

func Middleware(
	serviceName string,
	env string,
) func(next http.Handler) http.Handler {
	logLevel, err := EnvironmentLevel(env)
	if err != nil {
		panic(err)
	}

	logger := httplog.NewLogger(serviceName, httplog.Options{
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
		ReplaceAttrsOverride: ReplaceAttrs,
	})

	return httplog.RequestLogger(logger)
}
