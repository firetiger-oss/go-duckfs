package duckfs_test

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/firetiger-oss/go-duckfs"
	"github.com/marcboeker/go-duckdb/v2"
)

// testMutableFS is a directory-based mutable filesystem for testing
type testMutableFS string

func newTestMutableFS(dir string) testMutableFS {
	return testMutableFS(dir)
}

func (mfs testMutableFS) Open(name string) (fs.File, error) {
	fullPath := filepath.Join(string(mfs), name)
	return os.Open(fullPath)
}

func (mfs testMutableFS) Create(name string) (fs.File, error) {
	fullPath := filepath.Join(string(mfs), name)

	// Ensure the directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	return os.Create(fullPath)
}

func (mfs testMutableFS) Remove(name string) error {
	fullPath := filepath.Join(string(mfs), name)
	return os.Remove(fullPath)
}

func (mfs testMutableFS) Truncate(name string, size int64) error {
	fullPath := filepath.Join(string(mfs), name)
	return os.Truncate(fullPath, size)
}

// listFilesystemContents returns all files and directories in the filesystem
func listFilesystemContents(dir string) ([]string, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip the root directory itself
		if path == dir {
			return nil
		}
		// Get relative path from the base directory
		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, relPath)
		return nil
	})
	return files, err
}

// verifyFilesystemEmpty checks that the filesystem contains no files
func verifyFilesystemEmpty(t *testing.T, tempDir string, context string) {
	files, err := listFilesystemContents(tempDir)
	if err != nil {
		t.Fatalf("Failed to list filesystem contents %s: %v", context, err)
	}
	if len(files) > 0 {
		t.Errorf("Expected filesystem to be empty %s, but found files: %v", context, files)
	}
}

// verifyFilesystemNotEmpty checks that the filesystem contains files
func verifyFilesystemNotEmpty(t *testing.T, tempDir string, context string) []string {
	files, err := listFilesystemContents(tempDir)
	if err != nil {
		t.Fatalf("Failed to list filesystem contents %s: %v", context, err)
	}
	if len(files) == 0 {
		t.Errorf("Expected filesystem to contain files %s, but it was empty", context)
	}
	return files
}

func Example() {
	c, err := duckfs.Open("", nil, os.DirFS("testdata"))
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
		`SELECT timestamp, change_id, instrument_name, FROM read_parquet('example.parquet')`,
	).Scan(&row.Timestamp, &row.ChangeID, &row.InstrumentName); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("%+v\n", row)

	// Output:
	// {Timestamp:1735251109024 ChangeID:83653413002 InstrumentName:BTC-28DEC24-99000-C}
}

func TestNew(t *testing.T) {
	c, err := duckdb.NewConnector("test.db", func(driver.ExecerContext) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	test := func(want string) func(*duckfs.Connector) {
		return func(f *duckfs.Connector) {
			db := sql.OpenDB(f)
			defer db.Close()

			var got string
			if err := db.QueryRow(`SELECT message from read_csv('database.csv')`).Scan(&got); err != nil {
				t.Fatal(err)
			}

			if got != want {
				t.Errorf("virtual file system override did not work: %q != %q", got, want)
			}
		}
	}

	with(t, c, os.DirFS("testdata/folder-1"), test("hello"))
	with(t, c, os.DirFS("testdata/folder-2"), test("world"))
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
	c, err := duckfs.Open("", nil, os.DirFS("whatever"))
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
		`SELECT timestamp, change_id, instrument_name, FROM read_parquet('example.parquet')`,
	).Scan(&row.Timestamp, &row.ChangeID, &row.InstrumentName); err == nil {
		t.Error("no error when file does not exist")
	} else if _, ok := err.(*duckdb.Error); !ok {
		t.Errorf("unexpected error type: %T", err)
	}
}

func TestMutableFS(t *testing.T) {
	tempDir := t.TempDir()
	mfs := newTestMutableFS(tempDir)

	// Test that the mutable filesystem satisfies the MutableFS interface
	var _ duckfs.MutableFS = mfs

	// Test Create
	f, err := mfs.Create("test.txt")
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	// Test Write (check if file supports io.Writer)
	w, ok := f.(io.Writer)
	if !ok {
		t.Fatal("created file does not implement io.Writer")
	}

	testData := []byte("Hello, World!")
	n, err := w.Write(testData)
	if err != nil {
		t.Fatalf("failed to write to file: %v", err)
	}
	if n != len(testData) {
		t.Errorf("wrote %d bytes, expected %d", n, len(testData))
	}

	f.Close()

	// Test Open and Read
	f2, err := mfs.Open("test.txt")
	if err != nil {
		t.Fatalf("failed to open file: %v", err)
	}
	defer f2.Close()

	readData := make([]byte, len(testData))
	n, err = f2.Read(readData)
	if err != nil {
		t.Fatalf("failed to read from file: %v", err)
	}
	if n != len(testData) {
		t.Errorf("read %d bytes, expected %d", n, len(testData))
	}
	if string(readData) != string(testData) {
		t.Errorf("read data %q, expected %q", string(readData), string(testData))
	}

	// Test Remove
	err = mfs.Remove("test.txt")
	if err != nil {
		t.Fatalf("failed to remove file: %v", err)
	}

	// Test that file no longer exists
	_, err = mfs.Open("test.txt")
	if err == nil {
		t.Error("file still exists after removal")
	}
}

