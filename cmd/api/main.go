package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/junglegaming/backend-challenge-go/internal/adapters/inbound/httpapi"
	awsadapter "github.com/junglegaming/backend-challenge-go/internal/adapters/outbound/aws"
	pgadapter "github.com/junglegaming/backend-challenge-go/internal/adapters/outbound/postgres"
	redisadapter "github.com/junglegaming/backend-challenge-go/internal/adapters/outbound/redis"
	"github.com/junglegaming/backend-challenge-go/internal/application"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"go.uber.org/fx"
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type uuidGenerator struct{}

func (uuidGenerator) NewID() string { return uuid.NewString() }

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	app := fx.New(
		fx.Provide(func() config.Config { return cfg }),
		fx.Provide(func() *slog.Logger { return logger }),
		fx.Provide(func() application.Clock { return systemClock{} }),
		fx.Provide(func() application.IDGenerator { return uuidGenerator{} }),
		fx.Provide(newPostgres, newRedis, newSQS, newAuthenticator),
		fx.Provide(func(db *pgadapter.DB) application.UnitOfWork { return db }),
		fx.Provide(func(cache *redisadapter.Cache) application.IdempotencyCache { return cache }),
		fx.Provide(application.NewOpenWallet, application.NewGetWallet, application.NewListWalletLedger, application.NewReconcileWallet, application.NewGetWagerTransaction, application.NewGetProviderWagerTransaction),
		fx.Provide(func(uow application.UnitOfWork, ids application.IDGenerator, clock application.Clock, cache application.IdempotencyCache, cfg config.Config) *application.SubmitWager {
			return application.NewSubmitWager(uow, ids, clock, cache, cfg.IdempotencyTTL)
		}),
		fx.Provide(func(open *application.OpenWallet, get *application.GetWallet, list *application.ListWalletLedger, reconcile *application.ReconcileWallet, submit *application.SubmitWager, getWager *application.GetWagerTransaction, getProvider *application.GetProviderWagerTransaction) httpapi.UseCases {
			return httpapi.UseCases{OpenWallet: open, GetWallet: get, ListLedger: list, Reconcile: reconcile, SubmitWager: submit, GetWager: getWager, GetProviderWager: getProvider}
		}),
		fx.Invoke(startHTTP),
	)
	app.Run()
}

func newPostgres(lc fx.Lifecycle, cfg config.Config) (*pgadapter.DB, error) {
	db, err := pgadapter.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStop: func(context.Context) error { db.Close(); return nil }})
	return db, nil
}
func newRedis(lc fx.Lifecycle, cfg config.Config) (*redisadapter.Cache, error) {
	cache, err := redisadapter.New(context.Background(), cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStop: func(context.Context) error { return cache.Close() }})
	return cache, nil
}
func newSQS(cfg config.Config) *awsadapter.SQS {
	return awsadapter.NewSQS(cfg.AWSRegion, cfg.AWSEndpointURL, cfg.AWSAccessKeyID, cfg.AWSSecretAccessKey)
}
func newAuthenticator(cfg config.Config) (*httpapi.Authenticator, error) {
	return httpapi.NewAuthenticator(context.Background(), cfg.OIDCIssuer, cfg.OIDCClientID, cfg.OIDCProviderClientID, cfg.OIDCProviderID)
}

func startHTTP(lc fx.Lifecycle, cfg config.Config, db *pgadapter.DB, sqs *awsadapter.SQS, auth *httpapi.Authenticator, cases httpapi.UseCases, logger *slog.Logger) error {
	ready := func(ctx context.Context) error {
		if err := db.Ping(ctx); err != nil {
			return err
		}
		return sqs.Check(ctx, cfg.SQSQueueURL)
	}
	handler := httpapi.NewServer(auth, cases, ready, logger)
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	var listener net.Listener
	lc.Append(fx.Hook{OnStart: func(context.Context) error {
		var err error
		listener, err = net.Listen("tcp", cfg.HTTPAddr)
		if err != nil {
			return err
		}
		go func() {
			if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				logger.Error("HTTP server stopped unexpectedly", "error", serveErr)
			}
		}()
		logger.Info("HTTP server listening", "address", cfg.HTTPAddr)
		return nil
	}, OnStop: func(ctx context.Context) error { return server.Shutdown(ctx) }})
	return nil
}
