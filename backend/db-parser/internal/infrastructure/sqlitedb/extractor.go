// Package sqlitedb implements domain.TextExtractor for SQLite files: таблицы
// рендерятся текстом «Таблица: <имя> (строк: N)» + строки через « | ».
package sqlitedb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	// Чистый Go-драйвер SQLite: собирается с CGO_ENABLED=0, как весь пайплайн.
	_ "modernc.org/sqlite"
)

// Extractor reads a SQLite snapshot and renders its tables as text.
type Extractor struct {
	maxRowsPerTable int
}

// NewExtractor builds an Extractor. maxRowsPerTable caps the rows rendered per
// table (the remainder is summarised); values <= 0 mean "no cap".
func NewExtractor(maxRowsPerTable int) *Extractor {
	return &Extractor{maxRowsPerTable: maxRowsPerTable}
}

// Extract renders every user table. database/sql открывает файл, а не байты,
// поэтому снапшот кладётся во временный файл на время чтения.
func (e *Extractor) Extract(ctx context.Context, data []byte, _, _ string) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sqlite parsing panicked: %v", r)
		}
	}()

	tmp, err := os.CreateTemp("", "db-parser-*.sqlite")
	if err != nil {
		return "", fmt.Errorf("create temp db: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, werr := tmp.Write(data); werr != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write temp db: %w", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return "", fmt.Errorf("close temp db: %w", cerr)
	}

	db, err := sql.Open("sqlite", "file:"+tmp.Name()+"?mode=ro&immutable=1")
	if err != nil {
		return "", fmt.Errorf("open sqlite: %w", err)
	}
	defer func() { _ = db.Close() }()

	tables, err := listTables(ctx, db)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	for _, table := range tables {
		section, terr := e.renderTable(ctx, db, table)
		if terr != nil {
			return "", terr
		}
		if section == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(section)
	}
	return b.String(), nil
}

func listTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if serr := rows.Scan(&name); serr != nil {
			return nil, fmt.Errorf("scan table name: %w", serr)
		}
		out = append(out, name)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("iterate tables: %w", rerr)
	}
	return out, nil
}

// quoteIdent экранирует идентификатор SQLite кавычками — параметризовать имя
// таблицы или колонки в SQL нельзя; имена приходят из sqlite_master той же базы.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// tableColumns returns the table's column names in schema order.
func tableColumns(ctx context.Context, db *sql.DB, quoted string) ([]string, error) {
	var q strings.Builder
	q.WriteString(`PRAGMA table_info(`)
	q.WriteString(quoted)
	q.WriteString(`)`)
	rows, err := db.QueryContext(ctx, q.String())
	if err != nil {
		return nil, fmt.Errorf("table info %s: %w", quoted, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if serr := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); serr != nil {
			return nil, fmt.Errorf("scan table info %s: %w", quoted, serr)
		}
		out = append(out, name)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("iterate table info %s: %w", quoted, rerr)
	}
	return out, nil
}

// renderTable prints one table; "" means the table is empty and is skipped.
func (e *Extractor) renderTable(ctx context.Context, db *sql.DB, table string) (string, error) {
	quoted := quoteIdent(table)

	var count strings.Builder
	count.WriteString(`SELECT count(*) FROM `)
	count.WriteString(quoted)
	var total int64
	if err := db.QueryRowContext(ctx, count.String()).Scan(&total); err != nil {
		return "", fmt.Errorf("count %s: %w", table, err)
	}
	if total == 0 {
		return "", nil
	}

	cols, err := tableColumns(ctx, db, quoted)
	if err != nil {
		return "", fmt.Errorf("columns %s: %w", table, err)
	}
	if len(cols) == 0 {
		return "", nil
	}

	var sel strings.Builder
	sel.WriteString(`SELECT `)
	for i, c := range cols {
		if i > 0 {
			sel.WriteString(", ")
		}
		sel.WriteString(quoteIdent(c))
	}
	sel.WriteString(` FROM `)
	sel.WriteString(quoted)
	if e.maxRowsPerTable > 0 {
		fmt.Fprintf(&sel, " LIMIT %d", e.maxRowsPerTable)
	}
	rows, err := db.QueryContext(ctx, sel.String())
	if err != nil {
		return "", fmt.Errorf("read %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var b strings.Builder
	fmt.Fprintf(&b, "Таблица: %s (строк: %d)\n", table, total)
	b.WriteString(strings.Join(cols, " | "))

	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	written := int64(0)
	for rows.Next() {
		if serr := rows.Scan(ptrs...); serr != nil {
			return "", fmt.Errorf("scan %s: %w", table, serr)
		}
		cells := make([]string, len(cols))
		for i, v := range vals {
			cells[i] = cellString(v)
		}
		b.WriteByte('\n')
		b.WriteString(strings.Join(cells, " | "))
		written++
	}
	if rerr := rows.Err(); rerr != nil {
		return "", fmt.Errorf("iterate %s: %w", table, rerr)
	}
	if rest := total - written; rest > 0 {
		fmt.Fprintf(&b, "\n…и ещё строк: %d", rest)
	}
	return b.String(), nil
}

// cellString renders one cell; binary blobs are summarised, not dumped.
func cellString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		if len(t) > 256 || !utf8.Valid(t) {
			return fmt.Sprintf("<blob: %d байт>", len(t))
		}
		return string(t)
	default:
		return fmt.Sprint(t)
	}
}
