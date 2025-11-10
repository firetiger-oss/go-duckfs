package duckfs_test

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed testdata
var testdata embed.FS

// testFS wraps an fs.FS and handles test:// protocol prefix
type testFS struct {
	fsys fs.FS
}

func (t *testFS) Open(name string) (fs.File, error) {
	// Strip test:// prefix if present
	name = strings.TrimPrefix(name, "test://")
	return t.fsys.Open(name)
}

func newTestFS() *testFS {
	return &testFS{fsys: testdata}
}

func newTestFSFromDir(dir string) *testFS {
	subFS, err := fs.Sub(testdata, dir)
	if err != nil {
		panic(err)
	}
	return &testFS{fsys: subFS}
}
