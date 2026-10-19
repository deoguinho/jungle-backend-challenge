package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	awsadapter "github.com/junglegaming/backend-challenge-go/internal/adapters/outbound/aws"
	pgadapter "github.com/junglegaming/backend-challenge-go/internal/adapters/outbound/postgres"
	redisadapter "github.com/junglegaming/backend-challenge-go/internal/adapters/outbound/redis"
	"github.com/junglegaming/backend-challenge-go/internal/application"
	"github.com/junglegaming/backend-challenge-go/internal/domain/shared"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wagering"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
	"go.uber.org/fx"
)

type clock struct{}

func (clock) Now() time.Time { return time.Now().UTC() }

type ids struct{}

func (ids) NewID() string { return uuid.NewString() }

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
		fx.Provide(func() application.Clock { return clock{} }),
		fx.Provide(func() application.IDGenerator { return ids{} }),
		fx.Provide(newPostgres, newRedis, newSQS),
		fx.Provide(func(db *pgadapter.DB) application.UnitOfWork { return db }),
		fx.Provide(func(cache *redisadapter.Cache) application.IdempotencyCache { return cache }),
		fx.Provide(func(uow application.UnitOfWork, ids application.IDGenerator, c application.Clock, cache application.IdempotencyCache, cfg config.Config) *application.SubmitWager {
			return application.NewSubmitWager(uow, ids, c, cache, cfg.IdempotencyTTL, metrics.Default)
		}),
		fx.Provide(application.NewRetryPendingReference, application.NewExpirePendingReference),
		fx.Invoke(startWorker),
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

func startWorker(lc fx.Lifecycle, cfg config.Config, db *pgadapter.DB, sqs *awsadapter.SQS, submit *application.SubmitWager, retry *application.RetryPendingReference, expire *application.ExpirePendingReference, logger *slog.Logger) error {
	if cfg.WorkerKind != "sqs-consumer" && cfg.WorkerKind != "reference" && cfg.WorkerKind != "outbox-publisher" {
		return fmt.Errorf("WORKER_KIND must be sqs-consumer, reference, or outbox-publisher")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	metricsServer := &http.Server{Addr: cfg.MetricsAddr, Handler: metrics.Default, ReadHeaderTimeout: 5 * time.Second}
	lc.Append(fx.Hook{OnStart: func(context.Context) error {
		listener, listenErr := net.Listen("tcp", cfg.MetricsAddr)
		if listenErr != nil {
			return listenErr
		}
		go func() {
			if serveErr := metricsServer.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				logger.Error("metrics server stopped unexpectedly", "error", serveErr)
			}
		}()
		go func() {
			defer close(done)
			switch cfg.WorkerKind {
			case "sqs-consumer":
				runConsumer(ctx, cfg, sqs, submit, logger)
			case "reference":
				runReferenceWorker(ctx, db, retry, expire, logger)
			case "outbox-publisher":
				runOutboxPublisher(ctx, cfg, db, sqs, logger)
			}
		}()
		return nil
	}, OnStop: func(stopCtx context.Context) error {
		cancel()
		_ = metricsServer.Shutdown(stopCtx)
		select {
		case <-done:
			return nil
		case <-stopCtx.Done():
			return stopCtx.Err()
		}
	}})
	return nil
}

