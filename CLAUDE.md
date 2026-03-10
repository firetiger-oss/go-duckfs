# go-duckfs

Go package implementing a DuckDB virtual file system interface using Go's `io/fs` abstraction. Enables mounting custom filesystems (including cloud-backed) as DuckDB's virtual file system backend.

## Build & Test

Requires Go 1.24.0+ and pixi package manager.

```bash
# Install DuckDB library
pixi install --locked

# Set environment for CGO
export CGO_ENABLED=1
export CGO_LDFLAGS="-L.pixi/envs/default/lib"
export LD_LIBRARY_PATH=".pixi/envs/default/lib"
export GOFLAGS="-tags=duckdb_use_lib"

# Run tests (with race detector)
go test -v -race ./...

# Build
go build ./...

# Lint
go vet ./...
```

## Key Files

- `duckfs.go` - Main implementation with CGO exports and public API
- `gofs_extension.cpp` / `gofs_extension.hpp` - C++ DuckDB filesystem bridge
- `duckdb/v1.5.0/src/include/` - Vendored DuckDB headers
- `pixi.toml` - DuckDB version configuration (libduckdb == 1.5.0)
- `testdata/` - Embedded test data files

## Public API

- `Open(dsn, connInitFn, fsys)` - Create connector with owned DuckDB instance
- `New(connector, fsys)` - Wrap existing DuckDB connector
- `Connector` - Implements `driver.Connector`; must be closed to release resources

## Code Conventions

- Virtual paths use `://` protocol prefix (e.g., `test://file.parquet`)
- Absolute paths bypass virtual filesystem, use OS directly
- CGO exports prefixed with `duckfs_` for C++ bridge callbacks
- Global file maps (`globalFsys`, `globalFiles`) track registered filesystems and open file handles
