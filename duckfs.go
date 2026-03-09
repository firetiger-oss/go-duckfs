// Package duckfs implements a DuckDB filesystem interface using the Go
// standard library's filesystem interface ([io/fs]).
//
// # Path Routing
//
// duckfs routes file operations based on the path format:
//
//   - Protocol paths (e.g., "test://data/file.parquet") are served by the
//     registered [io/fs.FS] implementation. The full path including the
//     protocol prefix is passed to [io/fs.FS.Open], so the FS must handle
//     prefix stripping.
//   - Absolute paths (e.g., "/tmp/data.parquet") bypass the virtual
//     filesystem and are served directly by the operating system.
//   - Relative paths are resolved against the process working directory
//     by the OS, not the virtual filesystem.
//   - Write operations always use the local OS filesystem, since [io/fs.FS]
//     is a read-only interface.
//
// # Usage
//
// Create a [Connector] with [Open] for a standalone DuckDB instance, or
// [New] to wrap an existing [github.com/duckdb/duckdb-go/v2.Connector]:
//
//	c, err := duckfs.Open("", nil, myFS)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	db := sql.OpenDB(c)
//	defer db.Close()
//
// The [Connector] must be explicitly closed to release resources, unless
// passed to [database/sql.OpenDB], which takes ownership of the connector.
package duckfs

// #cgo CFLAGS:   -I${SRCDIR}/duckdb/v1.4.4/src/include
// #cgo CXXFLAGS: -I${SRCDIR}/duckdb/v1.4.4/src/include -std=c++17
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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/duckdb/duckdb-go/mapping"
	"github.com/duckdb/duckdb-go/v2"
)

func isVirtualFilePath(path string) bool {
	return strings.HasPrefix(path, ":memory:") || strings.Contains(path, "://")
}

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

//export duckfs_directory_exists
func duckfs_directory_exists(id C.int, path *C.char) C.int {
	p := C.GoString(path)

	if filepath.IsAbs(p) {
		info, err := os.Stat(p)
		if err != nil {
			return 0
		}
		if !info.IsDir() {
			return 0
		}
		return 1
	}

	fsys, ok := globalFsys.lookup(int32(id))
	if !ok {
		return 0
	}
	info, err := fs.Stat(fsys, p)
	if err != nil {
		return 0
	}
	if !info.IsDir() {
		return 0
	}
	return 1
}

//export duckfs_file_exists
func duckfs_file_exists(id C.int, path *C.char) C.int {
	p := C.GoString(path)

	if filepath.IsAbs(p) {
		_, err := os.Stat(p)
		if err != nil {
			return 0
		}
		return 1
	}

	fsys, ok := globalFsys.lookup(int32(id))
	if !ok {
		return 0
	}
	_, err := fs.Stat(fsys, p)
	if err != nil {
		return 0
	}
	return 1
}

//export duckfs_file_open
func duckfs_file_open(id C.int, path *C.char) C.int {
	p := C.GoString(path)

	if !isVirtualFilePath(p) {
		f, err := os.Open(p)
		if err != nil {
			return -1
		}
		return C.int(globalFiles.register(f))
	}

	fsys, ok := globalFsys.lookup(int32(id))
	if !ok {
		slog.Warn("duckfs_file_open: file system not found", "filesystem", id, "path", p)
		return -1
	}
	f, err := fsys.Open(p)
	if err != nil {
		return -1
	}
	return C.int(globalFiles.register(f))
}

