package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/sergioneiravargas/template-go/internal/auth"
	"github.com/sergioneiravargas/template-go/internal/example"
	"github.com/sergioneiravargas/template-go/internal/platform/amqpx"
	"github.com/sergioneiravargas/template-go/internal/platform/cache"
	"github.com/sergioneiravargas/template-go/internal/platform/debug"
	"github.com/sergioneiravargas/template-go/internal/platform/log"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"
	"github.com/sergioneiravargas/template-go/internal/platform/sql"
	"github.com/sergioneiravargas/template-go/internal/platform/websocket"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"
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
			newWebsocketHub,
			newAuthConf,
			auth.NewRepository,
			newAuthService,
			example.NewRepository,
			newExampleService,
			newHTTPHandler,
			newHTTPServer,
		),
		fx.Invoke(setupQueuePool),
		fx.Invoke(configureLifecycleHooks),
		fx.NopLogger,
	)

	app.Run()
}

func configureLifecycleHooks(
	lc fx.Lifecycle,
	server *http.Server,
	hub *websocket.Hub,
	pool *queue.Pool,
	db *sql.DB,
	amqpConn *amqpx.ConnectionManager,
) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					panic(err)
				}
			}()
			if enabled, _ := strconv.ParseBool(os.Getenv("APP_PROFILER_ENABLED")); enabled {
				debug.StartPProfServer(":6060")
			}
			return nil
		},
		OnStop: func(ctx context.Context) error {
			// ctx carries the fx stop deadline; every teardown step honors it.
			// Order: stop accepting traffic, drop sockets, drain jobs, then
			// tear down the transports they depend on.
			if err := server.Shutdown(ctx); err != nil {
				return err
			}
			if err := hub.Close(); err != nil {
				return err
			}
			if err := pool.Shutdown(ctx); err != nil {
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
	authService *auth.Service,
	exampleService *example.Service,
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

		// Routes
		r.Route("/api/v1", func(r chi.Router) {
			// Public routes
			r.Group(func(r chi.Router) {
				r.Use(httprate.LimitByIP(10, time.Minute))

				r.Post("/auth/register", auth.RegisterAPIHandler(logger, authService))
				r.Post("/auth/login", auth.LoginAPIHandler(logger, authService))
				r.Post("/auth/refresh", auth.RefreshAPIHandler(logger, authService))
				r.Post("/auth/logout", auth.LogoutAPIHandler(logger, authService))
			})

			// Private routes
			r.Group(func(r chi.Router) {
				r.Use(auth.Middleware(authService))

				r.Get("/hello-world", example.HelloWorldAPIHandler(logger))
				r.Post("/messages", example.CreateMessageAPIHandler(logger, exampleService))
				r.Get("/messages/{id}", example.GetMessageAPIHandler(logger, exampleService))
				r.Post("/rooms/{room}/broadcast", example.BroadcastRoomAPIHandler(logger, exampleService))
			})
		})
	})

	// Web routes
	r.Group(func(r chi.Router) {
		// Routes
		r.Get("/auth-client", auth.WebClientHandler())
		r.Get("/hello-world", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("Hello, World!"))
		})
	})

	return r
}

func newSQLConf() sql.Conf {
	var maxPoolConn int
	var err error
	if os.Getenv("SQL_MAX_POOL_CONN") != "" {
		maxPoolConn, err = strconv.Atoi(os.Getenv("SQL_MAX_POOL_CONN"))
		if err != nil {
			panic(err)
		}
	}
	return sql.Conf{
		Host:        os.Getenv("SQL_HOST"),
		Port:        os.Getenv("SQL_PORT"),
		Name:        os.Getenv("SQL_DATABASE"),
		User:        os.Getenv("SQL_USER"),
		Password:    os.Getenv("SQL_PASSWORD"),
		MaxPoolConn: maxPoolConn,
	}
}

func newSQLDB(
	conf sql.Conf,
) *sql.DB {
	return sql.NewDB(conf)
}

func newAMQPConn(sd fx.Shutdowner, logger *log.Logger) *amqpx.ConnectionManager {
	cfg := amqpx.Config{
		URL: fmt.Sprintf("amqp://%s:%s@%s:%s/%s", os.Getenv("AMQP_USER"), os.Getenv("AMQP_PASSWORD"), os.Getenv("AMQP_HOST"), os.Getenv("AMQP_PORT"), os.Getenv("APP_NAME")),
	}
	manager, err := amqpx.New(cfg, amqpx.NewAMQPDialer(cfg), logger, amqpx.WithOnGiveUp(func() {
		logger.Error("AMQP connection unrecoverable; shutting down for restart", nil)
		_ = sd.Shutdown(fx.ExitCode(1))
	}))
	if err != nil {
		panic(err)
	}
	return manager
}

func newQueuePool(
	db *sql.DB,
	conn *amqpx.ConnectionManager,
	logger *log.Logger,
) *queue.Pool {
	const workerCount = 4
	return queue.NewPool(
		db,
		logger,
		[]*queue.Queue{
			example.NewQueue(workerCount, logger, conn),
		},
	)
}

// setupQueuePool attaches the message handlers after construction: handlers
// need the service, and the service is built after the pool.
func setupQueuePool(
	service *example.Service,
	pool *queue.Pool,
	logger *log.Logger,
) {
	exampleQueue := pool.FindQueue(example.QueueName)
	if exampleQueue == nil {
		panic("example queue not found in pool during setup")
	}
	queue.WithMessageHandlers(
		example.MessageHandlers(service, logger)...,
	)(exampleQueue)
}

func newWebsocketHub(
	conn *amqpx.ConnectionManager,
) *websocket.Hub {
	return websocket.NewHub(conn)
}

func newExampleService(
	repository *example.Repository,
	hub *websocket.Hub,
	logger *log.Logger,
) *example.Service {
	return example.NewService(repository, hub, logger)
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
		PEMCertificate: auth.PEMCertificate{
			Private: authPrivateKey,
			Public:  authPublicKey,
		},
	}
}

func newAuthService(
	conf auth.Conf,
	repository *auth.Repository,
) *auth.Service {
	userInfoCache := cache.New[string, *auth.UserInfo](
		cache.WithTTL[string, *auth.UserInfo](10*time.Minute),
		cache.WithCleanupInterval[string, *auth.UserInfo](30*time.Second),
	)

	return auth.NewService(
		conf,
		repository,
		auth.ServiceWithUserInfoCache(userInfoCache),
	)
}