func ExampleMutableFS() {
	// Create a temporary directory for this example
	tempDir, err := os.MkdirTemp("", "duckfs_example")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Create a mutable filesystem
	mfs := newTestMutableFS(tempDir)

	// Open a DuckDB connection with the mutable filesystem
	c, err := duckfs.Open("", nil, mfs)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	db := sql.OpenDB(c)
	defer db.Close()

	// Example: Create a CSV file through DuckDB operations
	// Note: This is a conceptual example. In practice, you'd need DuckDB
	// to support writing operations to your virtual filesystem

	fmt.Println("Mutable filesystem example setup complete")

	// In a real scenario, you might:
	// 1. Use COPY TO to write query results to files
	// 2. Create temporary files for intermediate processing
	// 3. Remove files after processing

	// Output:
	// Mutable filesystem example setup complete
}

// TestFileCreateAndWrite tests file creation and writing through DuckDB's filesystem interface
func TestFileCreateAndWrite(t *testing.T) {
	tempDir := t.TempDir()
	mfs := newTestMutableFS(tempDir)

	connector, err := duckfs.Open("test.db", func(driver.ExecerContext) error { return nil }, mfs)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()

	db := sql.OpenDB(connector)
	defer db.Close()

	// Test 1: Verify that we can create a CSV file with sample data
	testData := "name,age,city\nAlice,30,New York\nBob,25,San Francisco\n"

	// First, create the file in our mutable filesystem so we can test writing
	f, err := mfs.Create("test_output.csv")
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	w, ok := f.(io.Writer)
	if !ok {
		t.Fatal("Created file does not implement io.Writer")
	}

	n, err := w.Write([]byte(testData))
	if err != nil {
		t.Fatalf("Failed to write test data: %v", err)
	}
	if n != len(testData) {
		t.Errorf("Wrote %d bytes, expected %d", n, len(testData))
	}
	f.Close()

	// Test 2: Verify DuckDB can read the file we created
	rows, err := db.Query("SELECT name, age, city FROM read_csv('test_output.csv')")
	if err != nil {
		t.Fatalf("Failed to read CSV file through DuckDB: %v", err)
	}
	defer rows.Close()

	var results []struct {
		Name string
		Age  int
		City string
	}

	for rows.Next() {
		var name, city string
		var age int
		if err := rows.Scan(&name, &age, &city); err != nil {
			t.Fatalf("Failed to scan row: %v", err)
		}
		results = append(results, struct {
			Name string
			Age  int
			City string
		}{name, age, city})
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 rows, got %d", len(results))
	}

	if results[0].Name != "Alice" || results[0].Age != 30 || results[0].City != "New York" {
		t.Errorf("First row incorrect: got %+v", results[0])
	}

	if results[1].Name != "Bob" || results[1].Age != 25 || results[1].City != "San Francisco" {
		t.Errorf("Second row incorrect: got %+v", results[1])
	}
}

