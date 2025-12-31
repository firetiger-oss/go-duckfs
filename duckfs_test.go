package duckfs_test

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io/fs"
	"log"
	"os"
	"testing"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/firetiger-oss/go-duckfs"
)

func Example() {
	c, err := duckfs.Open("", nil, newTestFS())
	if err != nil {
		log.Fatal(err)
	}

	db := sql.OpenDB(c)
	defer db.Close()

	var row struct {
		Timestamp      int64  `sql:"timestamp"`
		ChangeID       int64  `sql:"change_id"`
		InstrumentName string `sql:"instrument_name"`
	}

	if err := db.QueryRow(
		`SELECT timestamp, change_id, instrument_name, FROM read_parquet('test://testdata/example.parquet')`,
	).Scan(&row.Timestamp, &row.ChangeID, &row.InstrumentName); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("%+v\n", row)

	// Output:
	// {Timestamp:1735251109024 ChangeID:83653413002 InstrumentName:BTC-28DEC24-99000-C}
}

func TestNew(t *testing.T) {
	c, err := duckdb.NewConnector("", func(driver.ExecerContext) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	test := func(want string) func(*duckfs.Connector) {
		return func(f *duckfs.Connector) {
			db := sql.OpenDB(f)
			defer db.Close()

			var got string
			if err := db.QueryRow(`SELECT message from read_csv('test://database.csv')`).Scan(&got); err != nil {
				t.Fatal(err)
			}

			if got != want {
				t.Errorf("virtual file system override did not work: %q != %q", got, want)
			}
		}
	}

	with(t, c, newTestFSFromDir("testdata/folder-1"), test("hello"))
	with(t, c, newTestFSFromDir("testdata/folder-2"), test("world"))
}

func with(t *testing.T, c *duckdb.Connector, fsys fs.FS, fn func(*duckfs.Connector)) {
	f, err := duckfs.New(c, fsys)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	fn(f)
}

func TestQueryFileNotExist(t *testing.T) {
	c, err := duckfs.Open("", nil, newTestFS())
	if err != nil {
		log.Fatal(err)
	}

	db := sql.OpenDB(c)
	defer db.Close()

	var row struct {
		Timestamp      int64  `sql:"timestamp"`
		ChangeID       int64  `sql:"change_id"`
		InstrumentName string `sql:"instrument_name"`
	}

	if err := db.QueryRow(
		`SELECT timestamp, change_id, instrument_name, FROM read_parquet('test://testdata/nonexistent.parquet')`,
	).Scan(&row.Timestamp, &row.ChangeID, &row.InstrumentName); err == nil {
		t.Error("no error when file does not exist")
	} else if _, ok := err.(*duckdb.Error); !ok {
		t.Errorf("unexpected error type: %T", err)
	}
}

func TestDirectoryExists(t *testing.T) {
	c, err := duckfs.Open("", nil, newTestFS())
	if err != nil {
		t.Fatal(err)
	}

	db := sql.OpenDB(c)
	defer db.Close()

	// This test exercises the DirectoryExists code path by reading from
	// subdirectories. DuckDB will call DirectoryExists to verify that
	// "folder-1" is a directory when accessing files in subdirectories.

	// Test reading from a file in a subdirectory
	var message string
	err = db.QueryRow(`SELECT message FROM read_csv('test://testdata/folder-1/database.csv')`).Scan(&message)
	if err != nil {
		t.Fatalf("failed to read from subdirectory: %v", err)
	}

	if message != "hello" {
		t.Errorf("unexpected message: got %q, want 'hello'", message)
	}

	// Test reading from another subdirectory
	err = db.QueryRow(`SELECT message FROM read_csv('test://testdata/folder-2/database.csv')`).Scan(&message)
	if err != nil {
		t.Fatalf("failed to read from subdirectory: %v", err)
	}

	if message != "world" {
		t.Errorf("unexpected message: got %q, want 'world'", message)
	}
}

func TestWriteOperations(t *testing.T) {
	// Create a temporary directory for testing write operations
	tempDir := t.TempDir()

	// Open a DuckDB connection with a custom temp directory
	dsn := fmt.Sprintf("?temp_directory=%s", tempDir)
	c, err := duckfs.Open(dsn, nil, newTestFS())
	if err != nil {
		t.Fatal(err)
	}

	db := sql.OpenDB(c)
	defer db.Close()

	// Export a Parquet file to trigger write operations
	// This will use CreateDirectory, OpenFile with write flags, Write, WriteAt, etc.
	exportPath := tempDir + "/export/output.parquet"

	query := fmt.Sprintf(`
		COPY (
			SELECT
				range AS id,
				'value_' || range::VARCHAR AS name,
				range * 2 AS doubled
			FROM range(1000)
		) TO '%s' (FORMAT PARQUET)
	`, exportPath)

	if _, err := db.Exec(query); err != nil {
		t.Fatalf("COPY TO failed (write operations may not be working): %v", err)
	}

	// Verify the file was created
	if _, err := os.Stat(exportPath); err != nil {
		t.Fatalf("exported file not found: %v", err)
	}

	// Verify the directory was created
	exportDir := tempDir + "/export"
	if info, err := os.Stat(exportDir); err != nil {
		t.Fatalf("export directory not found: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("export path is not a directory")
	}

	// Verify we can read the exported file back
	var count int
	if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM '%s'", exportPath)).Scan(&count); err != nil {
		t.Fatalf("failed to read exported file: %v", err)
	}

	if count != 1000 {
		t.Errorf("expected 1000 rows in exported file, got %d", count)
	}
}

func TestSpillToDisk(t *testing.T) {
	// This test verifies spill-to-disk works by using a dataset that exceeds memory limit
	tempDir := t.TempDir()
	spillDir := tempDir + "/spill"

	dsn := fmt.Sprintf("%s", tempDir+"/test.db")
	c, err := duckfs.Open(dsn, nil, newTestFS())
	if err != nil {
		t.Fatal(err)
	}

	db := sql.OpenDB(c)
	defer db.Close()

	// Set temp directory via SQL as recommended in DuckDB docs
	if _, err := db.Exec(fmt.Sprintf("SET temp_directory='%s'", spillDir)); err != nil {
		t.Fatalf("failed to set temp_directory: %v", err)
	}

	// Set memory limit low enough to force spilling
	if _, err := db.Exec("SET memory_limit='50MB'"); err != nil {
		t.Fatalf("failed to set memory limit: %v", err)
	}

	if _, err := db.Exec("SET threads=1"); err != nil {
		t.Fatalf("failed to set threads: %v", err)
	}

	if _, err := db.Exec("SET preserve_insertion_order=false"); err != nil {
		t.Fatalf("failed to set preserve_insertion_order: %v", err)
	}

	// Create table with data larger than memory limit
	// Using ORDER BY on large string data should force external sorting
	_, err = db.Exec(`
		CREATE TABLE test_data AS
		SELECT
			range AS id,
			repeat('x', 500) AS payload
		FROM range(500000)
	`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Force sorting which should spill to disk
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM (SELECT * FROM test_data ORDER BY payload DESC, id ASC)").Scan(&count)
	if err != nil {
		t.Fatalf("failed to run ORDER BY query: %v", err)
	}

	if count != 500000 {
		t.Errorf("expected 500000 rows, got %d", count)
	}

	// Verify spill directory was cleaned up (DuckDB removes temp files after query completes)
	entries, err := os.ReadDir(spillDir)
	if err != nil {
		t.Fatalf("spill directory should exist: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected spill directory to be empty after query, got %d entries (remove functions may not be working)", len(entries))
		for _, entry := range entries {
			t.Logf("  - %s (isDir: %v)", entry.Name(), entry.IsDir())
		}
	}
}

func TestMoveFile(t *testing.T) {
	// This test verifies MoveFile works by forcing a checkpoint operation,
	// which internally uses MoveFile to atomically replace database files.
	tempDir := t.TempDir()
	dbPath := tempDir + "/test.db"

	c, err := duckfs.Open(dbPath, nil, newTestFS())
	if err != nil {
		t.Fatal(err)
	}

	db := sql.OpenDB(c)
	defer db.Close()

	// Create a table and insert data
	if _, err := db.Exec("CREATE TABLE test_move (id INTEGER, value VARCHAR)"); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	if _, err := db.Exec("INSERT INTO test_move VALUES (1, 'hello'), (2, 'world')"); err != nil {
		t.Fatalf("failed to insert data: %v", err)
	}

	// Force a checkpoint - this uses MoveFile internally to atomically
	// move WAL/temporary files to their final locations
	if _, err := db.Exec("CHECKPOINT"); err != nil {
		t.Fatalf("CHECKPOINT failed (MoveFile may not be working): %v", err)
	}

	// Verify data is still readable after checkpoint
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM test_move").Scan(&count); err != nil {
		t.Fatalf("failed to query after checkpoint: %v", err)
	}

	if count != 2 {
		t.Errorf("expected 2 rows, got %d", count)
	}

	// Verify database file exists
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file not found after checkpoint: %v", err)
	}
}

