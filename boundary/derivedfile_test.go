package boundary

import (
	"path/filepath"
	"strings"
	"testing"
)

// A region comes from a URL or a filename the user supplied, so it is input,
// and it becomes a path component. The same interpolation on the detail
// escaped the store once.
func TestARegionCannotEscapeTheStore(t *testing.T) {
	for _, region := range []string{
		"", "..", "../etc/passwd", "a/b", `a\b`, ".hidden", "with space",
		"semi;colon", "dollar$", "quote'", "null\x00byte", "tilde~",
		strings.Repeat("x", 65),
	} {
		t.Run(region, func(t *testing.T) {
			if ValidRegion(region) {
				t.Errorf("ValidRegion(%q) = true", region)
			}
			name, err := DerivedFile(region)
			if err == nil {
				t.Fatalf("DerivedFile(%q) = %q, want an error", region, name)
			}
		})
	}
}

func TestAnOrdinaryRegionNamesAFileInsideTheStore(t *testing.T) {
	for _, region := range []string{"denmark", "denmark-latest", "new_south_wales", "au.nsw", "x"} {
		name, err := DerivedFile(region)
		if err != nil {
			t.Fatalf("DerivedFile(%q): %v", region, err)
		}
		if !strings.HasSuffix(name, DerivedExt) {
			t.Errorf("DerivedFile(%q) = %q, want the %s extension", region, name, DerivedExt)
		}
		// The name must stay one component: joined to a directory it has to
		// land inside it.
		joined := filepath.Join("/store/boundaries", name)
		if filepath.Dir(joined) != "/store/boundaries" {
			t.Errorf("DerivedFile(%q) = %q, which lands at %q", region, name, joined)
		}
	}
}

// Two regions must not share a name, or a store holding both answers from
// whichever was written last.
func TestTwoRegionsGetTwoNames(t *testing.T) {
	a, _ := DerivedFile("denmark")
	b, _ := DerivedFile("sydney")
	if a == b {
		t.Errorf("both regions are stored as %q", a)
	}
}
