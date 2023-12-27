package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"

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
			newAppConf,
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

type AppConf struct {
	ServiceName string
	ServiceEnv  string

	SQLConf sql.Conf
	JWTConf jwt.Conf
}

func newAppConf() AppConf {
	serviceName := os.Getenv("SERVICE_NAME")
	if serviceName == "" {
		panic("missing service name")
	}

	serviceEnv := os.Getenv("SERVICE_ENV")
	supportedEnvs := []string{
		"prod",
		"dev",
	}
	if !slices.Contains(supportedEnvs, serviceEnv) {
		panic(fmt.Sprintf("unsupported service environment \"%s\"", serviceEnv))
	}

	sqlConf := sql.Conf{
		Host:     os.Getenv("SQL_HOST"),
		Port:     os.Getenv("SQL_PORT"),
		Name:     os.Getenv("SQL_NAME"),
		User:     os.Getenv("SQL_USER"),
		Password: os.Getenv("SQL_PASSWORD"),
		Driver:   "pgx",
	}

	jwtConf := jwt.Conf{
		KeySetURL: os.Getenv("JWT_KEYSET_URL"),
	}

	return AppConf{
		ServiceName: serviceName,
		ServiceEnv:  serviceEnv,
		SQLConf:     sqlConf,
		JWTConf:     jwtConf,
	}
}

func newHTTPHandler(
	appConf AppConf,
	jwtService *jwt.Service,
	logger *log.Logger,
) http.Handler {
	r := chi.NewRouter()

	// Middlewares
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(log.Middleware(appConf.ServiceName, appConf.ServiceEnv))
	r.Use(jwt.Middleware(jwtService))

	// Routes
	r.Get("/hello-world", func(w http.ResponseWriter, r *http.Request) {
		logger.Info("HTTP route reached", struct {
			RoutePath string `json:"routePath"`
		}{
			RoutePath: "/hello-world",
		})

		w.Write([]byte("Hello, World!"))
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
	handler := log.NewHandler(os.Stdout, appConf.ServiceEnv)

	return log.NewLogger(
		handler,
		appConf.ServiceName,
	)
}

func newJWTService(
	appConf AppConf,
) *jwt.Service {
	return jwt.NewService(
		appConf.JWTConf,
	)
}
