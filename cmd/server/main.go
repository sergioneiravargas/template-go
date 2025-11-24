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
	"github.com/sergioneiravargas/template-go/pkg/core/example"
	"github.com/sergioneiravargas/template-go/pkg/framework/cache"
	"github.com/sergioneiravargas/template-go/pkg/framework/log"
	"github.com/sergioneiravargas/template-go/pkg/framework/queue"
	"github.com/sergioneiravargas/template-go/pkg/framework/sql"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/fx"
)

func main() {
	app := fx.New(
		fx.Provide(
			newAppConf,
			newLogger,
			newSQLConf,
			newSQLDB,
			newAMQPConn,
			newQueuePool,
			newAuthConf,
			newAuthService,
			newHTTPHandler,
			newHTTPServer,
		),
		fx.Invoke(configureLifecycleHooks),
		fx.NopLogger,
	)

	app.Run()
}

func configureLifecycleHooks(
	lc fx.Lifecycle,
	server *http.Server,
	db *sql.DB,
	amqpConn *amqp.Connection,
) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				if err := server.ListenAndServe(); err != nil {
					panic(err)
				}
			}()
			return nil
		},
		OnStop: func(context.Context) error {
			if err := server.Shutdown(context.TODO()); err != nil {
				return err
			}
			if err := amqpConn.Close(); err != nil {
				return err
			}
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

func newHTTPServer(
	handler http.Handler,
) *http.Server {
	return &http.Server{
		Addr:              ":3000",
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func newHTTPHandler(
	appConf AppConf,
	logger *log.Logger,
	db *sql.DB,
	queuePool *queue.Pool,
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
			w.Write([]byte("Hello, World!"))
		})
		r.Post("/queue-job", func(w http.ResponseWriter, r *http.Request) {
			var payload struct {
				Message string `json:"message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				logger.Error("Failed to decode request payload", map[string]any{
					"error": err.Error(),
				})
				http.Error(w, "Bad request", http.StatusBadRequest)
				return
			}

			input := example.CreateLogInput{
				Message: payload.Message,
			}
			if err := input.Validate(); err != nil {
				http.Error(w, "Bad request: "+err.Error(), http.StatusBadRequest)
				return
			}

			log, err := example.CreateLog(db, input)
			if err != nil {
				logger.Error("Failed to create log", map[string]any{
					"error": err.Error(),
				})
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}

			resp, err := json.Marshal(log)
			if err != nil {
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			w.Write(resp)
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

func newAMQPConn() *amqp.Connection {
	amqpURL := fmt.Sprintf("amqp://%s:%s@%s:%s/", os.Getenv("AMQP_USER"), os.Getenv("AMQP_PASSWORD"), os.Getenv("AMQP_HOST"), os.Getenv("AMQP_PORT"))
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		panic(err)
	}
	return conn
}

func newQueuePool(
	db *sql.DB,
	conn *amqp.Connection,
	logger *log.Logger,
) *queue.Pool {
	amqpURL := fmt.Sprintf("amqp://%s:%s@%s:%s/", os.Getenv("AMQP_USER"), os.Getenv("AMQP_PASSWORD"), os.Getenv("AMQP_HOST"), os.Getenv("AMQP_PORT"))
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		panic(err)
	}

	queueWorkerCount := 4

	return queue.NewPool(
		db,
		logger,
		[]*queue.Queue{
			example.NewQueue(queueWorkerCount, logger, conn),
		},
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
