package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/osmbasetest"
)

// The name of the derived file is the one thing about this command a person
// has to predict in order to find its output again, and the rule is stated in
// the usage: the output is named after the extract. Every expectation below
// is derived from that rule -- drop the directory, drop the extension a
// .osm.pbf carries, drop the "-latest" a Geofabrik mirror puts in every
// filename -- and not from what the function happens to return.
//
// The order of the three strips is load-bearing and the cases say so. Taking
// ".osm" before ".pbf" leaves "denmark.osm" for the commonest filename there
// is, and taking "-latest" before either leaves "denmark-latest".
func TestRegionFrom_NamesTheOutputAfterTheExtract(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   string
	}{
		{"sydney.osm.pbf", "sydney"},
		{"denmark-latest.osm.pbf", "denmark"},
		{"/a/b/denmark-latest.osm.pbf", "denmark"},
		{"https://download.example/australia-oceania/australia-latest.osm.pbf", "australia"},
		{`C:\extracts\sydney.osm.pbf`, "sydney"},
		// The pieces on their own, so a strip that stopped happening is
		// distinguishable from one that happened at the wrong moment.
		{"sydney.pbf", "sydney"},
		{"sydney.osm", "sydney"},
		{"sydney-latest", "sydney"},
		{"sydney", "sydney"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			got, err := regionFrom(tc.source)
			if err != nil {
				t.Fatalf("regionFrom(%q): %v", tc.source, err)
			}
			if got != tc.want {
				t.Errorf("regionFrom(%q) = %q, want %q", tc.source, got, tc.want)
			}
		})
	}
}

// The other half: a source that does not yield a usable name must say so and
// point at --region, rather than producing something that goes on to become a
// path component.
//
// The query-string case is the one a person actually hits. A mirror behind a
// CDN or a signed link ends in "?token=...", and the answer has to be an
// instruction rather than a file called "x.osm.pbf?token=abc".
// A URL's query and fragment are not part of its filename, and the path
// usually names the region perfectly well. Sliced as a string they become
// part of it and the command refuses -- in exactly the case, a presigned
// link, where a credential is in play and the refusal would echo it.
func TestRegionFrom_ReadsAURLsPathAndNotItsQuery(t *testing.T) {
	for _, source := range []string{
		"https://example.test/denmark-latest.osm.pbf?token=abc",
		"https://example.test/denmark-latest.osm.pbf#frag",
		"https://user:pw@example.test/denmark-latest.osm.pbf?X-Amz-Signature=deadbeef",
		"https://example.test/europe/denmark-latest.osm.pbf",
	} {
		t.Run(source, func(t *testing.T) {
			got, err := regionFrom(source)
			if err != nil {
				t.Fatalf("regionFrom(%q): %v", source, err)
			}
			if got != "denmark" {
				t.Errorf("regionFrom(%q) = %q, want denmark", source, got)
			}
		})
	}
}

func TestRegionFrom_RefusesASourceThatNamesNoRegion(t *testing.T) {
	for _, source := range []string{
		"/a/b/",
		"",
		"-latest.osm.pbf",  // strips down to nothing
		".osm.pbf",         // strips to "", and a leading dot besides
		"../x/../y y.pbf",  // a space is not a filename this will build
		"denmark;rm -rf /", // nor is anything a shell would read
	} {
		t.Run(source, func(t *testing.T) {
			got, err := regionFrom(source)
			if err == nil {
				t.Fatalf("regionFrom(%q) = %q, want an error", source, got)
			}
			if !strings.Contains(err.Error(), "--region") {
				t.Errorf("error %q does not say how to fix it", err)
			}
		})
	}
}

// Whatever regionFrom returns becomes a path component twice over -- the
// derived file and, for a fetch, the extract beside it -- so it has to satisfy
// the check boundary makes of a region no matter what the source looked like.
// A traversal in the source must not survive into the name.
func TestRegionFrom_NeverYieldsANameThatLeavesTheStore(t *testing.T) {
	for _, source := range []string{
		"../../etc/passwd",
		"https://example.test/../../etc/passwd",
		"/etc/passwd",
		`..\..\windows\system32`,
		"a/b/../c.osm.pbf",
	} {
		t.Run(source, func(t *testing.T) {
			got, err := regionFrom(source)
			if err != nil {
				return // refusing outright is a fine answer
			}
			if !boundary.ValidRegion(got) {
				t.Fatalf("regionFrom(%q) = %q, which boundary rejects", source, got)
			}
			joined := filepath.Join("/store/boundaries", "extract_"+got+".osm.pbf")
			if filepath.Dir(joined) != "/store/boundaries" {
				t.Errorf("regionFrom(%q) = %q, whose extract lands at %q", source, got, joined)
			}
		})
	}
}

