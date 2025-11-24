package main

import (
	"context"
	"crypto/rsa"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/sergioneiravargas/template-go/pkg/core/auth"
	"github.com/sergioneiravargas/template-go/pkg/core/example"
	"github.com/sergioneiravargas/template-go/pkg/framework/cache"
	"github.com/sergioneiravargas/template-go/pkg/framework/log"
	"github.com/sergioneiravargas/template-go/pkg/framework/queue"
	"github.com/sergioneiravargas/template-go/pkg/framework/sql"

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
		),
		fx.Invoke(configureLifecycleHooks),
		fx.NopLogger,
	)

	app.Run()
}

func configureLifecycleHooks(
	lc fx.Lifecycle,
	db *sql.DB,
	amqpConn *amqp.Connection,
	pool *queue.Pool,
) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go pool.Work()
			return nil
		},
		OnStop: func(context.Context) error {
			if err := pool.Shutdown(); err != nil {
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
