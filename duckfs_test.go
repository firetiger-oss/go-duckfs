package duckfs_test

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io/fs"
	"log"
	"os"
	"testing"

	"github.com/firetiger-inc/go-duckfs"
	"github.com/marcboeker/go-duckdb/v2"
)

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
	c, err := duckdb.NewConnector("", func(driver.ExecerContext) error { return nil })
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
