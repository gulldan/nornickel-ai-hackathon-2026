package sqlitedb_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/example/db-parser/internal/domain"
	"github.com/example/db-parser/internal/infrastructure/sqlitedb"
)

// Extractor must satisfy the domain port the application orchestration depends on.
var _ domain.TextExtractor = (*sqlitedb.Extractor)(nil)

// buildDB creates a SQLite file from the statements and returns its bytes.
func buildDB(t *testing.T, stmts ...string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	for _, s := range stmts {
		if _, execErr := db.ExecContext(t.Context(), s); execErr != nil {
			_ = db.Close()
			t.Fatalf("exec %q: %v", s, execErr)
		}
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close fixture: %v", closeErr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// TestExtract renders tables with a header, column names and « | » cells;
// empty tables are skipped and NULL renders as an empty cell.
func TestExtract(t *testing.T) {
	data := buildDB(t,
		`CREATE TABLE users (id INTEGER, name TEXT)`,
		`INSERT INTO users VALUES (1, 'Alice'), (2, NULL)`,
		`CREATE TABLE empty (x TEXT)`,
	)
	got, err := sqlitedb.NewExtractor(0).Extract(context.Background(), data, "corpus.db", "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := "Таблица: users (строк: 2)\nid | name\n1 | Alice\n2 | "
	if got != want {
		t.Fatalf("Extract = %q, want %q", got, want)
	}
}

// TestExtract_RowCap caps rendered rows per table and reports the remainder.
func TestExtract_RowCap(t *testing.T) {
	data := buildDB(t,
		`CREATE TABLE nums (n INTEGER)`,
		`INSERT INTO nums VALUES (1), (2), (3)`,
	)
	got, err := sqlitedb.NewExtractor(2).Extract(context.Background(), data, "corpus.sqlite", "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := "Таблица: nums (строк: 3)\nn\n1\n2\n…и ещё строк: 1"
	if got != want {
		t.Fatalf("Extract = %q, want %q", got, want)
	}
}

// TestExtract_MultiTableAndBlob: таблицы идут по алфавиту через пустую строку,
// бинарный блоб сворачивается в аннотацию, а не попадает в индекс.
func TestExtract_MultiTableAndBlob(t *testing.T) {
	data := buildDB(t,
		`CREATE TABLE b_files (body BLOB)`,
		`INSERT INTO b_files VALUES (zeroblob(1000))`,
		`CREATE TABLE a_notes (txt TEXT)`,
		`INSERT INTO a_notes VALUES ('привет')`,
	)
	got, err := sqlitedb.NewExtractor(0).Extract(context.Background(), data, "corpus.db", "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := "Таблица: a_notes (строк: 1)\ntxt\nпривет\n\n" +
		"Таблица: b_files (строк: 1)\nbody\n<blob: 1000 байт>"
	if got != want {
		t.Fatalf("Extract = %q, want %q", got, want)
	}
}

// TestExtract_Garbage surfaces an error for bytes that are not a database.
func TestExtract_Garbage(t *testing.T) {
	_, err := sqlitedb.NewExtractor(0).Extract(context.Background(), []byte("not a database"), "x.db", "")
	if err == nil {
		t.Fatalf("Extract expected error for garbage input, got nil")
	}
}
