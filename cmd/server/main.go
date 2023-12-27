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

	return AppConf{
		ServiceName: serviceName,
		ServiceEnv:  serviceEnv,
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

func newLogger(
	appConf AppConf,
) *log.Logger {
	handler := log.NewHandler(os.Stdout, appConf.ServiceEnv)

	return log.NewLogger(
		handler,
		appConf.ServiceName,
	)
}

func newJWTService() *jwt.Service {
	keySetURL := os.Getenv("JWKS_URL")

	return jwt.NewService(
		keySetURL,
	)
}
