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
	"github.com/sergioneiravargas/template-go/internal/platform/mailer"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"
	"github.com/sergioneiravargas/template-go/internal/platform/sql"
	"github.com/sergioneiravargas/template-go/internal/platform/websocket"

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
			newAMQPConn,
			newQueuePool,
			newWebsocketHub,
			newMailerConf,
			newMailer,
			newAuthConf,
			auth.NewRepository,
			newAuthService,
			example.NewRepository,
			newExampleService,
			newWebsocketUpgrader,
			newHTTPHandler,
			newHTTPServer,
		),
		fx.Invoke(setupQueuePool),
		fx.Invoke(configureServerLifecycleHooks),
		fx.Invoke(configureBroadcastLifecycleHooks),
		fx.NopLogger,
	)

	app.Run()
}

func configureServerLifecycleHooks(
	lc fx.Lifecycle,
	server *http.Server,
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
			// fx runs OnStop hooks in reverse registration order, so the
			// broadcast hook below has already closed the hub (and its client
			// sockets) by the time this one tears down AMQP and the DB.
			if err := server.Shutdown(ctx); err != nil {
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

func configureBroadcastLifecycleHooks(
	lc fx.Lifecycle,
	hub *websocket.Hub,
	logger *log.Logger,
) {
	// bctx cancels the broadcast consumer on stop; done reports that it exited.
	bctx, cancelBroadcast := context.WithCancel(context.Background())
	done := make(chan struct{})

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				// At most this many broadcasts are fanned out concurrently;
				// the hub also uses it as the broker prefetch.
				const maxBroadcastConcurrency = 4
				for {
					err := hub.ConsumeMessages(bctx, maxBroadcastConcurrency, logger, func(message websocket.Message) {
						if err := hub.BroadcastMessage(message); err != nil {
							logger.Error("Error broadcasting message", log.Context{
								"error": err.Error(),
								"topic": message.Topic,
							})
						}
					})
					if bctx.Err() != nil {
						return
					}
					logger.Error("Broadcast subscription lost, resubscribing", log.Context{
						"error": err.Error(),
					})
					// Back off before resubscribing so a persistent failure
					// (e.g. the AMQP connection is reconnecting) does not spin
					// in a tight loop flooding logs.
					select {
					case <-bctx.Done():
						return
					case <-time.After(time.Second):
					}
				}
			}()

			return nil
		},
		OnStop: func(ctx context.Context) error {
			cancelBroadcast()
			select {
			case <-done:
			case <-ctx.Done():
				return fmt.Errorf("timed out waiting for broadcast consumer to stop: %w", ctx.Err())
			}
			return hub.Close()
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
		Addr:         ":3000",
		Handler:      handler,
		ReadTimeout:  5 * time.Second, // handshake only
		WriteTimeout: 5 * time.Second, // handshake only
		IdleTimeout:  0,               // keep connection alive indefinitely
	}
}

func newHTTPHandler(
	appConf AppConf,
	logger *log.Logger,
	authService *auth.Service,
	exampleService *example.Service,
	upgrader *websocket.Upgrader,
	hub *websocket.Hub,
) http.Handler {
	r := chi.NewRouter()

	// Middlewares
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(log.Middleware(appConf.Name, appConf.Env))

	// Websocket routes
	r.Group(func(r chi.Router) {
		// Middlewares
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins: []string{"*"},
			AllowedMethods: []string{"HEAD", "GET", "OPTIONS"},
			AllowedHeaders: []string{"Accept", "Authorization", "Content-Type"},
		}))
		// Browsers cannot set headers on the handshake: the token travels
		// in the access_token query parameter (see auth.Middleware).
		r.Use(auth.Middleware(authService))

		// Routes
		r.Route("/ws", func(r chi.Router) {
			r.Get("/rooms/{room}", example.NewRoomWebsocketHandler(upgrader, hub, exampleService, logger))
		})
	})

	// Web routes
	r.Group(func(r chi.Router) {
		// Routes
		r.Get("/ws-client", example.WebsocketClientHandler())
	})

	return r
}

func newWebsocketUpgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // Allow all origins (adjust for security)
		},
	}
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
			auth.NewQueue(workerCount, logger, conn),
			example.NewQueue(workerCount, logger, conn),
		},
	)
}

// setupQueuePool attaches the message handlers after construction: handlers
// need the services, and the services are built after the pool.
func setupQueuePool(
	authService *auth.Service,
	exampleService *example.Service,
	pool *queue.Pool,
	logger *log.Logger,
) {
	authQueue := pool.FindQueue(auth.QueueName)
	if authQueue == nil {
		panic("auth queue not found in pool during setup")
	}
	queue.WithMessageHandlers(
		auth.MessageHandlers(authService, logger)...,
	)(authQueue)

	exampleQueue := pool.FindQueue(example.QueueName)
	if exampleQueue == nil {
		panic("example queue not found in pool during setup")
	}
	queue.WithMessageHandlers(
		example.MessageHandlers(exampleService, logger)...,
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

	passwordResetURL := os.Getenv("AUTH_PASSWORD_RESET_URL")
	if passwordResetURL == "" {
		panic("missing auth password reset url")
	}

	maxPasswordHashConcurrency := 0
	if value := os.Getenv("AUTH_PASSWORD_HASH_MAX_CONCURRENCY"); value != "" {
		maxPasswordHashConcurrency, err = strconv.Atoi(value)
		if err != nil {
			panic(err)
		}
		if maxPasswordHashConcurrency < 1 {
			panic("AUTH_PASSWORD_HASH_MAX_CONCURRENCY must be at least 1")
		}
	}

	return auth.Conf{
		PEMCertificate: auth.PEMCertificate{
			Private: authPrivateKey,
			Public:  authPublicKey,
		},
		PasswordResetURL:           passwordResetURL,
		PasswordHashMaxConcurrency: maxPasswordHashConcurrency,
	}
}

func newAuthService(
	conf auth.Conf,
	repository *auth.Repository,
	m mailer.Mailer,
) *auth.Service {
	userInfoCache := cache.New[string, *auth.UserInfo](
		cache.WithTTL[string, *auth.UserInfo](10*time.Minute),
		cache.WithCleanupInterval[string, *auth.UserInfo](30*time.Second),
	)

	return auth.NewService(
		conf,
		repository,
		m,
		auth.ServiceWithUserInfoCache(userInfoCache),
	)
}

func newMailerConf() mailer.Conf {
	region := os.Getenv("MAILER_AWS_REGION")
	if region == "" {
		panic("missing mailer aws region")
	}
	sender := os.Getenv("MAILER_SENDER_ADDRESS")
	if sender == "" {
		panic("missing mailer sender address")
	}

	return mailer.Conf{
		AWSRegion: region,
		Sender:    sender,
	}
}

func newMailer(conf mailer.Conf) mailer.Mailer {
	m, err := mailer.NewSES(context.Background(), conf)
	if err != nil {
		panic(err)
	}
	return m
}