// --levels carries OpenStreetMap's own admin_level numbers, documented as
// running from 1 to 12. The boundaries of that range are here together with
// the values just outside it, because an off-by-one either way is the whole
// bug this check exists to stop.
func TestParseAdminLevels_ReadsTheDocumentedRange(t *testing.T) {
	for _, tc := range []struct {
		list string
		want []int
	}{
		{"", nil},
		{"   ", nil},
		{"1", []int{1}},
		{"12", []int{12}},
		{"9", []int{9}},
		{"8,9,10", []int{8, 9, 10}},
		{" 8 , 9 ,10 ", []int{8, 9, 10}},
		// Order is the caller's, not sorted behind their back: the levels
		// are recorded in the file's provenance and a reader comparing them
		// against what they asked for should see what they asked for.
		{"10,7", []int{10, 7}},
	} {
		t.Run(tc.list, func(t *testing.T) {
			got, err := parseAdminLevels(tc.list)
			if err != nil {
				t.Fatalf("parseAdminLevels(%q): %v", tc.list, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseAdminLevels(%q) = %v, want %v", tc.list, got, tc.want)
			}
		})
	}
}

// An empty list means every level, and that has to be the empty slice rather
// than a slice of something: osm.Read treats nil as "keep them all", so a
// parse that returned []int{0} for " " would silently keep nothing.
func TestParseAdminLevels_RefusesWhatIsNotALevel(t *testing.T) {
	for _, list := range []string{
		"0",      // below the range
		"13",     // above it
		"-1",     //
		"suburb", // the locate command's spelling, which this is not
		"9.5",
		"9,",   // a trailing comma is an empty field
		",9",   //
		"9,,7", //
		"8;9",  // the separator OSM itself uses in the tag, not here
		"9 10",
		"0x9",
	} {
		t.Run(list, func(t *testing.T) {
			got, err := parseAdminLevels(list)
			if err == nil {
				t.Fatalf("parseAdminLevels(%q) = %v, want an error", list, got)
			}
			if got != nil {
				t.Errorf("parseAdminLevels(%q) returned %v alongside its error", list, got)
			}
			if !strings.Contains(err.Error(), "1 to 12") {
				t.Errorf("error %q does not say what a level may be", err)
			}
		})
	}
}

// twoLevelExtract writes an extract holding two closed boundaries at two
// different admin_levels, side by side rather than nested so that a point
// resolves to exactly one of them.
//
// Two levels because one cannot show that --levels SELECTS: an extract with a
// single level passes a test of "the level asked for is in the file" whether
// the filter runs or not.
func twoLevelExtract(t *testing.T, dir, name string) string {
	t.Helper()
	e := osmbasetest.NewExtract().
		Node(1, -33.70, 151.10).
		Node(2, -33.70, 151.20).
		Node(3, -33.60, 151.20).
		Node(4, -33.60, 151.10).
		Node(5, -33.50, 151.10).
		Node(6, -33.50, 151.20).
		Node(7, -33.40, 151.20).
		Node(8, -33.40, 151.10).
		Way(10, []int64{1, 2, 3}).
		Way(11, []int64{3, 4, 1}).
		Way(12, []int64{5, 6, 7}).
		Way(13, []int64{7, 8, 5})
	e.Relation(100, []osmbasetest.ExtractMember{
		{Type: "way", ID: 10, Role: "outer"},
		{Type: "way", ID: 11, Role: "outer"},
	}, "boundary", "administrative", "admin_level", "9", "name", "Hornsby")
	e.Relation(101, []osmbasetest.ExtractMember{
		{Type: "way", ID: 12, Role: "outer"},
		{Type: "way", ID: 13, Role: "outer"},
	}, "boundary", "administrative", "admin_level", "6", "name", "Hornsby Shire")

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, e.Bytes(), 0o644); err != nil {
		t.Fatalf("writing the extract: %v", err)
	}
	return path
}

// readDerived opens a derived file and returns what it holds.
func readDerived(t *testing.T, path string) *boundary.Set {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening the derived file: %v", err)
	}
	defer f.Close()
	set, err := boundary.ReadDerived(f)
	if err != nil {
		t.Fatalf("ReadDerived(%s): %v", filepath.Base(path), err)
	}
	return set
}

