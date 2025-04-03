// Package duckfs implements a DuckDB filesystem interface using the Go
// standard library's filesystem interface (io/fs).
package duckfs

// #include <duckdb.h>
// duckdb_state duckdb_gofs_register_subsystem(duckdb_database, uintptr_t);
// duckdb_state duckdb_gofs_unregister_subsystem(duckdb_database);
import "C"
import (
	"fmt"
	"io"
	"io/fs"
	"sync"
	"unsafe"

	"github.com/marcboeker/go-duckdb"
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

	i := 0
	for i < len(m.files) && m.files[i] == zero {
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

type connector struct {
	database C.duckdb_database
}

func duckdbConnectorDatabase(c *duckdb.Connector) C.duckdb_database {
	return (*connector)(unsafe.Pointer(c)).database
}

func duckdbConnectorRegisterFS(c *duckdb.Connector, fsys fs.FS) (int32, error) {
	id := globalFsys.register(fsys)
	if status := C.duckdb_gofs_register_subsystem(duckdbConnectorDatabase(c), C.uintptr_t(id)); status != 0 {
		globalFsys.unregister(id)
		return 0, fmt.Errorf("gofs_register: duckdb error: %d", status)
	}
	return id, nil
}

func duckdbConnectorUnregisterFS(c *duckdb.Connector, id int32) error {
	if status := C.duckdb_gofs_unregister_subsystem(duckdbConnectorDatabase(c)); status != 0 {
		return fmt.Errorf("gofs_unregister: duckdb error: %d", status)
	}
	globalFsys.unregister(id)
	return nil
}

//export duckdb_gofs_file_open
func duckdb_gofs_file_open(id C.int, path *C.char) C.int {
	fsys, ok := globalFsys.lookup(int32(id))
	if !ok {
		return -1
	}
	f, err := fsys.Open(C.GoString(path))
	if err != nil {
		return -1
	}
	return C.int(globalFiles.register(f))
}

//export duckdb_gofs_file_close
func duckdb_gofs_file_close(id C.int) C.int {
	f, ok := globalFiles.unregister(int32(id))
	if !ok {
		return -1
	}
	f.Close()
	return 0
}

//export duckdb_gofs_file_size
func duckdb_gofs_file_size(id C.int) C.int64_t {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		return -1
	}
	s, err := f.Stat()
	if err != nil {
		return -1
	}
	return C.int64_t(s.Size())
}

//export duckdb_gofs_file_read_at
func duckdb_gofs_file_read_at(id C.int, buf unsafe.Pointer, size, off C.int64_t) C.int64_t {
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

//export duckdb_gofs_file_read
func duckdb_gofs_file_read(id C.int, buf unsafe.Pointer, size C.int64_t) C.int64_t {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		return -1
	}
	buffer := unsafe.Slice((*byte)(buf), size)
	n, err := f.Read(buffer)
	if err != nil {
		return -1
	}
	return C.int64_t(n)
}

//export duckdb_gofs_file_seek
func duckdb_gofs_file_seek(id C.int, off C.int64_t, whence int) C.int64_t {
	f, ok := globalFiles.lookup(int32(id))
	if !ok {
		return -1
	}
	r, ok := f.(io.Seeker)
	if !ok {
		return -1
	}
	s, err := r.Seek(int64(off), whence)
	if err != nil {
		return -1
	}
	return C.int64_t(s)
}

// RegisteredFS is a the type representing a Go filesystem that has been
// registered as virtual file system to a DuckDB database.
type RegisteredFS struct {
	fs.FS

	dc   *duckdb.Connector
	id   int32
	once sync.Once
}

// Close the registered filesystem. This must be called before the DuckDB
// database is closed to unregister the virtual file system.
//
// Close may be called concurrently from multiple goroutines.
func (f *RegisteredFS) Close() error {
	f.once.Do(func() { duckdbConnectorUnregisterFS(f.dc, f.id) })
	return nil
}

// Register registers a Go filesystem with the DuckDB database. The
// returned RegisteredFS must be closed before the connector is closed
// to unregister the virtual file system and free resources.
func Register(c *duckdb.Connector, fsys fs.FS) (*RegisteredFS, error) {
	id, err := duckdbConnectorRegisterFS(c, fsys)
	if err != nil {
		return nil, err
	}
	return &RegisteredFS{FS: fsys, dc: c, id: id}, nil
}
