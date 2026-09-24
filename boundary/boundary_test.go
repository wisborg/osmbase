package boundary_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/wisborg/osmbase/boundary"
	"github.com/wisborg/osmbase/locate"
)

// squareArea is a GeoJSON document with one square, optionally holed.
func squareArea(name string, west, south, east, north float64, hole bool) string {
	ring := func(w, s, e, n float64) string {
		return `[[` +
			coord(w, s) + `,` + coord(e, s) + `,` + coord(e, n) + `,` + coord(w, n) + `,` + coord(w, s) +
			`]]`
	}
	rings := ring(west, south, east, north)
	if hole {
		// A hole covering the middle ninth of the square.
		dx, dy := (east-west)/3, (north-south)/3
		rings = rings[:len(rings)-1] + `,` +
			strings.TrimPrefix(ring(west+dx, south+dy, east-dx, north-dy), `[`)
	}
	return `{"type":"FeatureCollection","features":[{"properties":{"NAME":"` + name +
		`"},"geometry":{"type":"Polygon","coordinates":` + rings + `}}]}`
}

func coord(lon, lat float64) string {
	return `[` + strconv.FormatFloat(lon, 'f', -1, 64) + `,` + strconv.FormatFloat(lat, 'f', -1, 64) + `]`
}

// TestSet_ContainsAPointInsideAndRejectsOneOutside is the whole claim: this
// answers containment, which the tiles cannot.
func TestSet_ContainsAPointInsideAndRejectsOneOutside(t *testing.T) {
	set, err := boundary.Read(strings.NewReader(squareArea("Denmark", 8, 54, 13, 58, false)), "country", false)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if set.Len() != 1 {
		t.Fatalf("Len = %d, want 1", set.Len())
	}

	if a, ok := set.At(56, 10); !ok || a.Name != "Denmark" {
		t.Errorf("a point inside gave (%q, %v), want Denmark, true", a.Name, ok)
	}
	// Outside the box entirely: the cheap rejection, and the one that runs
	// for almost every area on almost every lookup.
	if _, ok := set.At(56, 30); ok {
		t.Error("a point well outside the square was reported inside")
	}
	// Inside the bounding box on one axis only, which is the case a box test
	// alone gets wrong and only the ring test catches.
	if _, ok := set.At(60, 10); ok {
		t.Error("a point north of the square was reported inside")
	}
}

// TestSet_APointInAHoleIsOutside covers the ring order GeoJSON uses.
//
// The first ring is the outline and the rest are holes, so a point inside the
// outline AND inside a hole is outside the area. An implementation that tested
// only the first ring would answer every enclave as though it were the country
// around it -- which is precisely the kind of wrong answer that looks right.
func TestSet_APointInAHoleIsOutside(t *testing.T) {
	set, err := boundary.Read(strings.NewReader(squareArea("Italy", 0, 0, 9, 9, true)), "country", false)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, ok := set.At(4.5, 4.5); ok {
		t.Error("a point in the hole was reported inside the area")
	}
	if _, ok := set.At(1, 1); !ok {
		t.Error("a point inside the outline but outside the hole was reported outside")
	}
}

// TestRead_RefusesAFileWithNoNamedAreas stops a wrong file reading as an
// empty world.
//
// An empty set answers "no area contains this" for every coordinate on earth,
// which is indistinguishable from a point in the ocean and would be reported
// as a missing level rather than as a broken download.
func TestRead_RefusesAFileWithNoNamedAreas(t *testing.T) {
	_, err := boundary.Read(strings.NewReader(`{"type":"FeatureCollection","features":[]}`), "country", false)
	if err == nil {
		t.Fatal("a file with no areas was accepted")
	}
	if !strings.Contains(err.Error(), "country") {
		t.Errorf("the error does not say which file was wrong: %v", err)
	}
}

