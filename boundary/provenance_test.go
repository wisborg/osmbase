package boundary

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func oneArea() []Area {
	return []Area{NewArea("Horsens", "7", []Polygon{{Outer: square(55.8, 9.8, 0.2)}})}
}

// The file is a Derivative Database under the ODbL, so what it says about
// itself has to travel with it: a recipient holding only geometry has no way
// to know what licence it arrived under, and telling the person who built it
// tells the one person who already knows.
func TestProvenanceTravelsWithTheFile(t *testing.T) {
	want := Provenance{
		Source:      "denmark-latest.osm.pbf",
		Attribution: "© OpenStreetMap contributors, ODbL",
		Created:     time.Date(2026, 9, 23, 11, 22, 33, 0, time.UTC),
		Levels:      []int{7, 8, 9},
	}

	var buf bytes.Buffer
	if err := WriteDerived(&buf, NewSet(want, oneArea())); err != nil {
		t.Fatalf("WriteDerived: %v", err)
	}
	got, err := ReadDerived(&buf)
	if err != nil {
		t.Fatalf("ReadDerived: %v", err)
	}

	p := got.Provenance()
	if p.Source != want.Source {
		t.Errorf("source = %q, want %q", p.Source, want.Source)
	}
	if p.Attribution != want.Attribution {
		t.Errorf("attribution = %q, want %q -- the licence obligation did not survive", p.Attribution, want.Attribution)
	}
	if !p.Created.Equal(want.Created) {
		t.Errorf("created = %v, want %v", p.Created, want.Created)
	}
	if !slices.Equal(p.Levels, want.Levels) {
		t.Errorf("levels = %v, want %v", p.Levels, want.Levels)
	}
}

// A set built in memory or read from GeoJSON says nothing about itself, and
// that must read back as nothing rather than as a wrong claim.
func TestAnEmptyProvenanceSurvivesAsEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDerived(&buf, NewSet(Provenance{}, oneArea())); err != nil {
		t.Fatalf("WriteDerived: %v", err)
	}
	got, err := ReadDerived(&buf)
	if err != nil {
		t.Fatalf("ReadDerived: %v", err)
	}
	if p := got.Provenance(); p.Source != "" || p.Attribution != "" || !p.Created.IsZero() || len(p.Levels) != 0 {
		t.Errorf("provenance = %+v, want the zero value", p)
	}
}

// The header is length-prefixed so a later osmbase can add a field to it and
// a reader of this version steps over what it does not know. Without that,
// the only way to add provenance later is a version bump, and a version bump
// makes every file on disk a fresh download of a country extract.
func TestAHeaderFieldThisVersionDoesNotKnowIsSteppedOver(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDerived(&buf, NewSet(Provenance{Source: "s", Levels: []int{9}}, oneArea())); err != nil {
		t.Fatalf("WriteDerived: %v", err)
	}
	file := buf.Bytes()

	// Splice four extra bytes onto the end of the header block, as a later
	// version adding a field would.
	headerAt := len(derivedMagic) + 1
	n, w := binary.Uvarint(file[headerAt:])
	extra := []byte{0xde, 0xad, 0xbe, 0xef}

	grown := append([]byte{}, file[:headerAt]...)
	grown = binary.AppendUvarint(grown, n+uint64(len(extra)))
	grown = append(grown, file[headerAt+w:headerAt+w+int(n)]...)
	grown = append(grown, extra...)
	grown = append(grown, file[headerAt+w+int(n):]...)

	got, err := ReadDerived(bytes.NewReader(grown))
	if err != nil {
		t.Fatalf("ReadDerived on a file with a later version's header field: %v", err)
	}
	if got.Provenance().Source != "s" {
		t.Errorf("source = %q, want the fields this version does know", got.Provenance().Source)
	}
	if got.Len() != 1 {
		t.Errorf("read %d areas, want 1", got.Len())
	}
}