type wagerEnvelope struct {
	MessageID string `json:"messageId"`
	Type      string `json:"type"`
	Data      struct {
		ProviderID            string        `json:"providerId"`
		ExternalTransactionID string        `json:"externalTransactionId"`
		IdempotencyKey        string        `json:"idempotencyKey"`
		PlayerID              string        `json:"playerId"`
		WalletID              string        `json:"walletId"`
		RoundID               string        `json:"roundId"`
		GameID                string        `json:"gameId"`
		Kind                  wagering.Kind `json:"kind"`
		Money                 struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"money"`
		ReferenceExternalTransactionID string `json:"referenceExternalTransactionId,omitempty"`
	} `json:"data"`
}

func runConsumer(ctx context.Context, cfg config.Config, client *awsadapter.SQS, submit *application.SubmitWager, logger *slog.Logger) {
	const consumerName = "wager-transaction-consumer"
	for ctx.Err() == nil {
		if depth, err := client.ApproximateMessages(ctx, cfg.SQSDLQURL); err == nil {
			metrics.Default.Set("jungle_dlq_messages", float64(depth))
		}
		messages, err := client.Receive(ctx, cfg.SQSQueueURL, 10, 20, int32(cfg.SQSVisibilityTimeout))
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Error("receive SQS messages", "error", err)
			metrics.Default.Inc("jungle_sqs_receive_failures_total")
			pause(ctx, 2*time.Second)
			continue
		}
		sem := make(chan struct{}, cfg.WorkerConcurrency)
		var workers sync.WaitGroup
		for _, msg := range messages {
			sem <- struct{}{}
			workers.Add(1)
			go func(message awsadapter.Message) {
				defer workers.Done()
				defer func() { <-sem }()
				if err := handleMessage(ctx, cfg, client, submit, consumerName, message); err != nil {
					metrics.Default.Inc("jungle_sqs_message_retries_total")
					logger.Error("SQS message processing failed", "message_id", message.ID, "error", err)
				}
			}(msg)
		}
		workers.Wait()
	}
}
func handleMessage(ctx context.Context, cfg config.Config, client *awsadapter.SQS, submit *application.SubmitWager, consumer string, msg awsadapter.Message) error {
	var envelope wagerEnvelope
	if err := json.Unmarshal([]byte(msg.Body), &envelope); err != nil {
		return fmt.Errorf("decode wager envelope: %w", err)
	}
	if envelope.MessageID == "" || envelope.Type != "WagerTransactionRequested" || envelope.Data.ProviderID == "" || envelope.Data.IdempotencyKey == "" {
		return errors.New("invalid wager message envelope")
	}
	payloadHash := sha256.Sum256([]byte(msg.Body))
	amount, err := shared.NewMoney(envelope.Data.Money.Amount, envelope.Data.Money.Currency)
	if err != nil {
		return err
	}
	in := application.SubmitWagerInput{ExternalID: envelope.Data.ExternalTransactionID, IdempotencyKey: envelope.Data.IdempotencyKey, PlayerID: envelope.Data.PlayerID, WalletID: envelope.Data.WalletID, RoundID: envelope.Data.RoundID, GameID: envelope.Data.GameID, Kind: envelope.Data.Kind, Amount: amount, ReferenceExternalID: envelope.Data.ReferenceExternalTransactionID}
	in.PayloadHash, err = application.CanonicalPayloadHash(envelope.Data.ProviderID, in)
	if err != nil {
		return err
	}
	_, err = submit.ConsumeWagerMessage(ctx, envelope.Data.ProviderID, in, application.InboxDelivery{Consumer: consumer, MessageID: envelope.MessageID, PayloadHash: payloadHash[:]})
	if err != nil {
		return err
	}
	return client.Delete(ctx, cfg.SQSQueueURL, msg.Receipt)
}

func runReferenceWorker(ctx context.Context, db *pgadapter.DB, retry *application.RetryPendingReference, expire *application.ExpirePendingReference, logger *slog.Logger) {
	for ctx.Err() == nil {
		items, err := db.ListPendingReferences(ctx, 100)
		if err != nil {
			logger.Error("list pending references", "error", err)
		} else {
			for _, item := range items {
				var processErr error
				if item.Expired {
					processErr = expire.Execute(ctx, item.ProviderID, item.IdempotencyKey)
				} else {
					var result application.WagerResult
					result, processErr = retry.Execute(ctx, item.ProviderID, item.IdempotencyKey)
					if processErr == nil && result.Pending {
						processErr = db.DeferPendingReference(ctx, item.ProviderID, item.IdempotencyKey)
					}
				}
				if processErr != nil {
					metrics.Default.Inc("jungle_reference_retry_failures_total")
					logger.Error("retry pending reference", "provider_id", item.ProviderID, "error", processErr)
				}
			}
		}
		pause(ctx, 2*time.Second)
	}
}
func runOutboxPublisher(ctx context.Context, cfg config.Config, db *pgadapter.DB, sqs *awsadapter.SQS, logger *slog.Logger) {
	workerID := uuid.NewString()
	for ctx.Err() == nil {
		if age, ageErr := db.OldestPendingOutboxAge(ctx); ageErr == nil {
			metrics.Default.Set("jungle_outbox_oldest_pending_age_seconds", age)
		}
		events, err := db.ClaimOutbox(ctx, workerID, 50)
		if err != nil {
			logger.Error("claim outbox events", "error", err)
		} else {
			for _, record := range events {
				err = sqs.PublishEvent(ctx, cfg.SQSEventsQueueURL, record.Event)
				if err != nil {
					metrics.Default.Inc("jungle_outbox_publish_retries_total")
				}
				if finishErr := db.FinishOutbox(ctx, workerID, record.Event.ID, err); finishErr != nil {
					logger.Error("update outbox event", "event_id", record.Event.ID, "error", finishErr)
				}
			}
		}
		pause(ctx, time.Second)
	}
}
func pause(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
