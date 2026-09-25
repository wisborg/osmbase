package slice

import (
	"go/build"
	"slices"
	"strings"
	"testing"
)

// TestPackage_HasNoWayToFetchAnything is a structural test, not a behavioural
// one, and it is here because the claim it protects is the reason this library
// exists.
//
// "No third-party request at the moment a map is drawn" is a promise about
// what this package CAN do, not about what it happens to do. A store is where
// the temptation lives: a miss is a tile somebody wants, the URL it came from
// is right there in the manifest, and filling it in would be four lines. It
// holds because there is no HTTP client and no archive reader anywhere in this
// package's vocabulary -- the archive arrives as an interface the CALLER
// satisfies, at acquisition time, by a command a user typed.
//
// A reader should be able to check that by reading the import list, and this
// is that check, written down.
//
// build.ImportDir with mode 0 reads the package's own files and not its tests,
// which is the right boundary: a fixture may open an archive, and a store may
// not.
func TestPackage_HasNoWayToFetchAnything(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("reading this package's imports: %v", err)
	}

	banned := []string{
		"github.com/wisborg/osmbase/acquire",
		"github.com/wisborg/osmbase/pmtiles",
		"github.com/wisborg/osmbase/render",
		"net/http", "net", "net/url", "os/exec",
	}
	for _, imp := range pkg.Imports {
		if slices.Contains(banned, imp) {
			t.Errorf("slice imports %q; the store is filled by a caller that opened an archive, and a miss must never become a fetch", imp)
		}
	}

	// The positive half. Two packages of this module are the store's
	// business: the projection, which is how a rectangle of degrees becomes a
	// cell, and the bounded decompression every stored tile passes through --
	// which imports nothing but the standard library's compression and io,
	// and has its own test saying so.
	allowed := []string{"github.com/wisborg/osmbase/mercator", "github.com/wisborg/osmbase/internal/inflate"}
	for _, imp := range pkg.Imports {
		if !strings.HasPrefix(imp, "github.com/wisborg/osmbase/") {
			continue
		}
		if !slices.Contains(allowed, imp) {
			t.Errorf("slice imports %q, which is not one of %v", imp, allowed)
		}
	}
}
