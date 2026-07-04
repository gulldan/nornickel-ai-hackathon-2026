// Command backfill is a one-shot maintenance tool that gives already-indexed
// documents a real article title: for every document still missing one it
// extracts the title from the start of its text and persists it, so the
// documents page shows article titles for historical uploads too. It reuses the
// live ingestion path's extractor (LLM over the opening text), sourcing that
// text from the indexed chunks via llm-service rather than re-reading originals.
//
// Run once after deploying title extraction, e.g.:
//
//	DB_SERVICE_ADDR=db-service:9090 LLM_SERVICE_ADDR=llm-service:9090 \
//	VLLM_URL=... VLLM_MODEL=... go run ./cmd/backfill
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/rs/zerolog"

	"github.com/example/chunk-splitter/internal/infrastructure/titler"
	"github.com/example/chunk-splitter/internal/platform/aiclients"
	"github.com/example/chunk-splitter/internal/platform/config"
	"github.com/example/chunk-splitter/internal/platform/dbclient"
	"github.com/example/chunk-splitter/internal/platform/grpcx"
	"github.com/example/chunk-splitter/internal/platform/logger"

	llmv1 "github.com/example/chunk-splitter/internal/platform/genproto/llm/v1"
)

// openingChars bounds how much chunk text is fed to the extractor: the title
// lives at the very start, and the titler trims further to its own rune budget.
const openingChars = 4000

func main() {
	log := logger.New("backfill-titles", config.Get("LOG_LEVEL", "info"))
	if err := run(log); err != nil {
		log.Error().Err(err).Msg("backfill stopped with error")
		os.Exit(1)
	}
}

func run(log zerolog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	genURL := config.Get("VLLM_URL", "")
	if genURL == "" {
		return errors.New("VLLM_URL is required: title extraction needs a generation backend")
	}

	db, err := dbclient.New(config.MustGet("DB_SERVICE_ADDR"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	conn, err := grpcx.Dial(config.MustGet("LLM_SERVICE_ADDR"))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	rag := llmv1.NewRagServiceClient(conn)

	gen := aiclients.NewGenerator(
		genURL,
		config.Get("VLLM_MODEL", ""),
		config.Get("VLLM_API_KEY", ""),
		config.GetInt("VLLM_RPM", 0),
		nil,
	)
	tl := titler.New(gen, nil)

	// Empty owner lists every owner's documents in a single pass.
	docs, err := db.ListDocuments(ctx, "")
	if err != nil {
		return err
	}

	var scanned, titled, skipped int
	for _, d := range docs {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("backfill cancelled: %w", err)
		}
		if strings.TrimSpace(d.GetTitle()) != "" {
			continue // already titled
		}
		scanned++
		text, terr := openingText(ctx, rag, d.GetId(), d.GetOwnerId())
		if terr != nil {
			log.Warn().Err(terr).Str("document_id", d.GetId()).Msg("read chunks failed")
			skipped++
			continue
		}
		title, kind := tl.Extract(ctx, text)
		if kind != "" {
			if kerr := db.SetDocumentKind(ctx, d.GetId(), kind); kerr != nil {
				log.Warn().Err(kerr).Str("document_id", d.GetId()).Msg("set kind failed")
			}
		}
		if title == "" {
			log.Info().Str("document_id", d.GetId()).Str("filename", d.GetFilename()).Msg("no title extracted; left empty")
			skipped++
			continue
		}
		if serr := db.SetDocumentTitle(ctx, d.GetId(), title); serr != nil {
			log.Warn().Err(serr).Str("document_id", d.GetId()).Msg("set title failed")
			skipped++
			continue
		}
		titled++
		log.Info().Str("document_id", d.GetId()).Str("title", title).Msg("title set")
	}
	log.Info().
		Int("total", len(docs)).Int("scanned", scanned).Int("titled", titled).Int("skipped", skipped).
		Msg("backfill complete")
	return nil
}

// openingText assembles the start of a document's text from its first indexed
// chunks (ordered by index) up to openingChars.
func openingText(ctx context.Context, rag llmv1.RagServiceClient, docID, ownerID string) (string, error) {
	resp, err := rag.DocumentChunks(ctx, &llmv1.DocumentChunksRequest{DocumentId: docID, OwnerId: ownerID})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range resp.GetChunks() {
		if b.Len() >= openingChars {
			break
		}
		b.WriteString(c.GetText())
		b.WriteByte('\n')
	}
	return b.String(), nil
}
