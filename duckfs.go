// Package duckfs implements a DuckDB filesystem interface using the Go
// standard library's filesystem interface (io/fs).
package duckfs

// #include <gofs_extension.hpp>
import "C"
import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"runtime"
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

//export duckfs_file_open
func duckfs_file_open(id C.int, path *C.char) C.int {
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

//export duckfs_file_close
func duckfs_file_close(id C.int) C.int {
	f, ok := globalFiles.unregister(int32(id))
	if !ok {
		return -1
	}
	f.Close()
	return 0
}

//export duckfs_file_size
func duckfs_file_size(id C.int) C.int64_t {
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
		return -1
	}
	buffer := unsafe.Slice((*byte)(buf), size)
	n, err := f.Read(buffer)
	if err == nil {
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

// Register registers a Go filesystem with the DuckDB database.
//
// The fs.FS remains the virtual filesystem for the DuckDB database until
// the connector is closed, Unregister is called, or another call to
// Register is made to replace it.
func Register(c *duckdb.Connector, fsys fs.FS) error {
	id := globalFsys.register(fsys)
	if status := C.duckfs_register_subsystem(duckdbConnectorDatabase(c), C.int(id)); status != 0 {
		globalFsys.unregister(id)
		return fmt.Errorf("duckdb error when attempting to register a virtual file system: %d", status)
	}
	runtime.AddCleanup(c, unregister, id)
	return nil
}

func unregister(id int32) {
	globalFsys.unregister(id)
}

// Unregister removes the virtual filesystem backing a DuckDB database.
func Unregister(c *duckdb.Connector) error {
	if status := C.duckfs_unregister_subsystem(duckdbConnectorDatabase(c)); status != 0 {
		return fmt.Errorf("duckdb error when attempting to unregister a virtual file system: %d", status)
	}
	return nil
}

type connector struct { // same memory layout as duckdb.Connector
	database C.duckdb_database
}

func duckdbConnectorDatabase(c *duckdb.Connector) C.duckdb_database {
	return (*connector)(unsafe.Pointer(c)).database
}