// A nameless area cannot answer the question this package exists for, and
// both other readers here refuse one. Accepted, a corrupt file yields an
// area that answers containment true and names nowhere.
func TestANamelessAreaIsRefusedAtBothEnds(t *testing.T) {
	nameless := []Area{NewArea("", "k", []Polygon{{Outer: square(0, 0, 1)}})}

	if err := WriteDerived(&bytes.Buffer{}, NewSet(Provenance{}, nameless)); err == nil {
		t.Error("a nameless area was written")
	}

	// And on the read, where the bytes may not have come from this writer.
	// A COMPLETE file in every other way, so that removing the check leaves
	// something that reads cleanly rather than something that fails later.
	file := append([]byte(derivedMagic), derivedVersion, 4, 0, 0, 0, 0)
	file = append(file, 1)       // one area
	file = append(file, 0)       // a name of zero bytes
	file = append(file, 1, 'k')  // a kind
	file = append(file, 1, 1, 3) // one polygon, one ring, three points
	for range 3 {
		file = append(file, 2, 2) // small deltas, well inside the world
	}
	if _, err := ReadDerived(bytes.NewReader(file)); !errors.Is(err, ErrDerivedFormat) {
		t.Errorf("ReadDerived: %v, want a nameless area refused", err)
	}
}

// The deltas accumulate in int64 and wrap silently. A ring whose accumulated
// position leaves the coordinate system gives an area whose bounding box
// spans most of the plane -- and because Set.At prefers the smallest
// containing box, it wins exactly where the honest answer is "nowhere".
func TestAVertexOffTheEarthIsRefused(t *testing.T) {
	file := append([]byte(derivedMagic), derivedVersion, 4, 0, 0, 0, 0)
	file = append(file, 1)       // one area
	file = append(file, 1, 'n')  // name
	file = append(file, 1, 'k')  // kind
	file = append(file, 1, 1, 1) // one polygon, one ring, one point
	// A latitude delta of half the int64 range.
	file = binary.AppendUvarint(file, uint64(1)<<62)
	file = binary.AppendUvarint(file, 0)

	_, err := ReadDerived(bytes.NewReader(file))
	if !errors.Is(err, ErrDerivedFormat) {
		t.Fatalf("ReadDerived: %v, want ErrDerivedFormat", err)
	}
	if !strings.Contains(err.Error(), "off the Earth") {
		t.Errorf("ReadDerived: %v, want the coordinate named", err)
	}
}

// Nothing may follow the last area. The realistic way to get here is a
// rewrite in place that did not truncate, leaving the tail of a longer
// previous file behind -- which would otherwise read clean.
func TestBytesAfterTheLastAreaAreRefused(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDerived(&buf, NewSet(Provenance{}, oneArea())); err != nil {
		t.Fatalf("WriteDerived: %v", err)
	}
	file := append(buf.Bytes(), []byte("the tail of a longer file")...)

	if _, err := ReadDerived(bytes.NewReader(file)); !errors.Is(err, ErrDerivedFormat) {
		t.Errorf("ReadDerived: %v, want trailing bytes refused", err)
	}
}

// Per-item limits do not bound the file as a whole: every structure costs a
// header whether or not it holds anything, so a file of areas each declaring
// a million EMPTY polygons costs one byte per polygon and yields eighty
// bytes of slice and struct.
//
// An error is not the property. The bound is, so this measures it -- which
// is the shape docs/locate.md names after the same defect appeared four
// times elsewhere.
func TestAFileCannotAllocateInProportionToWhatItDeclares(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a multi-megabyte file")
	}

	// One area declaring a great many empty polygons: one byte each.
	const polygons = 1 << 21
	file := append([]byte(derivedMagic), derivedVersion, 4, 0, 0, 0, 0)
	file = append(file, 1, 1, 'n', 1, 'k')
	file = binary.AppendUvarint(file, polygons)
	for range polygons {
		file = append(file, 0) // a polygon of no rings
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	set, err := ReadDerived(bytes.NewReader(file))
	runtime.ReadMemStats(&after)
	grew := after.TotalAlloc - before.TotalAlloc

	// Whatever it does with the file, what it may not do is spend in
	// proportion to what the file asked for. Refusing is a fine answer and
	// so is reading it; allocating eighty bytes per declared byte is not.
	if grew > 64<<20 {
		t.Errorf("a %.1f MB file allocated %.1f MB (err=%v)",
			float64(len(file))/(1<<20), float64(grew)/(1<<20), err)
	}
	runtime.KeepAlive(set)
}

