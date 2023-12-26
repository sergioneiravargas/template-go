package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"template-go/pkg/jwt"
	"template-go/pkg/log"
	"template-go/pkg/sql"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
)

func main() {
	app := fx.New(
		fx.Provide(
			newSQLDB,
			newLogger,
			newJWTService,
			newHTTPHandler,
		),
		fx.Invoke(configureLifecycleHooks),
		fx.NopLogger,
	)

	app.Run()
}
func configureLifecycleHooks(
	lc fx.Lifecycle,
	handler http.Handler,
	db *sql.DB,
) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go http.ListenAndServe(":3000", handler)

			return nil
		},
		OnStop: func(context.Context) error {
			if err := db.Close(); err != nil {
				return err
			}

			return nil
		},
	})
}

func newHTTPHandler(
	jwtService *jwt.Service,
) http.Handler {
	r := chi.NewRouter()

	// Middlewares
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(jwt.NewMiddleware(jwtService))

	// Routes
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello, World!"))
	})

	return r
}

func newSQLDB() *sql.DB {
	conf := sql.Conf{
		Host:     os.Getenv("DB_HOST"),
		Port:     os.Getenv("DB_PORT"),
		Name:     os.Getenv("DB_NAME"),
		User:     os.Getenv("DB_USER"),
		Password: os.Getenv("DB_PASSWORD"),
		Driver:   "pgx",
	}

	setupFunc := func(db *sql.DB) error {
		return nil
	}

	return sql.NewDB(
		conf,
		setupFunc,
	)
}

func newLogger() *log.Logger {
	handler := slog.NewJSONHandler(os.Stdout, nil)

	return log.NewLogger(
		handler,
	)
}

func newJWTService() *jwt.Service {
	keySetURL := os.Getenv("JWKS_URL")

	return jwt.NewService(
		keySetURL,
	)
}