//export duckfs_file_open_write
func duckfs_file_open_write(path *C.char, flags C.int) C.int {
	p := C.GoString(path)

	if flags&C.DUCKDB_FILE_FLAGS_NULL_IF_EXISTS != 0 {
		if _, err := os.Stat(p); err == nil {
			return -1
		}
	}

	const (
		r  = C.DUCKDB_FILE_FLAGS_READ
		w  = C.DUCKDB_FILE_FLAGS_WRITE
		rw = r | w
	)

	var goFlags int
	switch {
	case flags&rw == rw:
		goFlags |= os.O_RDWR
	case flags&r != 0:
		goFlags |= os.O_RDONLY
	case flags&w != 0:
		goFlags |= os.O_WRONLY
	}
	if flags&C.DUCKDB_FILE_FLAGS_FILE_CREATE != 0 {
		goFlags |= os.O_CREATE
	}
	if flags&C.DUCKDB_FILE_FLAGS_FILE_CREATE_NEW != 0 {
		goFlags |= os.O_CREATE | os.O_TRUNC
	}
	if flags&C.DUCKDB_FILE_FLAGS_EXCLUSIVE_CREATE != 0 {
		goFlags |= os.O_CREATE | os.O_EXCL
	}
	if flags&C.DUCKDB_FILE_FLAGS_APPEND != 0 {
		goFlags |= os.O_APPEND
	} else if flags&C.DUCKDB_FILE_FLAGS_WRITE != 0 {
		goFlags |= os.O_TRUNC
	}

	if goFlags == 0 {
		goFlags = os.O_RDONLY
	}

	if goFlags&os.O_CREATE != 0 && flags&C.DUCKDB_FILE_FLAGS_NULL_IF_NOT_EXISTS == 0 {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			slog.Warn("duckfs_file_open_write: failed to create parent directory", "path", p, "error", err)
			return -1
		}
	}

	f, err := os.OpenFile(p, goFlags, 0644)
	if err != nil {
		if flags&C.DUCKDB_FILE_FLAGS_NULL_IF_NOT_EXISTS == 0 {
			slog.Warn("duckfs_file_open_write: failed to open file", "path", p, "flags", fmt.Sprintf("0x%x", flags), "goFlags", fmt.Sprintf("0x%x", goFlags), "error", err)
		}
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
	if err := f.Close(); err != nil {
		slog.Warn("duckfs_file_close: close failed", "file", id, "error", err)
	}
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
		return -1
	}
	r, ok := f.(io.ReaderAt)
	if !ok {
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
	if err != nil && !errors.Is(err, io.EOF) {
		return -1
	}
	return C.int64_t(n)
}

//export duckfs_file_write
func duckfs_file_write(id C.int, buf unsafe.Pointer, size C.int64_t) C.int64_t {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		slog.Warn("duckfs_file_write: file not found", "file", id)
		return -1
	}
	w, ok := f.(io.Writer)
	if !ok {
		slog.Warn("duckfs_file_write: file does not support io.Writer", "file", id)
		return -1
	}
	buffer := unsafe.Slice((*byte)(buf), size)
	n, err := w.Write(buffer)
	if err != nil {
		slog.Warn("duckfs_file_write: write failed", "file", id, "error", err)
		return -1
	}
	return C.int64_t(n)
}

//export duckfs_file_write_at
func duckfs_file_write_at(id C.int, buf unsafe.Pointer, size, off C.int64_t) C.int64_t {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		slog.Warn("duckfs_file_write_at: file not found", "file", id)
		return -1
	}
	w, ok := f.(io.WriterAt)
	if !ok {
		slog.Warn("duckfs_file_write_at: file does not support io.WriterAt", "file", id)
		return -1
	}
	buffer := unsafe.Slice((*byte)(buf), size)
	n, err := w.WriteAt(buffer, int64(off))
	if err != nil && n == 0 {
		slog.Warn("duckfs_file_write_at: write at failed", "file", id, "error", err)
		return -1
	}
	return C.int64_t(n)
}

//export duckfs_file_truncate
func duckfs_file_truncate(id C.int, size C.int64_t) C.int {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		slog.Warn("duckfs_file_truncate: file not found", "file", id)
		return -1
	}
	osFile, ok := f.(*os.File)
	if !ok {
		slog.Warn("duckfs_file_truncate: file is not an *os.File", "file", id)
		return -1
	}
	if err := osFile.Truncate(int64(size)); err != nil {
		slog.Warn("duckfs_file_truncate: truncate failed", "file", id, "error", err)
		return -1
	}
	return 0
}

//export duckfs_file_sync
func duckfs_file_sync(id C.int) C.int {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		slog.Warn("duckfs_file_sync: file not found", "file", id)
		return -1
	}
	osFile, ok := f.(*os.File)
	if !ok {
		// If it's not an os.File, it's probably read-only, so sync is a no-op
		return 0
	}
	if err := osFile.Sync(); err != nil {
		slog.Warn("duckfs_file_sync: sync failed", "file", id, "error", err)
		return -1
	}
	return 0
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

//export duckfs_file_last_modified
func duckfs_file_last_modified(id C.int) C.int64_t {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		return 0
	}
	s, err := f.Stat()
	if err != nil {
		return 0
	}
	return C.int64_t(s.ModTime().Unix())
}

//export duckfs_file_is_on_disk
func duckfs_file_is_on_disk(id C.int) C.int {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		return 0
	}
	if _, ok := f.(*os.File); ok {
		return 1
	}
	return 0
}

