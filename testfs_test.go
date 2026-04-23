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
// Used to test that duckfs_glob falls back to fs.Glob's ReadDir walker
// when the FS does not implement the optional GlobFS interface.
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
