# chunk-splitter

`chunk-splitter` consumes `DocumentParsed` events from the `chunk` queue,
splits parsed text into retrievable chunks, embeds them, and writes the same
chunk ids to Qdrant and OpenSearch for hybrid retrieval.

## Current Behavior

- Generic text uses the recursive splitter in `internal/infrastructure/splitter`.
- CSV/TSV-like text is normalized first through
  `internal/infrastructure/delimited`: rows become self-contained evidence with
  `source_uri=file.csv#rows=...` and repeated headers.
- Page offsets from `DocumentParsed.metadata["page_offsets"]` are used when
  present to annotate chunks with page ranges.
- Workbook sidecars can now arrive from `office-parser` via
  `workbook_sidecar_object_key`, but this worker does not yet read that sidecar
  to enforce workbook row-window boundaries.

## Important Env Vars

- `CHUNK_SIZE`, default `1000`: rune budget for the recursive fallback splitter.
- `CHUNK_OVERLAP`, default `150`: overlap for recursive fallback chunks.
- `CHUNK_MAX_TOKENS`, default `512`: token budget when `TOKENIZER_URL` is set.
- `SPLIT_WINDOW_MB`, default `8`: bounded window for large text processing.
- `TEXT_MAX_MB`, default `512`: parsed text safety cap.
- `EMBED_BATCH`, default `64`: chunks per embedding/indexing batch.
- `CONTEXTUAL_HEADERS`, default `true`: prefix chunks with document/section
  context for embeddings and BM25.

## Protobuf Contract

The workbook MVP did not require a protobuf change. Parser-specific fields are
passed through the existing `DocumentParsed.metadata` map, for example:

- `workbook_mode`
- `workbook_sidecar_object_key`
- `workbook_sidecar_version`
- `workbook_block_count`
- `workbook_formula_count`

That keeps the MVP additive. A protobuf change is only needed when downstream
services must treat parser blocks/sidecars as first-class structured messages
instead of metadata and object-store artifacts.

## Known Gaps

- Workbook `source_uri` boundaries are present in parser text, but the splitter
  does not yet guarantee one final chunk per workbook row-window/source_uri.
- `workbook.sidecar.json` is stored by `office-parser`, but not consumed here.
- CSV/TSV currently has row-anchored text, but not a durable
  `delimited.sidecar.json`.
- PDF/DOCX/PPTX/OCR layout-aware chunking remains a separate stage.
