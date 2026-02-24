# go-duckfs

<img src="assets/logo.png" alt="go-duckfs logo" width="200">

[![Go Reference](https://pkg.go.dev/badge/github.com/firetiger-oss/go-duckfs.svg)](https://pkg.go.dev/github.com/firetiger-oss/go-duckfs)

DuckDB virtual file system based on io/fs

## Motivation

The purpose of this package is to allow Go programs to mount `io/fs` read-only
file systems as backend for DuckDB databases. It guarantees that all I/O will be
executed by the Go runtime instead of being performed directly by DuckDB, which
is often desirable to integrate with instrumentation, caching layers, or network
clients for cloud storage.

## Building

The package requires C++ symbols that are not present in the go-duckdb static
build. The recommended way to install DuckDB is via [pixi](https://pixi.sh):

```bash
# Install DuckDB library
pixi install --locked

# Build with dynamic linking
CGO_ENABLED=1 \
CGO_LDFLAGS="-L.pixi/envs/default/lib" \
go build -tags=duckdb_use_lib
```

The Go program must be compiled using the `duckdb_use_lib` tag to select the
version of the DuckDB Go bindings suited for dynamic linking.

See also: https://github.com/duckdb/duckdb-go?tab=readme-ov-file#linking-a-dynamic-library

## Testing

Since `go test` builds the program from sources, it is necessary to set the
CGO environment variables and build tags:

```bash
CGO_ENABLED=1 \
CGO_LDFLAGS="-L.pixi/envs/default/lib" \
LD_LIBRARY_PATH=".pixi/envs/default/lib" \
go test -v ./... -tags=duckdb_use_lib
```

## Usage

The package exposes functions to create connectors for DuckDB instances with
a `fs.FS` as virtual file system, which can then be used to create a `sql.DB`.

### Basic Example

```go
c, err := duckfs.Open("", nil, os.DirFS("testdata"))
if err != nil {
	log.Fatal(err)
}

db := sql.OpenDB(c)
defer db.Close()

// Query files using DuckDB's read functions
rows, err := db.Query(`SELECT * FROM read_parquet('data.parquet')`)
```

### Protocol-Aware Filesystem

Virtual paths use a protocol prefix (e.g., `test://file.parquet`). To handle
these paths, wrap your `fs.FS` to strip the protocol prefix:

```go
type myFS struct {
	fsys fs.FS
}

func (f *myFS) Open(name string) (fs.File, error) {
	// Strip protocol prefix if present
	name = strings.TrimPrefix(name, "myproto://")
	return f.fsys.Open(name)
}
```

Then use it with DuckDB queries:

```go
c, err := duckfs.Open("", nil, &myFS{fsys: os.DirFS("data")})
if err != nil {
	log.Fatal(err)
}

db := sql.OpenDB(c)
defer db.Close()

// Query using protocol prefix
row := db.QueryRow(`SELECT * FROM read_csv('myproto://records.csv')`)
```

### API Patterns

The package provides two functions for creating connectors:

- **`duckfs.Open(dsn, connInitFn, fsys)`** - Creates a new DuckDB instance with
  the given `fs.FS`. The connector owns the DuckDB instance and must be closed
  to release resources.

- **`duckfs.New(connector, fsys)`** - Wraps an existing `duckdb.Connector` with
  a virtual filesystem. Useful when you need to share a DuckDB instance or swap
  filesystems dynamically.