//export duckfs_create_directory
func duckfs_create_directory(path *C.char) C.int {
	p := C.GoString(path)
	if isVirtualFilePath(p) {
		slog.Warn("duckfs_create_directory: cannot create directory in virtual filesystem", "path", p)
		return -1
	}
	if err := os.MkdirAll(p, 0755); err != nil {
		slog.Warn("duckfs_create_directory: failed to create directory", "path", p, "error", err)
		return -1
	}
	return 0
}

//export duckfs_remove_directory
func duckfs_remove_directory(path *C.char) C.int {
	p := C.GoString(path)
	if isVirtualFilePath(p) {
		slog.Warn("duckfs_remove_directory: cannot remove directory from virtual filesystem", "path", p)
		return -1
	}
	if err := os.RemoveAll(p); err != nil {
		slog.Warn("duckfs_remove_directory: failed to remove directory", "path", p, "error", err)
		return -1
	}
	return 0
}

//export duckfs_remove_file
func duckfs_remove_file(path *C.char) C.int {
	p := C.GoString(path)
	if isVirtualFilePath(p) {
		slog.Warn("duckfs_remove_file: cannot remove file from virtual filesystem", "path", p)
		return -1
	}
	if err := os.Remove(p); err != nil {
		slog.Warn("duckfs_remove_file: failed to remove file", "path", p, "error", err)
		return -1
	}
	return 0
}

//export duckfs_move_file
func duckfs_move_file(source, target *C.char) C.int {
	src := C.GoString(source)
	dst := C.GoString(target)

	// Only support moves on local file system (absolute paths)
	if !filepath.IsAbs(src) || !filepath.IsAbs(dst) {
		slog.Warn("duckfs_move_file: only absolute paths supported", "source", src, "target", dst)
		return -1
	}

	if err := os.Rename(src, dst); err != nil {
		slog.Warn("duckfs_move_file: failed to move file", "source", src, "target", dst, "error", err)
		return -1
	}

	return 0
}

//export duckfs_glob
func duckfs_glob(id C.int, pattern *C.char) *C.char {
	p := C.GoString(pattern)

	// No glob characters → no expansion needed.
	if !strings.ContainsAny(p, "*?[") {
		return nil
	}

	// Local filesystem path (no protocol prefix) → filepath.Glob.
	if !strings.Contains(p, "://") {
		matches, err := filepath.Glob(p)
		if err != nil || len(matches) == 0 {
			return C.CString("")
		}
		return C.CString(strings.Join(matches, "\n"))
	}

	// Virtual path with protocol prefix.
	fsys, ok := globalFsys.lookup(int32(id))
	if !ok {
		return C.CString("")
	}

	globFS, ok := fsys.(fs.GlobFS)
	if !ok {
		slog.Warn("duckfs_glob: fs.FS does not implement fs.GlobFS; glob patterns on virtual paths are not supported", "pattern", p)
		return C.CString("")
	}

	matches, err := globFS.Glob(p)
	if err != nil || len(matches) == 0 {
		return C.CString("")
	}
	return C.CString(strings.Join(matches, "\n"))
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
	runtime.SetFinalizer(c, nil)

	var err error
	c.once.Do(func() {
		if c.own {
			// When we own the connector, closing it shuts down the
			// database and internally cleans up all subsystems.
			err = c.conn.Close()
		} else {
			// When we don't own the connector, the database outlives
			// this Connector, so we must explicitly unregister the
			// filesystem subsystem. All SQL connections should be
			// closed first, or open file handles may cause a segfault.
			if status := C.duckfs_unregister_subsystem(duckdbConnectorDatabase(c.conn)); status != 0 {
				err = fmt.Errorf("duckdb error when attempting to unregister a virtual file system: %d", status)
			}
		}
		globalFsys.unregister(c.fsys)
	})

	return err
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

// newConnector creates a Connector with a finalizer that logs a warning if
// the Connector is garbage collected without being closed.
func newConnector(conn *duckdb.Connector, fsys int32) *Connector {
	c := &Connector{conn: conn, fsys: fsys}
	runtime.SetFinalizer(c, func(c *Connector) {
		slog.Warn("duckfs.Connector was garbage collected without calling Close; closing now to prevent resource leak")
		c.Close()
	})
	return c
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
	return newConnector(c, f), nil
}

type duckdbConnector struct { // same memory layout as duckdb.Connector (https://github.com/marcboeker/go-duckdb/blob/v2.3.3/duckdb.go#L45)
	database mapping.Database
}

func duckdbConnectorDatabase(c *duckdb.Connector) C.duckdb_database {
	return C.duckdb_database((*duckdbConnector)(unsafe.Pointer(c)).database.Ptr)
}