// TestFileWriteOperations tests various file writing scenarios
func TestFileWriteOperations(t *testing.T) {
	tempDir := t.TempDir()
	mfs := newTestMutableFS(tempDir)

	connector, err := duckfs.Open("test.db", func(driver.ExecerContext) error { return nil }, mfs)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()

	// Test writing different file types and sizes
	testCases := []struct {
		filename string
		content  string
		desc     string
	}{
		{"small.txt", "Hello", "small text file"},
		{"empty.txt", "", "empty file"},
		{"large.txt", strings.Repeat("A", 10000), "large text file"},
		{"with_newlines.txt", "Line 1\nLine 2\nLine 3\n", "file with newlines"},
		{"binary.dat", string([]byte{0, 1, 2, 255, 254, 253}), "binary data"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			// Create and write file
			f, err := mfs.Create(tc.filename)
			if err != nil {
				t.Fatalf("Failed to create %s: %v", tc.filename, err)
			}

			w, ok := f.(io.Writer)
			if !ok {
				t.Fatalf("File %s does not implement io.Writer", tc.filename)
			}

			n, err := w.Write([]byte(tc.content))
			if err != nil {
				t.Fatalf("Failed to write to %s: %v", tc.filename, err)
			}
			if n != len(tc.content) {
				t.Errorf("Wrote %d bytes to %s, expected %d", n, tc.filename, len(tc.content))
			}
			f.Close()

			// Verify file exists and content is correct
			readF, err := mfs.Open(tc.filename)
			if err != nil {
				t.Fatalf("Failed to open %s for reading: %v", tc.filename, err)
			}
			defer readF.Close()

			readContent := make([]byte, len(tc.content))
			readN, err := readF.Read(readContent)
			if err != nil && err != io.EOF {
				t.Fatalf("Failed to read %s: %v", tc.filename, err)
			}

			if readN != len(tc.content) {
				t.Errorf("Read %d bytes from %s, expected %d", readN, tc.filename, len(tc.content))
			}

			if string(readContent[:readN]) != tc.content {
				t.Errorf("Content mismatch in %s: got %q, expected %q", tc.filename, string(readContent[:readN]), tc.content)
			}
		})
	}
}

// TestFileRemoval tests file deletion through the filesystem interface
func TestFileRemoval(t *testing.T) {
	tempDir := t.TempDir()
	mfs := newTestMutableFS(tempDir)

	connector, err := duckfs.Open("test.db", func(driver.ExecerContext) error { return nil }, mfs)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()

	// Create several files
	testFiles := []string{"file1.txt", "file2.csv", "file3.json"}

	for _, filename := range testFiles {
		f, err := mfs.Create(filename)
		if err != nil {
			t.Fatalf("Failed to create %s: %v", filename, err)
		}

		w, ok := f.(io.Writer)
		if !ok {
			t.Fatalf("File %s does not implement io.Writer", filename)
		}

		content := fmt.Sprintf("Content of %s", filename)
		_, err = w.Write([]byte(content))
		if err != nil {
			t.Fatalf("Failed to write to %s: %v", filename, err)
		}
		f.Close()

		// Verify file exists
		_, err = mfs.Open(filename)
		if err != nil {
			t.Fatalf("File %s should exist after creation: %v", filename, err)
		}
	}

	// Remove files one by one
	for i, filename := range testFiles {
		err := mfs.Remove(filename)
		if err != nil {
			t.Fatalf("Failed to remove %s: %v", filename, err)
		}

		// Verify removed file no longer exists
		_, err = mfs.Open(filename)
		if err == nil {
			t.Errorf("File %s should not exist after removal", filename)
		}

		// Verify remaining files still exist
		for j := i + 1; j < len(testFiles); j++ {
			_, err = mfs.Open(testFiles[j])
			if err != nil {
				t.Errorf("File %s should still exist after removing %s: %v", testFiles[j], filename, err)
			}
		}
	}
}

