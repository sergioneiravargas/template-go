package main

import (
	"context"
	"fmt"
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
		),
		fx.Invoke(setupQueuePool),
		fx.Invoke(configureWorkerLifecycleHooks),
		fx.NopLogger,
	)

	app.Run()
}

func configureWorkerLifecycleHooks(
	lc fx.Lifecycle,
	hub *websocket.Hub,
	pool *queue.Pool,
	db *sql.DB,
	amqpConn *amqpx.ConnectionManager,
) {
	// workCtx is the root context for queue handlers and outbox consumers. It is
	// cancelled on stop AFTER the graceful drain, so in-flight handlers finish
	// under the fx stop deadline instead of being cut short.
	workCtx, cancelWork := context.WithCancel(context.Background())
	workDone := make(chan struct{})

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(workDone)
				pool.Work(workCtx)
			}()
			if enabled, _ := strconv.ParseBool(os.Getenv("APP_PROFILER_ENABLED")); enabled {
				debug.StartPProfServer(":6060")
			}
			return nil
		},
		OnStop: func(ctx context.Context) error {
			// Stop fetching and wait for in-flight handlers, bounded by the
			// fx stop deadline carried in ctx.
			if err := pool.Shutdown(ctx); err != nil {
				return err
			}
			// Stop outbox consumers (and hard-cancel any straggler).
			cancelWork()
			select {
			case <-workDone:
			case <-ctx.Done():
				return fmt.Errorf("timed out waiting for pool work to stop: %w", ctx.Err())
			}
			if err := hub.Close(); err != nil {
				return err
			}
			if err := amqpConn.Close(); err != nil {
				return err
			}
			// Close the DB only after every consumer has stopped so no
			// transaction is cut off mid-flight.
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
