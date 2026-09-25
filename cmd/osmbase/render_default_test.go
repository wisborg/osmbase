package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/slice"
)

// useCacheDir points the user cache directory, and so the default store, at
// a temporary directory.
func useCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, ".cache"))
	root, err := slice.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(root, dir) {
		t.Skipf("the default store is at %s, which this platform does not let a test move", root)
	}
	return root
}

// With no SOURCE and no --store, a default store holding a map is drawn from,
// and the network is not touched -- runCLI fails the test if it is. Rendering
// the same place twice used to read the default archive over the network
// twice, while the tiles sat in the cache.
func TestRenderWithNoSourceDrawsFromTheDefaultStore(t *testing.T) {
	root := useCacheDir(t)
	if r := runCLI(t, "fetch", deepArchive(t, 1, "Cached Credit"), "--world", "--max-zoom", "1", "--store", root, "--yes"); r.code != 0 {
		t.Fatalf("fetch: %s", r.stderr)
	}
	out := filepath.Join(t.TempDir(), "map.png")
	r := runCLI(t, "render", "--lat", "10", "--lon", "10", "--zoom", "1", "--width", "256", "--height", "256", "--out", out)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "drawing from the store at "+root) {
		t.Errorf("the render does not say it drew from the default store:\n%s", r.stderr)
	}
	if line, _ := lineContaining(r.stdout, "credit"); !strings.Contains(line, "Cached Credit") {
		t.Errorf("the credit is not the stored archive's: %q", line)
	}
}

// Naming a SOURCE still reads it, default store or not.
func TestRenderWithASourceReadsItEvenWithADefaultStore(t *testing.T) {
	root := useCacheDir(t)
	if r := runCLI(t, "fetch", deepArchive(t, 0, "Cached Credit"), "--world", "--max-zoom", "0", "--store", root, "--yes"); r.code != 0 {
		t.Fatalf("fetch: %s", r.stderr)
	}
	out := filepath.Join(t.TempDir(), "map.png")
	r := runCLI(t, "render", deepArchive(t, 0, "Named Credit"), "--lat", "1", "--lon", "1", "--zoom", "0",
		"--width", "128", "--height", "128", "--out", out)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if strings.Contains(r.stderr, "drawing from the store") {
		t.Errorf("a named SOURCE was passed over for the default store:\n%s", r.stderr)
	}
	if line, _ := lineContaining(r.stdout, "credit"); !strings.Contains(line, "Named Credit") {
		t.Errorf("the credit is not the named archive's: %q", line)
	}
}
