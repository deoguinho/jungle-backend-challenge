package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/application"
	"github.com/redis/go-redis/v9"
)

type Cache struct{ client *redis.Client }

func New(ctx context.Context, address, password string, db int) (*Cache, error) {
	_ = ctx
	client := redis.NewClient(&redis.Options{Addr: address, Password: password, DB: db})
	return &Cache{client: client}, nil
}

func (c *Cache) Close() error                   { return c.client.Close() }
func (c *Cache) Ping(ctx context.Context) error { return c.client.Ping(ctx).Err() }

func (c *Cache) Get(ctx context.Context, key string) (application.CachedIdempotency, bool, error) {
	value, err := c.client.Get(ctx, cacheKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return application.CachedIdempotency{}, false, nil
	}
	if err != nil {
		return application.CachedIdempotency{}, false, err
	}
	var item application.CachedIdempotency
	if err := json.Unmarshal(value, &item); err != nil {
		return application.CachedIdempotency{}, false, fmt.Errorf("decode idempotency cache: %w", err)
	}
	return item, true, nil
}

func (c *Cache) Put(ctx context.Context, key string, item application.CachedIdempotency, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	value, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, cacheKey(key), value, ttl).Err()
}

func cacheKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "jungle:idempotency:" + hex.EncodeToString(sum[:])
}