// TestFileOperationsIntegration tests a complete workflow of create, write, read, and delete
func TestFileOperationsIntegration(t *testing.T) {
	tempDir := t.TempDir()
	mfs := newTestMutableFS(tempDir)

	c, err := duckdb.NewConnector("", func(driver.ExecerContext) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	connector, err := duckfs.New(c, mfs)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()

	db := sql.OpenDB(connector)
	defer db.Close()

	// Step 1: Create a JSON file with sample data
	jsonData := `[
		{"id": 1, "name": "Alice", "score": 95.5},
		{"id": 2, "name": "Bob", "score": 87.2},
		{"id": 3, "name": "Charlie", "score": 92.8}
	]`

	f, err := mfs.Create("users.json")
	if err != nil {
		t.Fatalf("Failed to create JSON file: %v", err)
	}

	w, ok := f.(io.Writer)
	if !ok {
		t.Fatal("Created file does not implement io.Writer")
	}

	_, err = w.Write([]byte(jsonData))
	if err != nil {
		t.Fatalf("Failed to write JSON data: %v", err)
	}
	f.Close()

	// Step 2: Read and verify the JSON file through DuckDB
	rows, err := db.Query("SELECT id, name, score FROM read_json('users.json')")
	if err != nil {
		t.Fatalf("Failed to read JSON file through DuckDB: %v", err)
	}
	defer rows.Close()

	userCount := 0
	for rows.Next() {
		var id int
		var name string
		var score float64

		if err := rows.Scan(&id, &name, &score); err != nil {
			t.Fatalf("Failed to scan JSON row: %v", err)
		}

		userCount++

		// Verify data integrity
		switch id {
		case 1:
			if name != "Alice" || score != 95.5 {
				t.Errorf("User 1 data incorrect: name=%s, score=%f", name, score)
			}
		case 2:
			if name != "Bob" || score != 87.2 {
				t.Errorf("User 2 data incorrect: name=%s, score=%f", name, score)
			}
		case 3:
			if name != "Charlie" || score != 92.8 {
				t.Errorf("User 3 data incorrect: name=%s, score=%f", name, score)
			}
		default:
			t.Errorf("Unexpected user ID: %d", id)
		}
	}

	if userCount != 3 {
		t.Errorf("Expected 3 users, got %d", userCount)
	}

	// Step 3: Create a modified version of the file
	modifiedData := `[
		{"id": 1, "name": "Alice", "score": 98.0},
		{"id": 4, "name": "Diana", "score": 94.5}
	]`

	f2, err := mfs.Create("users_modified.json")
	if err != nil {
		t.Fatalf("Failed to create modified JSON file: %v", err)
	}

	w2, ok := f2.(io.Writer)
	if !ok {
		t.Fatal("Created modified file does not implement io.Writer")
	}

	_, err = w2.Write([]byte(modifiedData))
	if err != nil {
		t.Fatalf("Failed to write modified JSON data: %v", err)
	}
	f2.Close()

	// Step 4: Verify both files exist
	_, err = mfs.Open("users.json")
	if err != nil {
		t.Errorf("Original file should still exist: %v", err)
	}

	_, err = mfs.Open("users_modified.json")
	if err != nil {
		t.Errorf("Modified file should exist: %v", err)
	}

	// Step 5: Remove the original file
	err = mfs.Remove("users.json")
	if err != nil {
		t.Fatalf("Failed to remove original file: %v", err)
	}

	// Step 6: Verify original file is gone but modified file remains
	_, err = mfs.Open("users.json")
	if err == nil {
		t.Error("Original file should not exist after removal")
	}

	_, err = mfs.Open("users_modified.json")
	if err != nil {
		t.Errorf("Modified file should still exist: %v", err)
	}

	// Step 7: Clean up by removing the modified file
	err = mfs.Remove("users_modified.json")
	if err != nil {
		t.Fatalf("Failed to remove modified file: %v", err)
	}

	_, err = mfs.Open("users_modified.json")
	if err == nil {
		t.Error("Modified file should not exist after removal")
	}
}

// TestErrorHandling tests error conditions for mutable filesystem operations
func TestErrorHandling(t *testing.T) {
	tempDir := t.TempDir()
	mfs := newTestMutableFS(tempDir)

	connector, err := duckfs.Open("test.db", func(driver.ExecerContext) error { return nil }, mfs)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()

	// Test 1: Try to remove a non-existent file
	err = mfs.Remove("nonexistent.txt")
	if err == nil {
		t.Error("Removing non-existent file should return an error")
	}

	// Test 2: Try to open a non-existent file
	_, err = mfs.Open("nonexistent.txt")
	if err == nil {
		t.Error("Opening non-existent file should return an error")
	}

	// Test 3: Create a file, then try to create it again (should work - overwrite)
	f1, err := mfs.Create("duplicate.txt")
	if err != nil {
		t.Fatalf("Failed to create file first time: %v", err)
	}
	f1.Close()

	f2, err := mfs.Create("duplicate.txt")
	if err != nil {
		t.Fatalf("Failed to create file second time (overwrite): %v", err)
	}
	f2.Close()

	// Verify the file exists
	_, err = mfs.Open("duplicate.txt")
	if err != nil {
		t.Errorf("File should exist after creation: %v", err)
	}
}

// TestDuckDBCreateTableBackend tests CREATE TABLE operations using our
// filesystem as backend.
func TestDuckDBCreateTableBackend(t *testing.T) {
	tempDir := t.TempDir()
	mfs := newTestMutableFS(tempDir)

	// Use duckfs.Open to create a DuckDB instance with our virtual filesystem
	dbPath := "test_database.db"
	connector, err := duckfs.Open(dbPath, func(driver.ExecerContext) error { return nil }, mfs)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()

	db := sql.OpenDB(connector)
	defer db.Close()

	// Verify filesystem starts empty
	verifyFilesystemEmpty(t, tempDir, "at test start")

	// Test 1: Create a table and verify it can store and retrieve data
	_, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			name VARCHAR(100),
			email VARCHAR(255),
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}

	// Verify filesystem may still be empty after table creation (until data is inserted)
	// Some databases only create files when data is actually written

	// Test 2: Insert data into the table
	_, err = db.Exec(`
		INSERT INTO users (id, name, email) VALUES 
		(1, 'Alice Johnson', 'alice@example.com'),
		(2, 'Bob Smith', 'bob@example.com'),
		(3, 'Charlie Brown', 'charlie@example.com')
	`)
	if err != nil {
		t.Fatalf("Failed to insert data: %v", err)
	}

	// Verify filesystem now contains files after data insertion
	filesAfterInsert := verifyFilesystemNotEmpty(t, tempDir, "after data insertion")
	t.Logf("Files created after data insertion: %v", filesAfterInsert)

	// Test 3: Query the data back to ensure it was stored correctly
	rows, err := db.Query("SELECT id, name, email FROM users ORDER BY id")
	if err != nil {
		t.Fatalf("Failed to query users table: %v", err)
	}
	defer rows.Close()

	expectedUsers := []struct {
		ID    int
		Name  string
		Email string
	}{
		{1, "Alice Johnson", "alice@example.com"},
		{2, "Bob Smith", "bob@example.com"},
		{3, "Charlie Brown", "charlie@example.com"},
	}

	userCount := 0
	for rows.Next() {
		var id int
		var name, email string
		if err := rows.Scan(&id, &name, &email); err != nil {
			t.Fatalf("Failed to scan user row: %v", err)
		}

		if userCount >= len(expectedUsers) {
			t.Fatalf("Got more users than expected")
		}

		expected := expectedUsers[userCount]
		if id != expected.ID || name != expected.Name || email != expected.Email {
			t.Errorf("User %d mismatch: got (id=%d, name=%s, email=%s), expected (id=%d, name=%s, email=%s)",
				userCount, id, name, email, expected.ID, expected.Name, expected.Email)
		}
		userCount++
	}

	if userCount != len(expectedUsers) {
		t.Errorf("Expected %d users, got %d", len(expectedUsers), userCount)
	}

	// Test 4: Verify table exists in schema
	var tableCount int
	err = db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'users'").Scan(&tableCount)
	if err != nil {
		t.Fatalf("Failed to check table existence: %v", err)
	}
	if tableCount != 1 {
		t.Errorf("Expected 1 users table, found %d", tableCount)
	}
}