// --levels has to change what is IN the file, and the file has to record
// which levels it was asked for.
//
// The pair is the point. A run with --levels 9 that still held the level-6
// boundary would mean the filter never ran; a run without --levels that held
// only one of the two would mean it ran when it should not have. Either alone
// passes for an implementation that ignores the flag entirely.
//
// The recorded levels matter for the reason Provenance gives: without them a
// reader cannot tell "this file has no suburbs" from "this file was not asked
// for suburbs".
func TestBoundaries_LevelsSelectWhatTheFileHoldsAndTheFileSaysWhichWereAsked(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		want   []string // area names, in any order
		levels []int
	}{
		{"every level by default", nil, []string{"Hornsby", "Hornsby Shire"}, nil},
		{"one level", []string{"--levels", "9"}, []string{"Hornsby"}, []int{9}},
		{"the other level", []string{"--levels", "6"}, []string{"Hornsby Shire"}, []int{6}},
		{"both, named", []string{"--levels", "6,9"}, []string{"Hornsby", "Hornsby Shire"}, []int{6, 9}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := twoLevelExtract(t, dir, "sydney.osm.pbf")
			store := filepath.Join(dir, "store")

			args := append([]string{"--store", store, "--osm", src, "--yes"}, tc.args...)
			if _, stderr, err := runBoundaries(t, args...); err != nil {
				t.Fatalf("boundaries --osm: %v\n%s", err, stderr)
			}
			set := readDerived(t, filepath.Join(boundary.Dir(store), "osm_sydney.osmb"))

			var got []string
			for _, p := range []struct{ lat, lon float64 }{{-33.65, 151.15}, {-33.45, 151.15}} {
				if a, ok := set.At(p.lat, p.lon); ok {
					got = append(got, a.Name)
				}
			}
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("the file answers for %v, want %v", got, want)
			}
			if set.Len() != len(tc.want) {
				t.Errorf("the file holds %d areas, want %d", set.Len(), len(tc.want))
			}
			if got := set.Provenance().Levels; !reflect.DeepEqual(got, tc.levels) {
				t.Errorf("the file records levels %v, want %v", got, tc.levels)
			}
		})
	}
}

// An area's kind is its admin_level as written, because the level's MEANING
// differs by country and this command has no business naming it. A file that
// dropped the level would leave a consumer unable to tell a municipality from
// a suburb.
func TestBoundaries_TheFileKeepsTheAdminLevelOfEachArea(t *testing.T) {
	dir := t.TempDir()
	src := twoLevelExtract(t, dir, "sydney.osm.pbf")
	store := filepath.Join(dir, "store")

	if _, stderr, err := runBoundaries(t, "--store", store, "--osm", src, "--yes"); err != nil {
		t.Fatalf("boundaries --osm: %v\n%s", err, stderr)
	}
	set := readDerived(t, filepath.Join(boundary.Dir(store), "osm_sydney.osmb"))

	for _, tc := range []struct {
		lat, lon   float64
		name, kind string
	}{
		{-33.65, 151.15, "Hornsby", "9"},
		{-33.45, 151.15, "Hornsby Shire", "6"},
	} {
		a, ok := set.At(tc.lat, tc.lon)
		if !ok {
			t.Errorf("%.2f,%.2f is in no area, want %s", tc.lat, tc.lon, tc.name)
			continue
		}
		if a.Name != tc.name || a.Kind != tc.kind {
			t.Errorf("%.2f,%.2f resolved to %q kind %q, want %q kind %q",
				tc.lat, tc.lon, a.Name, a.Kind, tc.name, tc.kind)
		}
	}
}

// --region renames the output, and the run has to have announced the name it
// then used.
//
// Only the rejection of a bad --region was covered, which an implementation
// that validated the flag and then ignored it would pass. The announcement is
// checked against the file because the two are computed in different places:
// describeOSM prints filepath.Join(dir, name) before anything happens, and
// saveThroughTemp puts the file there afterwards.
func TestBoundaries_RegionRenamesTheOutputAndTheRunAnnouncesIt(t *testing.T) {
	dir := t.TempDir()
	src := extractFile(t, dir, "sydney.osm.pbf")
	store := filepath.Join(dir, "store")

	_, stderr, err := runBoundaries(t, "--store", store, "--osm", src, "--region", "nsw_north", "--yes")
	if err != nil {
		t.Fatalf("boundaries --osm --region: %v\n%s", err, stderr)
	}

	out := filepath.Join(boundary.Dir(store), "osm_nsw_north.osmb")
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("--region did not name the output: %v", err)
	}
	// And the name taken from the source was not used as well or instead.
	if _, err := os.Stat(filepath.Join(boundary.Dir(store), "osm_sydney.osmb")); err == nil {
		t.Error("the file was also written under the name taken from the extract")
	}
	if !strings.Contains(stderr, "it writes "+out) {
		t.Errorf("the run announced a different path from the one it wrote:\n%s", stderr)
	}
	if set := readDerived(t, out); set.Len() != 1 {
		t.Errorf("the renamed file holds %d areas, want 1", set.Len())
	}
}
