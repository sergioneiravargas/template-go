package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"

	"template-go/pkg/core/auth"
	"template-go/pkg/log"
	"template-go/pkg/sql"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"go.uber.org/fx"
)

func main() {
	app := fx.New(
		fx.Provide(
			newAppConf,
			newSQLDB,
			newLogger,
			newAuthService,
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

type AppConf struct {
	Name string
	Env  string

	SQLConf  sql.Conf
	AuthConf auth.Conf
}

func newAppConf() AppConf {
	appName := os.Getenv("APP_NAME")
	if appName == "" {
		panic("missing application name")
	}

	appEnv := os.Getenv("APP_ENV")
	supportedEnvs := []string{
		"prod",
		"dev",
	}
	if !slices.Contains(supportedEnvs, appEnv) {
		panic(fmt.Sprintf("unsupported application environment \"%s\"", appEnv))
	}

	sqlConf := sql.Conf{
		Host:     os.Getenv("SQL_HOST"),
		Port:     os.Getenv("SQL_PORT"),
		Name:     os.Getenv("SQL_NAME"),
		User:     os.Getenv("SQL_USER"),
		Password: os.Getenv("SQL_PASSWORD"),
		Driver:   "pgx",
	}

	authConf := auth.Conf{
		KeySetURL: os.Getenv("AUTH_KEYSET_URL"),
		DomainURL: os.Getenv("AUTH_DOMAIN_URL"),
	}

	return AppConf{
		Name:     appName,
		Env:      appEnv,
		SQLConf:  sqlConf,
		AuthConf: authConf,
	}
}

func newHTTPHandler(
	appConf AppConf,
	authService *auth.Service,
	logger *log.Logger,
) http.Handler {
	r := chi.NewRouter()

	// Middlewares
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(log.Middleware(appConf.Name, appConf.Env))

	// API routes
	r.Group(func(r chi.Router) {
		// Middlewares
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins: []string{"*"},
			AllowedMethods: []string{"HEAD", "GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowedHeaders: []string{"Accept", "Authorization", "Content-Type"},
		}))
		r.Use(auth.Middleware(authService))

		// Routes
		r.Route("/api/v1", func(r chi.Router) {
			r.Get("/hello-world", func(w http.ResponseWriter, r *http.Request) {
				logger.Info("HTTP route reached", struct {
					RoutePath string `json:"route_path"`
				}{
					RoutePath: "/hello-world",
				})

				userInfo, found := auth.UserInfoFromRequest(r)
				if !found {
					http.Error(w, "Internal server error", http.StatusInternalServerError)
					return
				}

				body, err := json.Marshal(struct {
					Message string `json:"message"`
				}{
					Message: fmt.Sprintf("Hello, %s!", userInfo.ID),
				})
				if err != nil {
					http.Error(w, "Internal server error", http.StatusInternalServerError)
					return
				}

				w.Write(body)
			})
		})
	})

	// Web routes
	r.Group(func(r chi.Router) {
		// Routes
		r.Get("/hello-world", func(w http.ResponseWriter, r *http.Request) {
			logger.Info("HTTP route reached", struct {
				RoutePath string `json:"route_path"`
			}{
				RoutePath: "/hello-world",
			})

			w.Write([]byte("Hello, World!"))
		})
	})

	return r
}

func newSQLDB(
	appConf AppConf,
) *sql.DB {
	setupFunc := func(db *sql.DB) error {
		return nil
	}

	return sql.NewDB(
		appConf.SQLConf,
		setupFunc,
	)
}

func newLogger(
	appConf AppConf,
) *log.Logger {
	handler := log.NewHandler(os.Stdout, appConf.Env)

	return log.NewLogger(
		appConf.Name,
		handler,
	)
}

func newAuthService(
	appConf AppConf,
) *auth.Service {
	return auth.NewService(
		appConf.AuthConf,
	)
}
