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

func (t *testFS) Glob(pattern string) ([]string, error) {
	pattern = strings.TrimPrefix(pattern, "test://")
	matches, err := fs.Glob(t.fsys, pattern)
	if err != nil {
		return nil, err
	}
	for i, m := range matches {
		matches[i] = "test://" + m
	}
	return matches, nil
}

func newTestFS() *testFS {
	return &testFS{fsys: testdata}
}

// plainTestFS wraps an fs.FS but does NOT implement fs.GlobFS.
// Used to test behavior when glob is not supported on virtual paths.
type plainTestFS struct {
	fsys fs.FS
}

func (t *plainTestFS) Open(name string) (fs.File, error) {
	name = strings.TrimPrefix(name, "test://")
	return t.fsys.Open(name)
}

func newTestFSFromDir(dir string) *testFS {
	subFS, err := fs.Sub(testdata, dir)
	if err != nil {
		panic(err)
	}
	return &testFS{fsys: subFS}
}