// TestSource_CoversOnlyWhatNaturalEarthHas keeps this from answering a
// question the data cannot.
//
// Natural Earth publishes no suburb outlines. Reporting a locality as
// contained by the state around it would be a true statement answering the
// wrong question, and it would carry the Contained source -- which a consumer
// is told means a statement of fact about THAT level.
// Covers means "this source can answer that level", and the data has to be
// there for it to be true.
//
// It used to mean "this KIND of source answers that level", which was safe
// only while Available guaranteed every Natural Earth file existed before
// anyone could open a source. A store holding a derived file and no Natural
// Earth outlines broke that guarantee -- and because locate treats a covered
// level as answered EXCLUSIVELY, claiming country there took the level away
// from the tiles and then answered nothing at all.
func TestSource_CoversOnlyWhatTheStoreActuallyHas(t *testing.T) {
	empty := boundary.Open(t.TempDir(), "")
	for _, level := range []locate.Level{
		locate.Country, locate.Region, locate.Water,
		locate.Locality, locate.Macrohood, locate.Neighbourhood, locate.Street,
	} {
		if empty.Covers(level) {
			t.Errorf("Covers(%s) = true for a store holding nothing", level)
		}
	}
}

// TestSource_AMissingFileIsNoAnswerRatherThanAFailure covers the ordinary
// case: boundaries are an optional download.
//
// A store without them must still answer -- every level falls back to the
// tiles, and the output says "near" instead of "in", which is the difference
// the user can see.
func TestSource_AMissingFileIsNoAnswerRatherThanAFailure(t *testing.T) {
	dir := t.TempDir()
	if boundary.Available(dir, "") {
		t.Fatal("an empty directory reports boundaries available")
	}
	s := boundary.Open(dir, "")
	if _, _, _, ok := inside(s, locate.Country, 56, 10); ok {
		t.Error("a store with no boundary files answered a containment question")
	}
}

// TestRead_KeepsEveryPartOfAMultiPolygon is about whole countries, not edge
// cases.
//
// Natural Earth represents an archipelago or a country with offshore parts --
// Denmark, Indonesia, the Philippines, the United States with Alaska and
// Hawaii -- as a MultiPolygon. Keeping only the first part answers containment
// correctly for one piece and reports "not contained" for every other, which
// for Denmark means the mainland or the islands but not both. Mutation
// testing found the whole MultiPolygon path deletable with the suite green.
func TestRead_KeepsEveryPartOfAMultiPolygon(t *testing.T) {
	// Two separated squares under one name, as a country in two pieces.
	doc := `{"type":"FeatureCollection","features":[{"properties":{"NAME":"Denmark"},` +
		`"geometry":{"type":"MultiPolygon","coordinates":[` +
		`[[[8,54],[10,54],[10,56],[8,56],[8,54]]],` +
		`[[[12,54],[14,54],[14,56],[12,56],[12,54]]]` +
		`]}}]}`
	set, err := boundary.Read(strings.NewReader(doc), "country", false)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	for _, c := range []struct {
		name     string
		lat, lon float64
	}{
		{"the first part", 55, 9},
		{"the second part", 55, 13},
	} {
		if a, ok := set.At(c.lat, c.lon); !ok || a.Name != "Denmark" {
			t.Errorf("%s: got (%q, %v), want Denmark, true", c.name, a.Name, ok)
		}
	}
	// And the gap between them is not inside either.
	if _, ok := set.At(55, 11); ok {
		t.Error("a point between the two parts was reported inside")
	}
}

