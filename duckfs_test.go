package duckfs_test

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"os"

	"github.com/achille-roussel/sqlrange"
	duckfs "github.com/firetiger-oss/duckdb-gofs"
	"github.com/marcboeker/go-duckdb"
)

func Example() {
	c, err := duckdb.NewConnector("", func(driver.ExecerContext) error { return nil })
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	fs, err := duckfs.Register(c, os.DirFS("testdata"))
	if err != nil {
		log.Fatal(err)
	}
	defer fs.Close()

	db := sql.OpenDB(c)
	defer db.Close()

	type Row struct {
		Timestamp      int64  `sql:"timestamp"`
		ChangeID       int64  `sql:"change_id"`
		InstrumentName string `sql:"instrument_name"`
	}

	for r, err := range sqlrange.Query[Row](db,
		`SELECT timestamp, change_id, instrument_name, FROM read_parquet('example.parquet')`,
	) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%+v\n", r)
	}
}
