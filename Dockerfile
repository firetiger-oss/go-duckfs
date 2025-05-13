# ==============================================================================
# This Dockerfile showcases how to build the extension using the Pixi package
# manager, and dynmically linking against libduckdb. Dynamic linking is
# necessary because the static build process used by the Go bindings we missing
# the file system symbols that the extension depends on.
#
# https://github.com/marcboeker/go-duckdb?tab=readme-ov-file#linking-a-dynamic-library
# ==============================================================================

# ==============================================================================
# Step 0: Install libduckdb with Pixi
# -----------------------------------
#
# We use the pixi package manager for simplicity here, we have a hard dependency
# on duckdb v1.2.2 and using pixi simplifies the installation of pre-built
# binaries.
FROM ghcr.io/prefix-dev/pixi:latest AS pixi
WORKDIR /src
COPY pixi.toml pixi.lock .
RUN pixi install --locked
# ==============================================================================

# ==============================================================================
# Step 1: build the extension
# ---------------------------
FROM golang:1.24.2 AS build
WORKDIR /src
# Download the Go dependencies first so they can be kept in the docker cache.
COPY go.mod go.sum .
RUN go mod download
# Copy the files that were installed by pixi for libduckdb.
COPY --from=pixi /src/.pixi .pixi
COPY . .
# There are two necessary configuration steps here:
#
#  - The CGO_LDFLAGS environment variable instructs CGO where to find the
#    version of libduckdb that was installed by pixi.
#
#  - The -tags=duckdb_use_lib build tag instructs the Go bindings to use the
#    dynamic library instead of statically linking against libduckdb.
#
ENV CGO_LDFLAGS="-L/src/.pixi/envs/default/lib -lduckdb"
RUN go build -x -tags=duckdb_use_lib
# ==============================================================================

# ==============================================================================
# Step 2: test the extension
# --------------------------
FROM build AS test
# When exectuing tests, the test program is dynamically linked against
# libduckdb, but we need to instruct the linker where to find it, which is why
# we set the LD_LIBRARY_PATH environment variable to the location where pixi has
# installed libduckdb.
ENV LD_LIBRARY_PATH=/src/.pixi/envs/default/lib
RUN go test -x -tags=duckdb_use_lib
# ==============================================================================