// TestDuckDBDropTableBackend tests DROP TABLE operations using our filesystem as backend
func TestDuckDBDropTableBackend(t *testing.T) {
	tempDir := t.TempDir()
	mfs := newTestMutableFS(tempDir)

	// Use duckfs.Open to create a DuckDB instance with our virtual filesystem
	dbPath := "test_database.db"
	connector, err := duckfs.Open(dbPath, func(driver.ExecerContext) error { return nil }, mfs)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()

	db := sql.OpenDB(connector)
	defer db.Close()

	// Verify filesystem starts empty
	verifyFilesystemEmpty(t, tempDir, "at test start")

	// Test 1: Create multiple tables
	tables := []string{"products", "orders", "customers"}

	for _, tableName := range tables {
		createSQL := fmt.Sprintf(`
			CREATE TABLE %s (
				id INTEGER PRIMARY KEY,
				name VARCHAR(100),
				data TEXT
			)
		`, tableName)

		_, err = db.Exec(createSQL)
		if err != nil {
			t.Fatalf("Failed to create table %s: %v", tableName, err)
		}

		// Insert some data to make sure table is functional
		insertSQL := fmt.Sprintf("INSERT INTO %s (id, name, data) VALUES (1, 'Test %s', 'Sample data')", tableName, tableName)
		_, err = db.Exec(insertSQL)
		if err != nil {
			t.Fatalf("Failed to insert data into table %s: %v", tableName, err)
		}
	}

	// Verify filesystem contains files after table creation and data insertion
	filesAfterCreation := verifyFilesystemNotEmpty(t, tempDir, "after all tables created and populated")
	t.Logf("Files created for tables: %v", filesAfterCreation)

	// Test 2: Verify all tables exist
	for _, tableName := range tables {
		var tableCount int
		err = db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?", tableName).Scan(&tableCount)
		if err != nil {
			t.Fatalf("Failed to check existence of table %s: %v", tableName, err)
		}
		if tableCount != 1 {
			t.Errorf("Expected table %s to exist, but found %d instances", tableName, tableCount)
		}
	}

	// Test 3: Drop tables one by one and verify they're removed
	for i, tableName := range tables {
		// Drop the table
		dropSQL := fmt.Sprintf("DROP TABLE %s", tableName)
		_, err = db.Exec(dropSQL)
		if err != nil {
			t.Fatalf("Failed to drop table %s: %v", tableName, err)
		}

		// Verify the dropped table no longer exists
		var tableCount int
		err = db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?", tableName).Scan(&tableCount)
		if err != nil {
			t.Fatalf("Failed to check absence of table %s: %v", tableName, err)
		}
		if tableCount != 0 {
			t.Errorf("Expected table %s to be dropped, but found %d instances", tableName, tableCount)
		}

		// Check filesystem state after each drop
		remainingFiles, err := listFilesystemContents(tempDir)
		if err != nil {
			t.Fatalf("Failed to check filesystem after dropping %s: %v", tableName, err)
		}
		t.Logf("Files remaining after dropping %s: %v", tableName, remainingFiles)

		// Verify remaining tables still exist
		for j := i + 1; j < len(tables); j++ {
			remainingTable := tables[j]
			err = db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?", remainingTable).Scan(&tableCount)
			if err != nil {
				t.Fatalf("Failed to check existence of remaining table %s: %v", remainingTable, err)
			}
			if tableCount != 1 {
				t.Errorf("Expected remaining table %s to still exist after dropping %s, but found %d instances",
					remainingTable, tableName, tableCount)
			}
		}
	}

	// Test 4: Verify no tables remain and filesystem is clean
	var totalTables int
	err = db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'main'").Scan(&totalTables)
	if err != nil {
		t.Fatalf("Failed to count remaining tables: %v", err)
	}
	if totalTables != 0 {
		t.Errorf("Expected no tables to remain, but found %d", totalTables)
	}

	// Verify filesystem is empty after all tables are dropped
	verifyFilesystemEmpty(t, tempDir, "after all tables dropped")
}

