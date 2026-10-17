package inflate

import (
	"go/build"
	"slices"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/internal/importcheck"
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

	// And what those compile in: the standard library's packages are
	// trusted to stay off the network, but the claim is checked, not
	// assumed, as it is for slice.
	banned := []string{"net", "net/http", "net/url", "os/exec", "github.com/wisborg/osmbase/"}
	root, g := importcheck.Load(t)
	for p, chain := range g.Reaches(root, banned) {
		t.Errorf("inflate compiles in %q, by %s", p, strings.Join(chain, " -> "))
	}
}
