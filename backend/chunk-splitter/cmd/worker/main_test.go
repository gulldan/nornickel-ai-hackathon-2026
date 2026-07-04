package main

import (
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// run must propagate the first infrastructure failure (here: Qdrant on a
// closed loopback port) before dialling the broker.
func TestRunQdrantInitFails(t *testing.T) {
	t.Setenv("DB_SERVICE_ADDR", "127.0.0.1:1")
	t.Setenv("QDRANT_URL", "http://127.0.0.1:1")
	t.Setenv("OPENSEARCH_URL", "http://127.0.0.1:1")
	t.Setenv("EMBEDDINGS_URL", "http://127.0.0.1:1")
	t.Setenv("RABBITMQ_URL", "amqp://127.0.0.1:1/")

	err := run(zerolog.Nop())
	if err == nil {
		t.Fatalf("run expected qdrant init error, got nil")
	}
	if !strings.Contains(err.Error(), "qdrant") {
		t.Fatalf("run error = %v, want qdrant init failure", err)
	}
}
