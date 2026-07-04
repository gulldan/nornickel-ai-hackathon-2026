// Package valkey wraps a Redis-protocol client (Valkey is a Redis fork). It
// backs auth-service refresh-token storage and the llm-service answer cache,
// keeping services stateless. JSON goes through the sonic-backed jsonx facade.
package valkey

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/example/main-service/internal/platform/jsonx"
)

// Client is a small JSON-friendly wrapper over *redis.Client.
type Client struct {
	rdb *redis.Client
}

// New connects to Valkey/Redis and verifies the connection.
func New(ctx context.Context, addr, password string, db int) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping valkey: %w", err)
	}
	return &Client{rdb: rdb}, nil
}

// Close releases the connection pool.
func (c *Client) Close() error {
	if err := c.rdb.Close(); err != nil {
		return fmt.Errorf("close valkey: %w", err)
	}
	return nil
}

// Ping verifies connectivity for readiness probes.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping valkey: %w", err)
	}
	return nil
}

// SetJSON marshals v and stores it under key with the given TTL (0 = no expiry).
func (c *Client) SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error {
	b, err := jsonx.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode value: %w", err)
	}
	if serr := c.rdb.Set(ctx, key, b, ttl).Err(); serr != nil {
		return fmt.Errorf("valkey set %s: %w", key, serr)
	}
	return nil
}

// GetJSON loads key into dest, returning (false, nil) on a cache miss.
func (c *Client) GetJSON(ctx context.Context, key string, dest any) (bool, error) {
	b, err := c.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("valkey get %s: %w", key, err)
	}
	if uerr := jsonx.Unmarshal(b, dest); uerr != nil {
		return false, fmt.Errorf("decode value: %w", uerr)
	}
	return true, nil
}

// Set stores a raw string value with a TTL.
func (c *Client) Set(ctx context.Context, key, val string, ttl time.Duration) error {
	if err := c.rdb.Set(ctx, key, val, ttl).Err(); err != nil {
		return fmt.Errorf("valkey set %s: %w", key, err)
	}
	return nil
}

// SetNX stores a raw string only when key does not already exist.
func (c *Client) SetNX(ctx context.Context, key, val string, ttl time.Duration) (bool, error) {
	ok, err := c.rdb.SetNX(ctx, key, val, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("valkey setnx %s: %w", key, err)
	}
	return ok, nil
}

// Get reads a raw string value, returning ("", false, nil) on a miss.
func (c *Client) Get(ctx context.Context, key string) (string, bool, error) {
	v, err := c.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("valkey get %s: %w", key, err)
	}
	return v, true, nil
}

// Del removes one or more keys.
func (c *Client) Del(ctx context.Context, keys ...string) error {
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("valkey del: %w", err)
	}
	return nil
}

// LPush prepends values to a Redis list.
func (c *Client) LPush(ctx context.Context, key string, values ...string) error {
	args := make([]any, len(values))
	for i, v := range values {
		args[i] = v
	}
	if err := c.rdb.LPush(ctx, key, args...).Err(); err != nil {
		return fmt.Errorf("valkey lpush %s: %w", key, err)
	}
	return nil
}

// LTrim keeps only the inclusive [start, stop] range of a Redis list.
func (c *Client) LTrim(ctx context.Context, key string, start, stop int64) error {
	if err := c.rdb.LTrim(ctx, key, start, stop).Err(); err != nil {
		return fmt.Errorf("valkey ltrim %s: %w", key, err)
	}
	return nil
}

// LRange returns the inclusive [start, stop] range of a Redis list.
func (c *Client) LRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	values, err := c.rdb.LRange(ctx, key, start, stop).Result()
	if err != nil {
		return nil, fmt.Errorf("valkey lrange %s: %w", key, err)
	}
	return values, nil
}

// IncrTTL atomically increments key and refreshes its TTL, returning the new
// value. It backs short-lived gauges (e.g. the in-flight interactive-query
// counter) that must self-heal if a holder dies before decrementing.
func (c *Client) IncrTTL(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	n, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("valkey incr %s: %w", key, err)
	}
	if ttl > 0 {
		if eerr := c.rdb.Expire(ctx, key, ttl).Err(); eerr != nil {
			return n, fmt.Errorf("valkey expire %s: %w", key, eerr)
		}
	}
	return n, nil
}

// HIncrBy atomically adds n to the hash field, creating the hash/field as needed.
// It backs the LLM usage day-hashes (per model/operation token & cost counters).
func (c *Client) HIncrBy(ctx context.Context, key, field string, n int64) error {
	if err := c.rdb.HIncrBy(ctx, key, field, n).Err(); err != nil {
		return fmt.Errorf("valkey hincrby %s: %w", key, err)
	}
	return nil
}

// HGetAll returns every field/value of a hash (empty map for a missing key).
func (c *Client) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	m, err := c.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("valkey hgetall %s: %w", key, err)
	}
	return m, nil
}

// Expire (re)sets a key's TTL.
func (c *Client) Expire(ctx context.Context, key string, ttl time.Duration) error {
	if err := c.rdb.Expire(ctx, key, ttl).Err(); err != nil {
		return fmt.Errorf("valkey expire %s: %w", key, err)
	}
	return nil
}

// Decr decrements key, returning the new value. Counters may dip below zero
// when a TTL expired between paired Incr/Decr calls; readers clamp at zero.
func (c *Client) Decr(ctx context.Context, key string) (int64, error) {
	n, err := c.rdb.Decr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("valkey decr %s: %w", key, err)
	}
	return n, nil
}

// GetInt reads an integer value, returning 0 for a missing key.
func (c *Client) GetInt(ctx context.Context, key string) (int64, error) {
	n, err := c.rdb.Get(ctx, key).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("valkey get %s: %w", key, err)
	}
	return n, nil
}
