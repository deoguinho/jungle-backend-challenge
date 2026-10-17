package config

import (
	"errors"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr, DatabaseURL                                          string
	RedisAddr, RedisPassword                                       string
	RedisDB                                                        int
	IdempotencyTTL                                                 time.Duration
	AWSRegion, AWSEndpointURL, AWSAccessKeyID, AWSSecretAccessKey  string
	SQSQueueURL, SQSEventsQueueURL                                 string
	OIDCIssuer, OIDCClientID, OIDCProviderClientID, OIDCProviderID string
	WorkerKind                                                     string
	WorkerConcurrency                                              int
}

func Load() (Config, error) {
	c := Config{HTTPAddr: value("HTTP_ADDR", ":8080"), DatabaseURL: os.Getenv("DATABASE_URL"), RedisAddr: value("REDIS_ADDR", "localhost:6379"), RedisPassword: os.Getenv("REDIS_PASSWORD"), AWSRegion: value("AWS_REGION", "us-east-1"), AWSEndpointURL: os.Getenv("AWS_ENDPOINT_URL"), AWSAccessKeyID: value("AWS_ACCESS_KEY_ID", "test"), AWSSecretAccessKey: value("AWS_SECRET_ACCESS_KEY", "test"), SQSQueueURL: os.Getenv("SQS_QUEUE_URL"), SQSEventsQueueURL: os.Getenv("SQS_EVENTS_QUEUE_URL"), OIDCIssuer: os.Getenv("OIDC_ISSUER"), OIDCClientID: value("OIDC_CLIENT_ID", "jungle-api"), OIDCProviderClientID: value("OIDC_PROVIDER_CLIENT_ID", "jungle-provider-local"), OIDCProviderID: value("OIDC_PROVIDER_ID", "local"), WorkerKind: os.Getenv("WORKER_KIND"), WorkerConcurrency: integer("WORKER_CONCURRENCY", 4)}
	var err error
	c.RedisDB, err = strconv.Atoi(value("REDIS_DB", "0"))
	if err != nil || c.RedisDB < 0 {
		return Config{}, errors.New("REDIS_DB must be a non-negative integer")
	}
	c.IdempotencyTTL, err = time.ParseDuration(value("IDEMPOTENCY_CACHE_TTL", "24h"))
	if err != nil || c.IdempotencyTTL <= 0 {
		return Config{}, errors.New("IDEMPOTENCY_CACHE_TTL must be a positive duration")
	}
	if c.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	return c, nil
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func integer(key string, fallback int) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil || n < 1 {
		return fallback
	}
	return n
}