func TestRelativeTempDirectory(t *testing.T) {
	// Create a temporary directory and change to it
	tempDir := t.TempDir()
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}

	// Open DuckDB with a relative temp directory
	c, err := duckfs.Open("test.db", nil, newTestFS())
	if err != nil {
		t.Fatal(err)
	}

	db := sql.OpenDB(c)
	defer db.Close()

	// Set relative temp directory - should work with protocol-based routing
	if _, err := db.Exec("SET temp_directory='.tmp'"); err != nil {
		t.Fatalf("failed to set relative temp_directory: %v", err)
	}

	// Create a large table with ORDER BY to trigger spilling to temp directory
	if _, err := db.Exec("SET memory_limit='50MB'"); err != nil {
		t.Fatalf("failed to set memory limit: %v", err)
	}

	if _, err := db.Exec("SET threads=1"); err != nil {
		t.Fatalf("failed to set threads: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE test AS
		SELECT
			range AS id,
			repeat('x', 500) AS payload
		FROM range(500000)
	`)
	if err != nil {
		t.Fatalf("failed to create table with relative temp directory: %v", err)
	}

	// Force sorting which should spill to .tmp directory
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM (SELECT * FROM test ORDER BY payload DESC, id ASC)").Scan(&count)
	if err != nil {
		t.Fatalf("failed to run ORDER BY query: %v", err)
	}

	if count != 500000 {
		t.Errorf("expected 500000 rows, got %d", count)
	}

	// Verify .tmp directory exists in current directory
	if _, err := os.Stat(".tmp"); err != nil {
		t.Errorf(".tmp directory should exist: %v", err)
	}
}
