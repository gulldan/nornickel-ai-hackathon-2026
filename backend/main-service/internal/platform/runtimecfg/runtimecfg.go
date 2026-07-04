// Package runtimecfg layers DB-backed runtime overrides (published to Valkey
// by main-service) over env variables: override → env → default. Reads are
// cached in-process for a few seconds, so services pick up changes without
// redeploy and survive a Valkey outage on the env fallback.
package runtimecfg

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/example/main-service/internal/platform/config"
	"github.com/example/main-service/internal/platform/valkey"
)

// ValkeyKey holds the JSON object {ENV_NAME: value} with every active override.
const ValkeyKey = "rag:app_settings"

const (
	cacheTTL     = 3 * time.Second
	fetchTimeout = 400 * time.Millisecond
)

// Overrides resolves setting values with the override → env → default order.
// A nil *Overrides (or nil Valkey client) degrades to plain env reads.
type Overrides struct {
	kv      *valkey.Client
	mu      sync.Mutex
	vals    map[string]string
	fetched time.Time
}

// New builds an Overrides reader over the shared Valkey client.
func New(kv *valkey.Client) *Overrides { return &Overrides{kv: kv} }

// Publish replaces the override set visible to every service.
func Publish(ctx context.Context, kv *valkey.Client, vals map[string]string) error {
	if vals == nil {
		vals = map[string]string{}
	}
	return kv.SetJSON(ctx, ValkeyKey, vals, 0)
}

func (o *Overrides) snapshot(ctx context.Context) map[string]string {
	if o == nil || o.kv == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if time.Since(o.fetched) < cacheTTL {
		return o.vals
	}
	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	var vals map[string]string
	if ok, err := o.kv.GetJSON(fetchCtx, ValkeyKey, &vals); err == nil {
		if !ok {
			vals = nil
		}
		o.vals = vals
	}
	o.fetched = time.Now()
	return o.vals
}

// Lookup returns the override for key, if one is set.
func (o *Overrides) Lookup(ctx context.Context, key string) (string, bool) {
	v, ok := o.snapshot(ctx)[key]
	return v, ok
}

// Get resolves key as a string.
func (o *Overrides) Get(ctx context.Context, key, def string) string {
	if v, ok := o.Lookup(ctx, key); ok && v != "" {
		return v
	}
	return config.Get(key, def)
}

// GetBool resolves key as a boolean (1, t, true, yes, on are truthy).
func (o *Overrides) GetBool(ctx context.Context, key string, def bool) bool {
	if v, ok := o.Lookup(ctx, key); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "t", "true", "yes", "on":
			return true
		case "0", "f", "false", "no", "off":
			return false
		}
	}
	return config.GetBool(key, def)
}

// GetInt resolves key as an integer.
func (o *Overrides) GetInt(ctx context.Context, key string, def int) int {
	if v, ok := o.Lookup(ctx, key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return config.GetInt(key, def)
}

// GetFloat resolves key as a float64.
func (o *Overrides) GetFloat(ctx context.Context, key string, def float64) float64 {
	if v, ok := o.Lookup(ctx, key); ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return config.GetFloat(key, def)
}
