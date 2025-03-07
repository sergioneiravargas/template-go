package main

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/sergioneiravargas/template-go/pkg/core/auth"
	"github.com/sergioneiravargas/template-go/pkg/framework/cache"
	"github.com/sergioneiravargas/template-go/pkg/framework/log"
	"github.com/sergioneiravargas/template-go/pkg/framework/sql"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"go.uber.org/fx"
)

func main() {
	app := fx.New(
		fx.Provide(
			newAppConf,
			newLogger,
			newSQLConf,
			newSQLDB,
			newAuthConf,
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
}

func newHTTPHandler(
	appConf AppConf,
	logger *log.Logger,
	authService *auth.Service,
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
					RoutePath: r.URL.Path,
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
				RoutePath: r.URL.Path,
			})

			w.Write([]byte("Hello, World!"))
		})
	})

	return r
}

func newAppConf() AppConf {
	// App configuration
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

	return AppConf{
		Name: appName,
		Env:  appEnv,
	}
}

func newSQLConf() sql.Conf {
	return sql.Conf{
		Host:     os.Getenv("SQL_HOST"),
		Port:     os.Getenv("SQL_PORT"),
		User:     os.Getenv("SQL_USER"),
		Password: os.Getenv("SQL_PASSWORD"),
		Name:     os.Getenv("SQL_DATABASE"),
	}
}

func newSQLDB(
	conf sql.Conf,
) *sql.DB {
	return sql.NewDB(conf)
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

func newAuthConf() auth.Conf {
	authKeySet, err := auth.FetchKeySet(os.Getenv("AUTH_KEYSET_URL"))
	if err != nil {
		panic(err)
	}
	authUserInfoURL := os.Getenv("AUTH_USERINFO_URL")

	authPrivateKeyBytes, err := os.ReadFile(os.Getenv("AUTH_PRIVATE_KEY_FILE"))
	if err != nil {
		panic(err)
	}
	authPrivateKey, err := auth.LoadPrivateKeyFromPEM(authPrivateKeyBytes)
	if err != nil {
		panic(err)
	}

	authPublicKeyBytes, err := os.ReadFile(os.Getenv("AUTH_PUBLIC_KEY_FILE"))
	if err != nil {
		panic(err)
	}
	authPublicKey, err := auth.LoadPublicKeyFromPEM(authPublicKeyBytes)
	if err != nil {
		panic(err)
	}

	return auth.Conf{
		KeySet:      authKeySet,
		UserInfoURL: authUserInfoURL,
		PEMCertificate: struct {
			Private *rsa.PrivateKey
			Public  *rsa.PublicKey
		}{
			Private: authPrivateKey,
			Public:  authPublicKey,
		},
	}
}

func newAuthService(
	conf auth.Conf,
) *auth.Service {
	userInfoCache := cache.New[string, *auth.UserInfo](
		cache.WithTTL[string, *auth.UserInfo](10*time.Minute),
		cache.WithCleanupInterval[string, *auth.UserInfo](30*time.Second),
	)

	return auth.NewService(
		conf,
		auth.ServiceWithUserInfoCache(userInfoCache),
	)
}
