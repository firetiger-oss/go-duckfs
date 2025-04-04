package duckfs_test

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"os"
	"testing"

	"github.com/firetiger-inc/go-duckfs"
	"github.com/marcboeker/go-duckdb"
)

func Example() {
	c, err := duckdb.NewConnector("", func(driver.ExecerContext) error { return nil })
	if err != nil {
		log.Fatal(err)
	}

	if err := duckfs.Register(c, os.DirFS("testdata")); err != nil {
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

func TestRegisterOverride(t *testing.T) {
	c, err := duckdb.NewConnector("", func(driver.ExecerContext) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	if err := duckfs.Register(c, os.DirFS("testdata/folder-1")); err != nil {
		t.Fatal(err)
	}
	if err := duckfs.Register(c, os.DirFS("testdata/folder-2")); err != nil {
		t.Fatal(err)
	}

	db := sql.OpenDB(c)
	defer db.Close()

	var msg string
	if err := db.QueryRow(`SELECT message from read_csv('database.csv')`).Scan(&msg); err != nil {
		t.Fatal(err)
	}

	if msg != "world" {
		t.Errorf("virtual file system override did not work: %q", msg)
	}
}