// Every limit the reader enforces, the writer enforces too. Otherwise a set
// writes successfully and then cannot be loaded -- a file nobody can read,
// discovered on a different day than it was produced.
func TestTheWriterRefusesWhatTheReaderWould(t *testing.T) {
	big := make([]Area, 0, 4)
	for range 4 {
		big = append(big, NewArea("n", "k", []Polygon{{Outer: square(0, 0, 1)}}))
	}
	if err := WriteDerived(&bytes.Buffer{}, NewSet(Provenance{}, big)); err != nil {
		t.Fatalf("an ordinary set was refused: %v", err)
	}

	levels := make([]int, maxDerivedLevels+1)
	if err := WriteDerived(&bytes.Buffer{}, NewSet(Provenance{Levels: levels}, oneArea())); err == nil {
		t.Error("a provenance declaring more levels than the format holds was written")
	}

	long := strings.Repeat("x", maxDerivedString+1)
	if err := WriteDerived(&bytes.Buffer{}, NewSet(Provenance{Source: long}, oneArea())); err == nil {
		t.Error("a source string past the format's limit was written")
	}
}

// The whole-file budgets, which the per-item limits do not provide: each
// item is proportionate to its own bytes, and the file is not, because an
// empty structure still costs a header.
func TestTheFileAsAWholeIsBounded(t *testing.T) {
	// n empty structures of the given kind, spread one to an area so that
	// no per-item limit is approached.
	build := func(kind byte, n int) []byte {
		const perArea = 1024
		areas := n / perArea
		file := append([]byte(derivedMagic), derivedVersion, 4, 0, 0, 0, 0)
		file = binary.AppendUvarint(file, uint64(areas))
		for range areas {
			file = append(file, 1, 'n', 1, 'k')
			if kind == 'p' {
				file = binary.AppendUvarint(file, perArea)
				for range perArea {
					file = append(file, 0) // a polygon of no rings
				}
				continue
			}
			file = append(file, 1) // one polygon
			file = binary.AppendUvarint(file, perArea)
			for range perArea {
				file = append(file, 0) // a ring of no points
			}
		}
		return file
	}

	for _, tc := range []struct {
		name string
		kind byte
		max  int
	}{
		{"polygons", 'p', maxDerivedTotalPolygons},
		{"rings", 'r', maxDerivedTotalRings},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadDerived(bytes.NewReader(build(tc.kind, tc.max+1024))); err == nil {
				t.Errorf("a file declaring more %s than the format reads was accepted", tc.name)
			}
		})
	}
}

// The writer refuses the whole-file totals too, so it cannot produce a file
// the reader will not load. Built directly rather than through NewArea
// because the point is the count, not the geometry.
func TestTheWriterRefusesAWholeFileTotal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds millions of empty polygons")
	}

	t.Run("polygons", func(t *testing.T) {
		// Spread across several areas, so no area exceeds the per-area
		// limit and only the total is past.
		const perArea = 1 << 19
		areas := make([]Area, 0, 1+maxDerivedTotalPolygons/perArea)
		for len(areas)*perArea <= maxDerivedTotalPolygons {
			areas = append(areas, Area{Name: "n", Kind: "k", polygons: make([]polygon, perArea)})
		}
		if err := WriteDerived(&bytes.Buffer{}, NewSet(Provenance{}, areas)); err == nil {
			t.Errorf("a set of %d polygons was written", len(areas)*perArea)
		}
	})

	t.Run("rings", func(t *testing.T) {
		const perArea = 1 << 19
		areas := make([]Area, 0, 1+maxDerivedTotalRings/perArea)
		for len(areas)*perArea <= maxDerivedTotalRings {
			areas = append(areas, Area{
				Name: "n", Kind: "k",
				polygons: []polygon{{rings: make([]Ring, perArea)}},
			})
		}
		if err := WriteDerived(&bytes.Buffer{}, NewSet(Provenance{}, areas)); err == nil {
			t.Errorf("a set of %d rings was written", len(areas)*perArea)
		}
	})
}