// TestRead_PrefersTheEnglishNameWhenSeveralAreCarried pins the order in
// nameKeys.
//
// Natural Earth carries NAME, NAME_EN, NAME_LONG and ADMIN at once and they
// differ for a good many countries. NAME is sometimes the local-script
// spelling, and this package has no language parameter for a caller to ask
// with, so the English form is the one that can be relied on to render. A
// reorder in either direction changes the displayed name for real countries,
// and mutation testing found the order completely unexercised.
func TestRead_PrefersTheEnglishNameWhenSeveralAreCarried(t *testing.T) {
	square := `"geometry":{"type":"Polygon","coordinates":[[[0,0],[2,0],[2,2],[0,2],[0,0]]]}`

	for _, c := range []struct {
		name  string
		props string
		want  string
	}{
		{"all of them", `{"NAME_EN":"English","NAME":"Local","NAME_LONG":"Long","ADMIN":"Admin"}`, "English"},
		{"no English form", `{"NAME":"Local","NAME_LONG":"Long"}`, "Local"},
		{"only a late fallback", `{"ADMIN":"Admin"}`, "Admin"},
	} {
		t.Run(c.name, func(t *testing.T) {
			set, err := boundary.Read(strings.NewReader(
				`{"type":"FeatureCollection","features":[{"properties":`+c.props+`,`+square+`}]}`), "country", false)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			a, ok := set.At(1, 1)
			if !ok {
				t.Fatal("the square did not contain its own middle")
			}
			if a.Name != c.want {
				t.Errorf("Name = %q, want %q", a.Name, c.want)
			}
		})
	}
}

// TestRead_IgnoresAGeometryWithNoArea covers the documented "not an error"
// path.
//
// A file may carry a point or a line for something with no area. This package
// has nothing to say about those, which is different from being unable to read
// the file -- and the difference decides whether one odd record makes the
// whole download useless.
func TestRead_IgnoresAGeometryWithNoArea(t *testing.T) {
	doc := `{"type":"FeatureCollection","features":[` +
		`{"properties":{"NAME":"A Point"},"geometry":{"type":"Point","coordinates":[1,1]}},` +
		`{"properties":{"NAME":"An Area"},"geometry":{"type":"Polygon","coordinates":[[[0,0],[2,0],[2,2],[0,2],[0,0]]]}}` +
		`]}`
	set, err := boundary.Read(strings.NewReader(doc), "country", false)
	if err != nil {
		t.Fatalf("a file carrying a point alongside an area was refused: %v", err)
	}
	if set.Len() != 1 {
		t.Errorf("Len = %d, want 1: the point should be skipped and the area kept", set.Len())
	}
	if a, _ := set.At(1, 1); a.Name != "An Area" {
		t.Errorf("At returned %q, want An Area", a.Name)
	}
}

// writeStore puts a two-file boundary store on disk, as a real fetch would.
func writeStore(t *testing.T, detail, countries, regions, waters string) string {
	t.Helper()
	root := t.TempDir()
	dir := boundary.Dir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for _, f := range []struct {
		name, body string
	}{
		{boundary.File(detail, boundary.Countries), countries},
		{boundary.File(detail, boundary.Regions), regions},
		{boundary.File(detail, boundary.Waters), waters},
	} {
		if f.body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", f.name, err)
		}
	}
	return root
}

func squareDoc(name string, west, south, east, north float64) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return `{"type":"FeatureCollection","features":[{"properties":{"NAME":"` + name +
		`"},"geometry":{"type":"Polygon","coordinates":[[` +
		`[` + f(west) + `,` + f(south) + `],[` + f(east) + `,` + f(south) + `],` +
		`[` + f(east) + `,` + f(north) + `],[` + f(west) + `,` + f(north) + `],` +
		`[` + f(west) + `,` + f(south) + `]]]}}]}`
}

