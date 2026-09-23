package boundary

import (
	"os"
	"path/filepath"
	"slices"
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

// The length limit is a limit, and 64 is inside it.
//
// The rejection table carries a 65-character name and the acceptance table
// carries nothing longer than "new_south_wales", so a limit that had slipped
// to "64 or more is too long" would be refusing a name it documents as legal
// with no test to say so. The pair below is the boundary itself: 64 in, 65
// out.
//
// It matters because the region is derived from a filename by default. A
// Geofabrik sub-region -- "north-america/us/district-of-columbia-latest" --
// is not far off, and the failure would arrive as a command that refuses to
// name its own output.
func TestARegionMayBeAsLongAsTheLimitSays(t *testing.T) {
	const limit = 64
	for _, tc := range []struct {
		n  int
		ok bool
	}{
		{1, true},
		{limit - 1, true},
		{limit, true},
		{limit + 1, false},
	} {
		region := strings.Repeat("a", tc.n)
		if got := ValidRegion(region); got != tc.ok {
			t.Errorf("ValidRegion(%d characters) = %v, want %v", tc.n, got, tc.ok)
		}
		name, err := DerivedFile(region)
		if (err == nil) != tc.ok {
			t.Errorf("DerivedFile(%d characters) = %q, %v; want ok=%v", tc.n, name, err, tc.ok)
		}
	}
}

// The working copy of an extract is the second path component built from the
// same user-supplied string, and the command was spelling it inline -- safe
// only while the other name happened to be built first, which makes an
// ordering the check.
func TestAnExtractNameIsValidatedLikeTheOutput(t *testing.T) {
	for _, region := range []string{"..", "../etc/passwd", "a/b", "", ".hidden"} {
		if name, err := ExtractFile(region); err == nil {
			t.Errorf("ExtractFile(%q) = %q, want an error", region, name)
		}
	}
	name, err := ExtractFile("denmark")
	if err != nil {
		t.Fatalf("ExtractFile: %v", err)
	}
	if filepath.Dir(filepath.Join("/store/boundaries", name)) != "/store/boundaries" {
		t.Errorf("ExtractFile(denmark) = %q, which leaves the store", name)
	}
	// And it cannot collide with the output for the same region.
	derived, _ := DerivedFile("denmark")
	if name == derived {
		t.Errorf("the extract and the output are both %q", name)
	}
}

// locate has to find these files without ever being told a region, so the
// naming rule has an inverse here rather than a second spelling in cmd.
func TestTheRegionCanBeRecoveredFromAFilesName(t *testing.T) {
	for _, region := range []string{"denmark", "new_south_wales", "au.nsw"} {
		name, err := DerivedFile(region)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := RegionOf(name)
		if !ok || got != region {
			t.Errorf("RegionOf(%q) = %q, %v; want %q", name, got, ok, region)
		}
	}

	// Everything else in that directory is not one of these.
	for _, name := range []string{
		"ne_10m_admin_0_countries.geojson", // the Natural Earth outlines
		"extract_denmark.osm.pbf",          // a working copy --keep-extract left
		"osm_denmark.osmb.117",             // an interrupted run's temporary
		"osm_.osmb", "osm_../x.osmb", "denmark.osmb", "osm_denmark",
	} {
		if region, ok := RegionOf(name); ok {
			t.Errorf("RegionOf(%q) = %q, and it is not a derived file", name, region)
		}
	}
}

func TestAStoreListsTheRegionsItHolds(t *testing.T) {
	root := t.TempDir()
	dir := Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{
		"osm_sydney.osmb", "osm_denmark.osmb",
		"ne_10m_admin_0_countries.geojson", "extract_denmark.osm.pbf", "osm_x.osmb.9",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := DerivedRegions(root)
	if err != nil {
		t.Fatalf("DerivedRegions: %v", err)
	}
	if !slices.Equal(got, []string{"denmark", "sydney"}) {
		t.Errorf("DerivedRegions = %v, want denmark and sydney in order", got)
	}

	// A store with no boundaries at all is not an error; it is a store
	// nobody has fetched into.
	empty, err := DerivedRegions(t.TempDir())
	if err != nil || len(empty) != 0 {
		t.Errorf("DerivedRegions on an empty store = %v, %v", empty, err)
	}
}
