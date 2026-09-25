package inflate

import (
	"go/build"
	"slices"
	"testing"
)

// slice imports this package and promises, by its import list, that a store
// can never fetch anything. That promise now runs through here, so this
// package's import list is held to the same standard: the standard library's
// compression, bytes and io, and nothing that could open a connection or
// reach another package of the module.
func TestPackage_ImportsNothingThatCouldFetch(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{"bytes", "compress/gzip", "errors", "fmt", "io"}
	for _, imp := range pkg.Imports {
		if !slices.Contains(allowed, imp) {
			t.Errorf("inflate imports %q, which is not one of %v", imp, allowed)
		}
	}
}