// TestSource_AnswersCountryAndRegionIndEPENDENTLYFromDisk is the path the CLI
// actually runs, and it had no coverage at all.
//
// Every other test here calls Read directly or points Source at an empty
// directory, so the load-and-cache path -- the one a real "osmbase locate"
// depends on -- could be gutted to "return nil" with the whole suite green.
// Mutation testing also found that a cache hit returning the wrong set would
// pass: the two files are cached under one map, and swapping which field is
// returned makes a country lookup answer with the region set from the SECOND
// call onward. locate asks for both levels on every invocation, so that path
// fires on the very first real use.
//
// The two areas are given deliberately different names and different squares,
// so an answer from the wrong file is visible rather than coincidental.
func TestSource_AnswersCountryAndRegionIndependentlyFromDisk(t *testing.T) {
	root := writeStore(t, "50m",
		squareDoc("Denmark", 8, 54, 13, 58),
		squareDoc("Midtjylland", 9, 55, 11, 57),
		squareDoc("Kattegat", 10, 56, 12, 58))
	s := boundary.Open(root, "50m")

	if !boundary.Available(root, "50m") {
		t.Fatal("a store with both files reports no boundaries available")
	}

	// Twice each, and interleaved, so the cached path is exercised as well as
	// the first load -- the cache-key bug above only appears on the second.
	for round := range 2 {
		if name, kind, _, ok := inside(s, locate.Country, 56, 10); !ok || name != "Denmark" || kind != "country" {
			t.Errorf("round %d: country = (%q, %q, %v), want Denmark, country, true", round, name, kind, ok)
		}
		if name, kind, _, ok := inside(s, locate.Region, 56, 10); !ok || name != "Midtjylland" || kind != "state" {
			t.Errorf("round %d: region = (%q, %q, %v), want Midtjylland, state, true", round, name, kind, ok)
		}
		// Inside the country and outside the region, which no single file can
		// answer correctly if the two are being confused.
		if _, _, _, ok := inside(s, locate.Region, 54.5, 12.5); ok {
			t.Errorf("round %d: a point outside the region was reported inside it", round)
		}
		if _, _, _, ok := inside(s, locate.Country, 54.5, 12.5); !ok {
			t.Errorf("round %d: a point inside the country was reported outside it", round)
		}
	}
}

// TestSource_ACorruptFileLosesItsLevelAndNothingElse covers the failure the
// temp-file-and-rename exists to prevent, for when it happens anyway.
//
// A file that survives the existence check and then fails to parse must cost
// its own level and no other. Reporting it as a hard error would lose a
// perfectly good country answer to a damaged region file; ignoring the parse
// error and caching an empty set would report every coordinate on earth as
// outside every region, which is indistinguishable from the sea.
func TestSource_ACorruptFileLosesItsLevelAndNothingElse(t *testing.T) {
	root := writeStore(t, "50m", squareDoc("Denmark", 8, 54, 13, 58), `{"features":[`,
		squareDoc("Kattegat", 10, 56, 12, 58))
	s := boundary.Open(root, "50m")

	if _, _, _, ok := inside(s, locate.Region, 56, 10); ok {
		t.Error("a truncated region file answered a containment question")
	}
	if name, _, _, ok := inside(s, locate.Country, 56, 10); !ok || name != "Denmark" {
		t.Errorf("country = (%q, %v) with a broken region file alongside; one damaged file must not cost the other", name, ok)
	}
}

// TestAvailable_NeedsBothFiles covers the half-finished download.
//
// The two files are fetched as separate requests, so an interruption between
// them leaves one. Reporting that store as having boundaries wires them in,
// and the precedence rule then WITHHOLDS the region answer rather than falling
// back to the tiles -- so a user loses a level they would otherwise have had,
// silently, from an interruption the download's own design anticipates.
func TestAvailable_NeedsBothFiles(t *testing.T) {
	all := writeStore(t, "50m", squareDoc("A", 0, 0, 1, 1), squareDoc("B", 0, 0, 1, 1), squareDoc("C", 0, 0, 1, 1))
	if !boundary.Available(all, "50m") {
		t.Error("a complete store reports unavailable")
	}
	for _, c := range []struct {
		name                      string
		countries, regions, water string
	}{
		{"only the country file", squareDoc("A", 0, 0, 1, 1), "", ""},
		{"missing the water file", squareDoc("A", 0, 0, 1, 1), squareDoc("B", 0, 0, 1, 1), ""},
	} {
		if boundary.Available(writeStore(t, "50m", c.countries, c.regions, c.water), "50m") {
			t.Errorf("a store %s reports available; an interrupted fetch would silently cost a level", c.name)
		}
	}
}

