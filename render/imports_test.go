package render

import (
	"go/build"
	"slices"
	"strings"
	"testing"
)

// TestPackage_ReachesNeitherTheArchiveNorTheNetwork is a structural test, not a
// behavioural one, and it is here because the claim it protects is the reason
// this library exists.
//
// "No third-party request at the moment a map is drawn" is a promise about what
// this package CAN do, not about what it happens to do. It holds because the
// renderer's tile source is one method wide and is declared here: there is no
// archive reader and no HTTP client anywhere in its vocabulary, so a future
// change cannot quietly add a fetch on a cache miss. A reader should be able to
// check that by reading the import list, and this is that check, written down.
//
// build.ImportDir with mode 0 reads the package's own files and not its tests,
// which is the right boundary: a fixture may use anything, and a render may
// not.
func TestPackage_ReachesNeitherTheArchiveNorTheNetwork(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("reading this package's imports: %v", err)
	}

	banned := []string{
		"github.com/wisborg/osmbase/pmtiles",
		"github.com/wisborg/osmbase/acquire",
		"net/http", "net", "net/url", "os/exec",
	}
	for _, imp := range pkg.Imports {
		if slices.Contains(banned, imp) {
			t.Errorf("render imports %q; the renderer talks to a TileSource and to nothing else, and a miss must never become a fetch", imp)
		}
	}

	// The positive half. Only these three packages of this module are the
	// renderer's business: geometry, projection and ink.
	allowed := []string{
		"github.com/wisborg/osmbase/raster",
		"github.com/wisborg/osmbase/mvt",
		"github.com/wisborg/osmbase/mercator",
	}
	for _, imp := range pkg.Imports {
		if !strings.HasPrefix(imp, "github.com/wisborg/osmbase/") {
			continue
		}
		if !slices.Contains(allowed, imp) {
			t.Errorf("render imports %q, which is not one of %v", imp, allowed)
		}
	}
}
