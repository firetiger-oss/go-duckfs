# duckdb-gofs
DuckDB virtual file system based on io/fs

## Motivation
The purpose of this package is to allow Go programs to mount `io/fs` read-only
file systems as backend for DuckDB databases. It garantees that all I/O will be
executed by the Go runtime instead of being performed directly by DuckDB, which
is often desirable to integrate with instrumentation, caching layers, or network
clients for cloud storage.

## Building
The package requires C++ symbols that are not present in the go-duckdb static
buildd. If you installed DuckDB on a local workstation, the simplest way to
build this package is to use a dynamically linked version of DuckDB installed
on the workstation.

For example, when installing LLVM and DuckDB with Homebrew on MacOS:
```
export CGO_LDFLAGS=-L/opt/homebrew/opt/llvm/lib/c++ -L/opt/homebrew/opt/llvm/lib/unwind -L/opt/homebrew/lib -lunwind
export CGO_CPPFLAGS=-I/opt/homebrew/opt/llvm/include -I/opt/homebrew/include
```

Then the program must be passed a build tag to dynamically link against DuckDB:
```
go test -tags=duckdb_use_lib
```

## Testing
Since `go test` builds the program from sources, it is necessary to also set
build tags to dynamically link the test program against DuckDB:

```
go test -tags=duckdb_use_lib
```

## Usage
The library has a single function to register the Go virtual file system on
a DuckDB connector. Programs must create the connector independently before
constructing a `database/sql` connection to install the file system, for
example:

```go
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

...
```
