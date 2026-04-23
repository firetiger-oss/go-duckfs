package duckfs_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io/fs"
	"log"
	"os"
	"sync"
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
	if _, err := db.Exec("SET memory_limit='100MB'"); err != nil {
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
	// Note: DuckDB may not create the spill directory if data fits in memory
	entries, err := os.ReadDir(spillDir)
	if err == nil && len(entries) != 0 {
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
	if _, err := db.Exec("SET memory_limit='100MB'"); err != nil {
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

	// Verify .tmp directory may exist in current directory (DuckDB creates it lazily)
	// The important thing is that the query completed successfully with relative temp dir
}

func ExampleNew() {
	c, err := duckdb.NewConnector("", nil)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	f, err := duckfs.New(c, newTestFS())
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	db := sql.OpenDB(f)
	defer db.Close()

	var name string
	if err := db.QueryRow(
		`SELECT instrument_name FROM read_parquet('test://testdata/example.parquet') LIMIT 1`,
	).Scan(&name); err != nil {
		log.Fatal(err)
	}

	fmt.Println(name)
	// Output:
	// BTC-28DEC24-99000-C
}

func TestConnectorDatabasePointer(t *testing.T) {
	// This test validates that the unsafe pointer cast in duckdbConnectorDatabase
	// correctly extracts the database handle from duckdb.Connector.
	// If the duckdb-go struct layout changes, New() will fail here.
	c, err := duckdb.NewConnector("", func(driver.ExecerContext) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	f, err := duckfs.New(c, newTestFS())
	if err != nil {
		t.Fatal("duckdbConnectorDatabase pointer cast likely broken: " + err.Error())
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentConnections(t *testing.T) {
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()

			c, err := duckfs.Open("", nil, newTestFS())
			if err != nil {
				errs[i] = err
				return
			}

			db := sql.OpenDB(c)
			defer db.Close()

			var name string
			err = db.QueryRow(
				`SELECT instrument_name FROM read_parquet('test://testdata/example.parquet') LIMIT 1`,
			).Scan(&name)
			if err != nil {
				errs[i] = err
			}
		}()
	}

	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
}

func TestCloseIdempotent(t *testing.T) {
	c, err := duckfs.Open("", nil, newTestFS())
	if err != nil {
		t.Fatal(err)
	}

	// First close should succeed.
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second close should not panic and should be a no-op.
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestQueryAfterClose(t *testing.T) {
	c, err := duckfs.Open("", nil, newTestFS())
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	// Attempting to connect after Close should fail.
	_, err = c.Connect(context.Background())
	if err == nil {
		t.Error("expected error when connecting after Close, got nil")
	}
}

func TestGlob(t *testing.T) {
	c, err := duckfs.Open("", nil, newTestFS())
	if err != nil {
		t.Fatal(err)
	}

	db := sql.OpenDB(c)
	defer db.Close()

	// Use glob pattern to read from multiple CSV files in different directories.
	rows, err := db.Query(`SELECT message FROM read_csv('test://testdata/folder-*/database.csv') ORDER BY message`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var messages []string
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, msg)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	if messages[0] != "hello" || messages[1] != "world" {
		t.Errorf("unexpected messages: %v", messages)
	}
}

func TestGlobWithoutGlobFS(t *testing.T) {
	// plainTestFS only implements fs.FS, not fs.GlobFS.
	// duckfs_glob falls back to fs.Glob, but fs.Glob's ReadDir walker
	// cannot preserve the "test://" double-slash protocol prefix
	// (path.Join collapses it to "test:/"), so no matches are found.
	c, err := duckfs.Open("", nil, &plainTestFS{fsys: testdata})
	if err != nil {
		t.Fatal(err)
	}

	db := sql.OpenDB(c)
	defer db.Close()

	_, err = db.Query(`SELECT message FROM read_csv('test://testdata/folder-*/database.csv')`)
	if err == nil {
		t.Error("expected error when glob fallback cannot preserve protocol prefix")
	}
}

func TestGlobLocalFilesystem(t *testing.T) {
	tempDir := t.TempDir()

	// Create test CSV files in the temp directory.
	for _, name := range []string{"data-1.csv", "data-2.csv"} {
		if err := os.WriteFile(tempDir+"/"+name, []byte("value\n"+name+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	c, err := duckfs.Open("", nil, newTestFS())
	if err != nil {
		t.Fatal(err)
	}

	db := sql.OpenDB(c)
	defer db.Close()

	// Query with a glob pattern using an absolute local path.
	rows, err := db.Query(fmt.Sprintf(`SELECT value FROM read_csv('%s/data-*.csv') ORDER BY value`, tempDir))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var values []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		values = append(values, v)
	}

	if len(values) != 2 {
		t.Fatalf("expected 2 values, got %d", len(values))
	}

	if values[0] != "data-1.csv" || values[1] != "data-2.csv" {
		t.Errorf("unexpected values: %v", values)
	}
}
