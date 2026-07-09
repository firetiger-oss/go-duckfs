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
- `duckdb/v1.5.4/src/include/` - Vendored DuckDB headers
- `pixi.toml` - DuckDB version configuration (libduckdb == 1.5.4)
- `testdata/` - Embedded test data files

## Upgrading DuckDB

To upgrade to a new DuckDB release `vX.Y.Z` (example below uses `v1.5.4`):

1. Create a branch named after the version: `git checkout -b v1.5.4`

2. Vendor the headers for the new tag. Previous versions' header directories
   are kept in the tree, they are not deleted.

   ```bash
   git clone --depth 1 --branch v1.5.4 https://github.com/duckdb/duckdb.git /tmp/duckdb-src
   mkdir -p duckdb/v1.5.4/src/include
   cp -r /tmp/duckdb-src/src/include/* duckdb/v1.5.4/src/include/
   rm -rf /tmp/duckdb-src
   ```

3. Update the version references:

   | File | Change |
   |------|--------|
   | `pixi.toml` | `libduckdb = "==1.5.4"` |
   | `duckfs.go` | `#cgo CFLAGS` and `#cgo CXXFLAGS` include paths |
   | `Dockerfile` | Version in the Step 0 comment |
   | `CLAUDE.md` | Version in "Key Files" |

4. Update the Go bindings. The `duckdb-go/v2` version encodes the DuckDB
   version: DuckDB `1.5.4` maps to `v2.10504.0`, i.e. `major.minor` and `patch`
   are zero-padded into `1MMPP`. The `duckdb-go/mapping` module has its own
   `v0.0.x` series; bump it only if a newer non-preview release exists.

   ```bash
   go get github.com/duckdb/duckdb-go/v2@v2.10504.0
   go mod tidy
   ```

5. Refresh `pixi.lock` with `pixi install` (drop `--locked`, which forbids
   lockfile updates).

6. Build, vet, and test against the pixi-installed library. `duckdb_use_lib`
   links the shared library instead of the bindings' bundled static one, so it
   must be able to find it at both link and run time:

   ```bash
   export CGO_ENABLED=1
   export CGO_LDFLAGS="-L$PWD/.pixi/envs/default/lib -lduckdb -Wl,-rpath,$PWD/.pixi/envs/default/lib"
   go build -tags=duckdb_use_lib ./...
   go vet -tags=duckdb_use_lib ./...
   go test -tags=duckdb_use_lib -race ./...
   ```

   The linker warns about a duplicate `-rpath` and a duplicate `-lduckdb`
   because the build tag adds its own flags. The warnings are harmless.

7. Commit, push, and open a PR against `main`:

   ```bash
   git add duckdb/v1.5.4 pixi.toml pixi.lock duckfs.go go.mod go.sum Dockerfile CLAUDE.md
   git commit -m "Update DuckDB to v1.5.4"
   git push -u origin v1.5.4
   gh pr create --base main --title "Update DuckDB to v1.5.4"
   ```

## Public API

- `Open(dsn, connInitFn, fsys)` - Create connector with owned DuckDB instance
- `New(connector, fsys)` - Wrap existing DuckDB connector
- `Connector` - Implements `driver.Connector`; must be closed to release resources

## Code Conventions

- Virtual paths use `://` protocol prefix (e.g., `test://file.parquet`)
- Absolute paths bypass virtual filesystem, use OS directly
- CGO exports prefixed with `duckfs_` for C++ bridge callbacks
- Global file maps (`globalFsys`, `globalFiles`) track registered filesystems and open file handles
