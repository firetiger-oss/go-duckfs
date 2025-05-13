// Package duckfs implements a DuckDB filesystem interface using the Go
// standard library's filesystem interface (io/fs).
package duckfs

// #cgo CFLAGS:   -I${SRCDIR}/duckdb/v1.2.2/src/include
// #cgo CXXFLAGS: -I${SRCDIR}/duckdb/v1.2.2/src/include -std=c++17
// #include <gofs_extension.hpp>
import "C"

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sync"
	"unsafe"

	"github.com/marcboeker/go-duckdb/v2"
)

type filemap[T comparable] struct {
	mutex sync.RWMutex
	files []T
}

func (m *filemap[T]) lookup(id int32) (T, bool) {
	var zero T

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if id < 0 || int(id) >= len(m.files) || m.files[id] == zero {
		return zero, false
	}

	return m.files[id], true
}

func (m *filemap[T]) register(f T) int32 {
	var zero T

	m.mutex.Lock()
	defer m.mutex.Unlock()

	var i int
	for i < len(m.files) && m.files[i] != zero {
		i++
	}

	if i == len(m.files) {
		m.files = append(m.files, f)
	} else {
		m.files[i] = f
	}

	return int32(i)
}

func (m *filemap[T]) unregister(id int32) (T, bool) {
	var zero T

	m.mutex.Lock()
	defer m.mutex.Unlock()

	if id < 0 || int(id) >= len(m.files) || m.files[id] == zero {
		return zero, false
	}

	f := m.files[id]
	m.files[id] = zero
	return f, true
}

var (
	globalFsys  filemap[fs.FS]
	globalFiles filemap[fs.File]
)

//export duckfs_file_exists
func duckfs_file_exists(id C.int, path *C.char) C.int {
	fsys, ok := globalFsys.lookup(int32(id))
	if !ok {
		return 0
	}
	_, err := fs.Stat(fsys, C.GoString(path))
	if err != nil {
		return 0
	}
	return 1
}

//export duckfs_file_open
func duckfs_file_open(id C.int, path *C.char) C.int {
	fsys, ok := globalFsys.lookup(int32(id))
	if !ok {
		slog.Warn("duckfs_file_open: file system not found", "filesystem", id, "path", C.GoString(path))
		return -1
	}
	f, err := fsys.Open(C.GoString(path))
	if err != nil {
		return -1
	}
	return C.int(globalFiles.register(f))
}

//export duckfs_file_close
func duckfs_file_close(id C.int) C.int {
	f, ok := globalFiles.unregister(int32(id))
	if !ok {
		slog.Warn("duckfs_file_close: file not found", "file", id)
		return -1
	}
	f.Close()
	return 0
}

//export duckfs_file_size
func duckfs_file_size(id C.int) C.int64_t {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		slog.Warn("duckfs_file_size: file not found", "file", id)
		return -1
	}
	s, err := f.Stat()
	if err != nil {
		return -1
	}
	return C.int64_t(s.Size())
}

//export duckfs_file_read_at
func duckfs_file_read_at(id C.int, buf unsafe.Pointer, size, off C.int64_t) C.int64_t {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		slog.Warn("duckfs_file_read_at: file not found", "file", id)
		return -1
	}
	r, ok := f.(io.ReaderAt)
	if !ok {
		slog.Warn("duckfs_file_read_at: file does not support io.ReaderAt", "file", id)
		return -1
	}
	buffer := unsafe.Slice((*byte)(buf), size)
	n, err := r.ReadAt(buffer, int64(off))
	if err != nil && n == 0 {
		return -1
	}
	return C.int64_t(n)
}

//export duckfs_file_read
func duckfs_file_read(id C.int, buf unsafe.Pointer, size C.int64_t) C.int64_t {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		slog.Warn("duckfs_file_read: file not found", "file", id)
		return -1
	}
	buffer := unsafe.Slice((*byte)(buf), size)
	n, err := f.Read(buffer)
	if n > 0 {
		return C.int64_t(n)
	}
	if errors.Is(err, io.EOF) && n == 0 {
		return 0
	}
	return -1
}

//export duckfs_file_seek
func duckfs_file_seek(id C.int, off C.int64_t, whence int) C.int64_t {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		slog.Warn("duckfs_file_seek: file not found", "file", id)
		return -1
	}
	r, ok := f.(io.Seeker)
	if !ok {
		slog.Warn("duckfs_file_seek: file does not support io.Seeker", "file", id)
		return -1
	}
	s, err := r.Seek(int64(off), whence)
	if err != nil {
		return -1
	}
	return C.int64_t(s)
}

// Connector is a type similar to duckdb.Connector, but it manages the
// lifecycle of a virtual filesystem installed on the underlying DuckDB
// database.
//
// https://pkg.go.dev/github.com/marcboeker/go-duckdb/v2#Connector
type Connector struct {
	conn *duckdb.Connector
	own  bool
	fsys int32
	once sync.Once
}

func (c *Connector) Close() error {
	var err1 error
	var err2 error

	c.once.Do(func() {
		if status := C.duckfs_unregister_subsystem(duckdbConnectorDatabase(c.conn)); status != 0 {
			err1 = fmt.Errorf("duckdb error when attempting to unregister a virtual file system: %d", status)
		}
		globalFsys.unregister(c.fsys)
	})

	if c.own {
		err2 = c.conn.Close()
	}

	return errors.Join(err1, err2)
}

func (c *Connector) Connect(ctx context.Context) (driver.Conn, error) {
	return c.conn.Connect(ctx)
}

func (c *Connector) Driver() driver.Driver {
	return c.conn.Driver()
}

// Open constructs a DuckDB connector that uses the provided filesystem as the
// virtual filesystem for the DuckDB database.
//
// The returned connector must be explicitly closed to avoid leaking resources,
// unless passed to sql.OpenDB, which takes ownership of the connector and
// closes it when sql.DB.Close is called.
//
// https://pkg.go.dev/github.com/marcboeker/go-duckdb/v2#NewConnector
func Open(dsn string, connInitFn func(execer driver.ExecerContext) error, fsys fs.FS) (*Connector, error) {
	c, err := duckdb.NewConnector(dsn, connInitFn)
	if err != nil {
		return nil, err
	}
	x, err := New(c, fsys)
	if err != nil {
		c.Close()
		return nil, err
	}
	x.own = true
	return x, nil
}

// New constructs a Connector that wraps the given DuckDB connector.
//
// The returned Connector does not take ownership of c, so the caller remains
// responsible for closing it to avoid leaking resources.
func New(c *duckdb.Connector, fsys fs.FS) (*Connector, error) {
	f := globalFsys.register(fsys)
	if status := C.duckfs_register_subsystem(duckdbConnectorDatabase(c), C.int(f)); status != 0 {
		globalFsys.unregister(f)
		return nil, fmt.Errorf("duckdb error when attempting to register a virtual file system: %d", status)
	}
	return &Connector{conn: c, fsys: f}, nil
}

type duckdbConnector struct { // same memory layout as duckdb.Connector
	_        bool
	database C.duckdb_database
}

func duckdbConnectorDatabase(c *duckdb.Connector) C.duckdb_database {
	return (*duckdbConnector)(unsafe.Pointer(c)).database
}