// TestValidDetail_RefusesAnythingThatCouldLeaveTheStore is the check that
// closes a path traversal.
//
// The detail is interpolated into a filename and then joined to the store's
// boundary directory, so a value carrying ".." escapes it. That was
// reproduced: with the store's own boundaries directory EMPTY, a crafted
// --detail made locate read a planted file from outside the store and print
// its contents as a country name. The boundaries command validated its flag
// and the locate command did not, which is why the check now lives here
// instead of in a caller that has to remember it.
func TestValidDetail_RefusesAnythingThatCouldLeaveTheStore(t *testing.T) {
	for _, ok := range boundary.Details {
		if !boundary.ValidDetail(ok) {
			t.Errorf("ValidDetail(%q) = false, want true", ok)
		}
	}
	if !boundary.ValidDetail("") {
		t.Error(`ValidDetail("") = false; empty means "the default"`)
	}
	for _, bad := range []string{
		"10m/../../outside/x",
		"../..",
		"/etc/passwd",
		"10",
		"10M",
	} {
		if boundary.ValidDetail(bad) {
			t.Errorf("ValidDetail(%q) = true", bad)
		}
	}

	// And a refused detail yields a source that answers nothing rather than
	// one pointed somewhere unexpected.
	s := boundary.Open(t.TempDir(), "../..")
	if _, _, _, ok := inside(s, locate.Country, 0, 0); ok {
		t.Error("a source opened with an invalid detail answered a containment question")
	}
}

// TestSource_IsSafeToShareBetweenGoroutines covers the shape this type is
// built to have.
//
// Open's whole point is that a source is loaded once and reused, which is the
// shape a consumer shares: locate.AtEach exists for routes of thousands of
// points, and a caller processing several routes at once would naturally hand
// them one Source. Before the lock, two goroutines reaching the lazy load
// wrote the same map concurrently -- which Go turns into a crash, not a race
// to argue about.
//
// Only meaningful under -race, which "make race" runs; without it this is a
// smoke test that nothing deadlocks.
func TestSource_IsSafeToShareBetweenGoroutines(t *testing.T) {
	root := writeStore(t, "50m",
		squareDoc("Denmark", 8, 54, 13, 58),
		squareDoc("Midtjylland", 9, 55, 11, 57),
		squareDoc("Kattegat", 10, 56, 12, 58))
	s := boundary.Open(root, "50m")

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			level := locate.Country
			if i%2 == 1 {
				level = locate.Region
			}
			for range 20 {
				inside(s, level, 56, 10)
			}
		}(i)
	}
	wg.Wait()

	// And it still answers correctly afterwards, so a lock that serialised
	// everything into nonsense would show up too.
	if name, _, _, ok := inside(s, locate.Country, 56, 10); !ok || name != "Denmark" {
		t.Errorf("after concurrent use, country = (%q, %v), want Denmark, true", name, ok)
	}
}

// TestSet_TheSmallestContainingAreaWins is what makes a marine answer useful.
//
// Seas nest inside oceans: the Tasman Sea is inside the South Pacific, and the
// ocean sits earlier in Natural Earth's file. Taking the first containing area
// reported a trans-Tasman flight as being over the Pacific Ocean -- true, and
// not the answer anybody wanted.
//
// Both orders are asserted because a single order passes by luck: with the
// small area listed first, "keep the first" and "keep the smallest" agree.
func TestSet_TheSmallestContainingAreaWins(t *testing.T) {
	ocean := `{"properties":{"name":"South Pacific Ocean"},"geometry":{"type":"Polygon","coordinates":[[[0,0],[40,0],[40,40],[0,40],[0,0]]]}}`
	sea := `{"properties":{"name":"Tasman Sea"},"geometry":{"type":"Polygon","coordinates":[[[10,10],[20,10],[20,20],[10,20],[10,10]]]}}`

	for _, c := range []struct {
		name  string
		feats string
	}{
		{"the ocean listed first", ocean + `,` + sea},
		{"the sea listed first", sea + `,` + ocean},
	} {
		t.Run(c.name, func(t *testing.T) {
			set, err := boundary.Read(strings.NewReader(
				`{"type":"FeatureCollection","features":[`+c.feats+`]}`), "water", false)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			// Inside both.
			if a, ok := set.At(15, 15); !ok || a.Name != "Tasman Sea" {
				t.Errorf("got %q, want Tasman Sea: the smaller of two areas that both contain the point", a.Name)
			}
			// Inside the ocean only.
			if a, ok := set.At(30, 30); !ok || a.Name != "South Pacific Ocean" {
				t.Errorf("got %q, want South Pacific Ocean", a.Name)
			}
		})
	}
}

