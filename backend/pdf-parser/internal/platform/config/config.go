// Package config provides small helpers for reading configuration from
// environment variables. Every service builds its own typed Config struct on
// top of these primitives, keeping the 12-factor "config in the environment"
// contract that lets the same image run unchanged in dev and prod.
package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Get returns the value of the environment variable named by key, or def when
// the variable is unset or empty.
func Get(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// MustGet returns the value of key or terminates the process. Use it only for
// configuration without a sane default (e.g. database DSNs).
func MustGet(key string) string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		log.Fatalf("config: required environment variable %q is not set", key)
	}
	return v
}

// GetInt parses key as an integer, falling back to def on absence or error.
func GetInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		log.Printf("config: %q is not a valid int, using default %d", key, def)
	}
	return def
}

// GetFloat parses key as a float64, falling back to def on absence or error.
func GetFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
		log.Printf("config: %q is not a valid float, using default %g", key, def)
	}
	return def
}

// GetBool parses key as a boolean (1, t, true, yes, on are truthy).
func GetBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "t", "true", "yes", "on":
			return true
		case "0", "f", "false", "no", "off":
			return false
		}
	}
	return def
}

// GetDuration parses key as a Go duration string (e.g. "15m", "750ms").
func GetDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Printf("config: %q is not a valid duration, using default %s", key, def)
	}
	return def
}

// GetSlice splits key on commas, trimming whitespace and dropping empties.
func GetSlice(key, def string) []string {
	raw := Get(key, def)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
