# go-duckfs

DuckDB virtual file system based on io/fs

## Motivation

The purpose of this package is to allow Go programs to mount `io/fs` read-only
file systems as backend for DuckDB databases. It garantees that all I/O will be
executed by the Go runtime instead of being performed directly by DuckDB, which
is often desirable to integrate with instrumentation, caching layers, or network
clients for cloud storage.

## Building

The package requires C++ symbols that are not present in the go-duckdb static
build. If you installed DuckDB on a local workstation, the simplest way to
build this package is to use a dynamically linked version of DuckDB installed
on the workstation.

Then the Go program must be compiled using the `duckdb_us_lib` tag to select the
version of the DuckDB Go bindings suited for dynamic linking, for example:
```
go test -tags=duckdb_use_lib
```

https://github.com/duckdb/duckdb-go?tab=readme-ov-file#linking-a-dynamic-library

## Testing

Since `go test` builds the program from sources, it is necessary to also set
build tags to dynamically link the test program against DuckDB:

```
go test -tags=duckdb_use_lib
```

## Usage

The package exposes functions to create connectors for DuckDB instances with
a `fs.FS` as virtual file system, which can then be used to create a `sql.DB`,
for example:

```go
c, err := duckfs.Open("", nil, os.DirFS("testdata"))
if err != nil {
	log.Fatal(err)
}

db := sql.OpenDB(c)
defer db.Close()

...
```