// TestDuckDBTableBackendFilesystemVerification specifically tests filesystem state during table lifecycle
func TestDuckDBTableBackendFilesystemVerification(t *testing.T) {
	tempDir := t.TempDir()
	mfs := newTestMutableFS(tempDir)

	// Use duckfs.Open to create a DuckDB instance with our virtual filesystem
	dbPath := "test_database.db"
	connector, err := duckfs.Open(dbPath, func(driver.ExecerContext) error { return nil }, mfs)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()

	db := sql.OpenDB(connector)
	defer db.Close()

	// Step 1: Verify filesystem starts completely empty
	t.Log("=== Step 1: Initial state verification ===")
	verifyFilesystemEmpty(t, tempDir, "at test start")

	// Step 2: Create table and verify filesystem state
	t.Log("=== Step 2: Create table ===")
	_, err = db.Exec(`
		CREATE TABLE test_table (
			id INTEGER PRIMARY KEY,
			name TEXT,
			value REAL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	// Check if files are created immediately after table creation
	filesAfterCreate, err := listFilesystemContents(tempDir)
	if err != nil {
		t.Fatalf("Failed to list files after table creation: %v", err)
	}
	t.Logf("Files after CREATE TABLE: %v", filesAfterCreate)

	// Step 3: Insert data and verify filesystem has files
	t.Log("=== Step 3: Insert data and verify filesystem has files ===")
	_, err = db.Exec(`
		INSERT INTO test_table (id, name, value) VALUES 
		(1, 'test_row_1', 42.5),
		(2, 'test_row_2', 84.0),
		(3, 'test_row_3', 126.5)
	`)
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	// Verify filesystem now definitely contains files
	filesAfterInsert := verifyFilesystemNotEmpty(t, tempDir, "after data insertion")
	t.Logf("Files after INSERT: %v", filesAfterInsert)

	// Step 4: Verify we can read the data back
	t.Log("=== Step 4: Verify data can be read back ===")
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM test_table").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count rows: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 rows, got %d", count)
	}

	// Step 5: Drop table and verify filesystem is cleaned up
	t.Log("=== Step 5: Drop table and verify cleanup ===")
	_, err = db.Exec("DROP TABLE test_table")
	if err != nil {
		t.Fatalf("Failed to drop test table: %v", err)
	}

	// Verify table no longer exists in schema
	var tableCount int
	err = db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'test_table'").Scan(&tableCount)
	if err != nil {
		t.Fatalf("Failed to check table existence after drop: %v", err)
	}
	if tableCount != 0 {
		t.Errorf("Expected test_table to be dropped, but found %d instances", tableCount)
	}

	// Step 6: Verify filesystem is completely empty again
	t.Log("=== Step 6: Verify filesystem is completely clean ===")
	verifyFilesystemEmpty(t, tempDir, "after DROP TABLE")

	t.Log("=== Test completed: Full lifecycle verified ===")
	t.Log("✓ Filesystem empty at start")
	t.Log("✓ Files created when data inserted")
	t.Log("✓ Data correctly stored and retrievable")
	t.Log("✓ Files removed when table dropped")
	t.Log("✓ Filesystem empty at end")
}

// TestMutableFSTruncate tests the Truncate functionality specifically
func TestMutableFSTruncate(t *testing.T) {
	tempDir := t.TempDir()
	mfs := newTestMutableFS(tempDir)

	// Test that the mutable filesystem satisfies the MutableFS interface
	var _ duckfs.MutableFS = mfs

	// Create a file with some content
	f, err := mfs.Create("truncate_test.txt")
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	// Write initial data
	originalData := []byte("This is a test file with some content that will be truncated.")
	w, ok := f.(io.Writer)
	if !ok {
		t.Fatal("Created file does not implement io.Writer")
	}

	n, err := w.Write(originalData)
	if err != nil {
		t.Fatalf("Failed to write original data: %v", err)
	}
	if n != len(originalData) {
		t.Errorf("Wrote %d bytes, expected %d", n, len(originalData))
	}
	f.Close()

	// Verify original file size
	originalStat, err := os.Stat(filepath.Join(tempDir, "truncate_test.txt"))
	if err != nil {
		t.Fatalf("Failed to stat original file: %v", err)
	}
	originalSize := originalStat.Size()
	if originalSize != int64(len(originalData)) {
		t.Errorf("Original file size %d, expected %d", originalSize, len(originalData))
	}

	// Test truncating to smaller size
	newSize := int64(20)
	err = mfs.Truncate("truncate_test.txt", newSize)
	if err != nil {
		t.Fatalf("Failed to truncate file: %v", err)
	}

	// Verify truncated file size
	truncatedStat, err := os.Stat(filepath.Join(tempDir, "truncate_test.txt"))
	if err != nil {
		t.Fatalf("Failed to stat truncated file: %v", err)
	}
	if truncatedStat.Size() != newSize {
		t.Errorf("Truncated file size %d, expected %d", truncatedStat.Size(), newSize)
	}

	// Verify truncated content
	readFile, err := mfs.Open("truncate_test.txt")
	if err != nil {
		t.Fatalf("Failed to open truncated file: %v", err)
	}
	defer readFile.Close()

	readData := make([]byte, newSize)
	readN, err := readFile.Read(readData)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read truncated file: %v", err)
	}

	if int64(readN) != newSize {
		t.Errorf("Read %d bytes from truncated file, expected %d", readN, newSize)
	}

	expectedContent := string(originalData[:newSize])
	actualContent := string(readData[:readN])
	if actualContent != expectedContent {
		t.Errorf("Truncated content %q, expected %q", actualContent, expectedContent)
	}

	// Test truncating to zero (empty file)
	err = mfs.Truncate("truncate_test.txt", 0)
	if err != nil {
		t.Fatalf("Failed to truncate file to zero: %v", err)
	}

	emptyStat, err := os.Stat(filepath.Join(tempDir, "truncate_test.txt"))
	if err != nil {
		t.Fatalf("Failed to stat empty file: %v", err)
	}
	if emptyStat.Size() != 0 {
		t.Errorf("Empty file size %d, expected 0", emptyStat.Size())
	}

	// Clean up
	err = mfs.Remove("truncate_test.txt")
	if err != nil {
		t.Fatalf("Failed to remove test file: %v", err)
	}

	t.Log("✓ Truncate functionality works correctly")
	t.Log("✓ File can be truncated to smaller size")
	t.Log("✓ File content is preserved up to truncation point")
	t.Log("✓ File can be truncated to zero size")
}

// TestDuckDBTableOperationsWithFilesystem tests complete table lifecycle with filesystem verification
func TestDuckDBTableOperationsWithFilesystem(t *testing.T) {
	tempDir := t.TempDir()
	mfs := newTestMutableFS(tempDir)

	// Use duckfs.Open to create a DuckDB instance with our virtual filesystem
	dbPath := "test_database.db"
	connector, err := duckfs.Open(dbPath, func(driver.ExecerContext) error { return nil }, mfs)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()

	db := sql.OpenDB(connector)
	defer db.Close()

	// Test 1: Create a table for storing log data
	_, err = db.Exec(`
		CREATE TABLE application_logs (
			id INTEGER PRIMARY KEY,
			timestamp TIMESTAMP,
			level VARCHAR(10),
			message TEXT,
			user_id INTEGER
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create application_logs table: %v", err)
	}

	// Test 2: Insert sample log data
	logData := []struct {
		ID        int
		Timestamp string
		Level     string
		Message   string
		UserID    int
	}{
		{1, "2024-01-15 10:30:00", "INFO", "User logged in", 123},
		{2, "2024-01-15 10:31:15", "WARN", "Password change attempted", 123},
		{3, "2024-01-15 10:32:00", "ERROR", "Database connection failed", 456},
		{4, "2024-01-15 10:33:45", "INFO", "User logged out", 123},
		{5, "2024-01-15 10:35:00", "DEBUG", "Cache refreshed", 0},
	}

	for _, log := range logData {
		_, err = db.Exec(`
			INSERT INTO application_logs (id, timestamp, level, message, user_id) 
			VALUES (?, ?, ?, ?, ?)
		`, log.ID, log.Timestamp, log.Level, log.Message, log.UserID)
		if err != nil {
			t.Fatalf("Failed to insert log entry %d: %v", log.ID, err)
		}
	}

	// Test 3: Query data with different filters
	testQueries := []struct {
		name     string
		query    string
		expected int
	}{
		{
			"All logs",
			"SELECT COUNT(*) FROM application_logs",
			5,
		},
		{
			"Error logs only",
			"SELECT COUNT(*) FROM application_logs WHERE level = 'ERROR'",
			1,
		},
		{
			"User 123 logs",
			"SELECT COUNT(*) FROM application_logs WHERE user_id = 123",
			3,
		},
		{
			"Info and Warning logs",
			"SELECT COUNT(*) FROM application_logs WHERE level IN ('INFO', 'WARN')",
			3,
		},
	}

	for _, testQuery := range testQueries {
		t.Run(testQuery.name, func(t *testing.T) {
			var count int
			err = db.QueryRow(testQuery.query).Scan(&count)
			if err != nil {
				t.Fatalf("Failed to execute query '%s': %v", testQuery.query, err)
			}
			if count != testQuery.expected {
				t.Errorf("Query '%s' returned %d results, expected %d", testQuery.name, count, testQuery.expected)
			}
		})
	}

	// Test 4: Test COPY TO operation to export data to filesystem
	_, err = db.Exec("COPY application_logs TO 'exported_logs.csv' (FORMAT CSV, HEADER)")
	if err != nil {
		t.Fatalf("Failed to export logs to CSV: %v", err)
	}

	// Verify the exported file exists and can be read
	exportedFile, err := mfs.Open("exported_logs.csv")
	if err != nil {
		t.Fatalf("Failed to open exported CSV file: %v", err)
	}
	defer exportedFile.Close()

	// Read the first few lines to verify content
	buffer := make([]byte, 200)
	n, err := exportedFile.Read(buffer)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read exported CSV file: %v", err)
	}

	content := string(buffer[:n])
	if !strings.Contains(content, "id,timestamp,level,message,user_id") {
		t.Errorf("Exported CSV doesn't contain expected header")
	}
	if !strings.Contains(content, "User logged in") {
		t.Errorf("Exported CSV doesn't contain expected data")
	}

	// Test 5: Create another table and use COPY FROM to import data
	_, err = db.Exec(`
		CREATE TABLE imported_logs (
			id INTEGER,
			timestamp TIMESTAMP,
			level VARCHAR(10),
			message TEXT,
			user_id INTEGER
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create imported_logs table: %v", err)
	}

	_, err = db.Exec("COPY imported_logs FROM 'exported_logs.csv' (FORMAT CSV, HEADER)")
	if err != nil {
		t.Fatalf("Failed to import logs from CSV: %v", err)
	}

	// Verify imported data matches original
	var importedCount int
	err = db.QueryRow("SELECT COUNT(*) FROM imported_logs").Scan(&importedCount)
	if err != nil {
		t.Fatalf("Failed to count imported logs: %v", err)
	}
	if importedCount != len(logData) {
		t.Errorf("Imported %d logs, expected %d", importedCount, len(logData))
	}

	// Test 6: Drop tables and verify cleanup
	_, err = db.Exec("DROP TABLE application_logs")
	if err != nil {
		t.Fatalf("Failed to drop application_logs table: %v", err)
	}

	_, err = db.Exec("DROP TABLE imported_logs")
	if err != nil {
		t.Fatalf("Failed to drop imported_logs table: %v", err)
	}

	// Test 7: Clean up exported file
	err = mfs.Remove("exported_logs.csv")
	if err != nil {
		t.Fatalf("Failed to remove exported CSV file: %v", err)
	}

	// Verify file is gone
	_, err = mfs.Open("exported_logs.csv")
	if err == nil {
		t.Error("Exported CSV file should have been removed")
	}
}
