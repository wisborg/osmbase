package importcheck

import (
	"reflect"
	"slices"
	"testing"
)

// The chain is the shortest by which a package is reached, and none where it
// is not.
func TestChain(t *testing.T) {
	g := Graph{
		"a": {"b", "c"},
		"b": {"d"},
		"c": {"e"},
		"e": {"d", "f"},
	}
	if got, want := g.Chain("a", "d"), []string{"a", "b", "d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a to d: %v, want %v, the shorter way", got, want)
	}
	if got, want := g.Chain("a", "f"), []string{"a", "c", "e", "f"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a to f: %v, want %v", got, want)
	}
	if got := g.Chain("b", "f"); got != nil {
		t.Errorf("b reaches f by %v; it does not", got)
	}
}

// Reaches matches a banned path exactly, or a banned prefix ending in a
// slash, and never the package itself.
func TestReaches(t *testing.T) {
	g := Graph{
		"m/root":    {"net", "m/inner"},
		"m/inner":   {"m/inner/x"},
		"m/inner/x": nil,
		"net":       nil,
		"netip":     nil,
	}
	got := g.Reaches("m/root", []string{"net", "m/", "os/exec"})
	want := map[string][]string{
		"net":       {"m/root", "net"},
		"m/inner":   {"m/root", "m/inner"},
		"m/inner/x": {"m/root", "m/inner", "m/inner/x"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v (netip is not net; the root is not its own import)", got, want)
	}
}

// Load reads this package's own graph, and nothing of its test files': reflect,
// which only the test imports, is not listed among the package's imports.
func TestLoad(t *testing.T) {
	root, g := Load(t)
	if root != "github.com/wisborg/osmbase/internal/importcheck" {
		t.Errorf("root %q", root)
	}
	if g.Chain(root, "os/exec") == nil {
		t.Error("os/exec, which this package imports, not reached")
	}
	if slices.Contains(g[root], "reflect") {
		t.Error("reflect, which only this package's test imports, listed as its own import")
	}
}