// TestRead_PrefersASpecificNameOverAShoutedOrGenericOne covers the two ways
// Natural Earth's marine names differ from its admin ones.
//
// Its lowercase "name" is the SPECIFIC form and "name_en" the generic --
// "South Pacific Ocean" against "Pacific Ocean", for 71 of 306 features --
// which is the opposite of the admin files, where the English key is the one
// to trust. And it shouts the largest features, because that is how an ocean
// is labelled on a map and not how a sentence names one.
func TestRead_PrefersASpecificNameOverAShoutedOrGenericOne(t *testing.T) {
	square := `"geometry":{"type":"Polygon","coordinates":[[[0,0],[2,0],[2,2],[0,2],[0,0]]]}`

	for _, c := range []struct {
		name  string
		props string
		want  string
	}{
		{"specific over generic", `{"name":"South Pacific Ocean","name_en":"Pacific Ocean"}`, "South Pacific Ocean"},
		{"shouted gives way to cased", `{"name":"INDIAN OCEAN","name_en":"Indian Ocean"}`, "Indian Ocean"},
		{"shouted is kept when it is all there is", `{"name":"SOUTHERN OCEAN"}`, "SOUTHERN OCEAN"},
		{"capitals inside a name are not shouting", `{"name":"Bay of Biscay","name_en":"Generic Bay"}`, "Bay of Biscay"},
		{"admin files still prefer the English key", `{"NAME":"Danmark","NAME_EN":"Denmark"}`, "Denmark"},
	} {
		t.Run(c.name, func(t *testing.T) {
			set, err := boundary.Read(strings.NewReader(
				`{"type":"FeatureCollection","features":[{"properties":`+c.props+`,`+square+`}]}`), "water", false)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			a, ok := set.At(1, 1)
			if !ok {
				t.Fatal("the square did not contain its own middle")
			}
			if a.Name != c.want {
				t.Errorf("Name = %q, want %q", a.Name, c.want)
			}
		})
	}
}

// TestRead_TakesTheFeaturesOwnKindOnlyWhenAsked keeps Natural Earth's
// internal vocabulary out of the answer.
//
// The marine file's featurecla is worth reading -- "ocean", "strait", "bay"
// are the words a reader wants. The admin files' says "Admin-0 country",
// which is jargon, and shipped in the output for exactly one commit.
func TestRead_TakesTheFeaturesOwnKindOnlyWhenAsked(t *testing.T) {
	doc := func(cla string) string {
		return `{"type":"FeatureCollection","features":[{"properties":{"name":"X","featurecla":"` + cla +
			`"},"geometry":{"type":"Polygon","coordinates":[[[0,0],[2,0],[2,2],[0,2],[0,0]]]}}]}`
	}

	set, err := boundary.Read(strings.NewReader(doc("strait")), "water", true)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if a, _ := set.At(1, 1); a.Kind != "strait" {
		t.Errorf("Kind = %q, want strait: the marine file's own class is the useful one", a.Kind)
	}

	set, err = boundary.Read(strings.NewReader(doc("Admin-0 country")), "country", false)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if a, _ := set.At(1, 1); a.Kind != "country" {
		t.Errorf("Kind = %q, want country: an admin file's own class is Natural Earth's jargon", a.Kind)
	}
}

// inside is Contains with the answer folded to "an area holds the point".
func inside(s *boundary.Source, l locate.Level, lat, lon float64) (name, kind, credit string, ok bool) {
	name, kind, credit, c := s.Contains(l, lat, lon)
	return name, kind, credit, c == locate.Inside
}
