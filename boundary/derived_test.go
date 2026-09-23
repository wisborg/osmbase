package boundary

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
)

// square returns a ring of the given extent, counterclockwise.
func square(lat, lon, side float64) Ring {
	return Ring{
		{Lat: lat, Lon: lon},
		{Lat: lat, Lon: lon + side},
		{Lat: lat + side, Lon: lon + side},
		{Lat: lat + side, Lon: lon},
	}
}

func TestADerivedFileRoundTrips(t *testing.T) {
	areas := []Area{
		NewArea("Horsens", "7", []Polygon{{
			Outer: square(55.8, 9.8, 0.2),
			Holes: []Ring{square(55.85, 9.85, 0.05)},
		}}),
		NewArea("Hornsby", "9", []Polygon{
			{Outer: square(-33.7, 151.1, 0.1)},
			{Outer: square(-33.5, 151.3, 0.05)}, // a second part
		}),
	}

	var buf bytes.Buffer
	if err := WriteDerived(&buf, NewSet(Provenance{}, areas)); err != nil {
		t.Fatalf("WriteDerived: %v", err)
	}
	got, err := ReadDerived(&buf)
	if err != nil {
		t.Fatalf("ReadDerived: %v", err)
	}

	if got.Len() != 2 {
		t.Fatalf("read %d areas, want 2", got.Len())
	}

	// Containment is the point of the file, so that is what is checked: the
	// same questions must get the same answers on either side of it.
	for _, tc := range []struct {
		name     string
		lat, lon float64
		want     string
	}{
		{"inside the first", 55.82, 9.82, "Horsens"},
		{"inside its hole", 55.87, 9.87, ""},
		{"inside the second", -33.68, 151.15, "Hornsby"},
		{"inside its other part", -33.48, 151.32, "Hornsby"},
		{"outside everything", 0, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ok := got.At(tc.lat, tc.lon)
			switch {
			case tc.want == "" && ok:
				t.Errorf("got %q, want nothing", a.Name)
			case tc.want != "" && !ok:
				t.Errorf("got nothing, want %q", tc.want)
			case tc.want != "" && a.Name != tc.want:
				t.Errorf("got %q, want %q", a.Name, tc.want)
			}
		})
	}
}

// The coordinates are stored as ten-millionths of a degree, which is the
// resolution OpenStreetMap itself uses -- so a coordinate that came from an
// extract must survive the file exactly.
func TestCoordinatesSurviveTheFileAtOsmResolution(t *testing.T) {
	want := Ring{
		{Lat: -33.8688197, Lon: 151.2092955},
		{Lat: 55.8607123, Lon: 9.8503456},
		{Lat: 0.0000001, Lon: -0.0000001},
		{Lat: 89.9999999, Lon: -179.9999999},
	}
	var buf bytes.Buffer
	if err := WriteDerived(&buf, NewSet(Provenance{}, []Area{NewArea("n", "k", []Polygon{{Outer: want}})})); err != nil {
		t.Fatalf("WriteDerived: %v", err)
	}
	got, err := ReadDerived(&buf)
	if err != nil {
		t.Fatalf("ReadDerived: %v", err)
	}
	ring := got.areas[0].polygons[0].rings[0]
	if len(ring) != len(want) {
		t.Fatalf("read %d points, want %d", len(ring), len(want))
	}
	for i := range want {
		// Half a unit of the stored resolution: anything looser would accept
		// a rounding that moves a vertex, and anything tighter would be
		// asserting float equality after a divide.
		if math.Abs(ring[i].Lat-want[i].Lat) > 5e-8 || math.Abs(ring[i].Lon-want[i].Lon) > 5e-8 {
			t.Errorf("point %d = %f,%f, want %f,%f", i, ring[i].Lat, ring[i].Lon, want[i].Lat, want[i].Lon)
		}
	}
}

// The file is on somebody's disk. A version this does not know must be
// refused by name: read as the version it is not, it yields boundaries
// somewhere else, and nothing about a wrong place looks wrong.
func TestAnUnknownVersionIsRefused(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDerived(&buf, NewSet(Provenance{}, []Area{NewArea("n", "k", []Polygon{{Outer: square(0, 0, 1)}})})); err != nil {
		t.Fatalf("WriteDerived: %v", err)
	}
	file := buf.Bytes()
	// The version is the byte after the magic.
	file[len(derivedMagic)] = derivedVersion + 1

	_, err := ReadDerived(bytes.NewReader(file))
	if !errors.Is(err, ErrDerivedFormat) {
		t.Fatalf("ReadDerived: %v, want ErrDerivedFormat", err)
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("ReadDerived: %v, want the version named", err)
	}
}

func TestBytesThatAreNotADerivedFileAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"too short for the magic", []byte("os")},
		{"the wrong magic", []byte("json{}")},
		{"the magic and nothing else", []byte(derivedMagic)},
		{"the magic and a version but no header", append([]byte(derivedMagic), derivedVersion)},
		{"truncated part way through an area", append([]byte(derivedMagic), 1, 1, 3, 'a')},
		// A file that is well formed in every way except that it is not
		// this format. Without the magic it parses cleanly as an empty set,
		// so nothing downstream would report a problem -- it would simply
		// contain no boundaries.
		{"a valid body behind the wrong magic", append([]byte("XXXX"), derivedVersion, 4, 0, 0, 0, 0, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadDerived(bytes.NewReader(tc.data)); !errors.Is(err, ErrDerivedFormat) {
				t.Errorf("ReadDerived: %v, want ErrDerivedFormat", err)
			}
		})
	}
}

// Every count in the file decides an allocation, and the file can be
// truncated or replaced. A declared count past its limit must be refused
// before anything is allocated for it.
func TestADeclaredCountPastItsLimitIsRefusedBeforeAllocating(t *testing.T) {
	for _, tc := range []struct {
		name string
		file []byte
	}{
		{"areas", declaring(uint64(maxDerivedAreas) + 1)},
		{"a name", declaring(1, uint64(maxDerivedString)+1)},
		{"polygons", declaring(1, 1, 'n', 1, 'k', uint64(maxDerivedPolygons)+1)},
		{"rings", declaring(1, 1, 'n', 1, 'k', 1, uint64(maxDerivedRings)+1)},
		{"points", declaring(1, 1, 'n', 1, 'k', 1, 1, uint64(maxDerivedPoints)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadDerived(bytes.NewReader(tc.file))
			if !errors.Is(err, ErrDerivedFormat) {
				t.Fatalf("ReadDerived: %v, want ErrDerivedFormat", err)
			}
			if !strings.Contains(err.Error(), "at most") {
				t.Errorf("ReadDerived: %v, want a refusal naming the limit", err)
			}
		})
	}
}

// declaring builds a file header followed by the given values as uvarints,
// so a test can say what a file claims without supplying what it claims.
func declaring(vs ...uint64) []byte {
	out := append([]byte(derivedMagic), byte(derivedVersion))
	out = append(out, 4, 0, 0, 0, 0) // an empty header block
	for _, v := range vs {
		out = binary.AppendUvarint(out, v)
	}
	return out
}

// A file with no areas is a valid file. A region really may hold none at the
// level asked for, and refusing it would make an honest answer look broken.
func TestAFileMayHoldNoAreas(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDerived(&buf, nil); err != nil {
		t.Fatalf("WriteDerived: %v", err)
	}
	got, err := ReadDerived(&buf)
	if err != nil {
		t.Fatalf("ReadDerived: %v", err)
	}
	if got.Len() != 0 {
		t.Errorf("read %d areas, want none", got.Len())
	}
	if _, ok := got.At(55, 9); ok {
		t.Error("an empty set contained a point")
	}
}

func TestANameTooLongToStoreIsRefusedOnWrite(t *testing.T) {
	long := strings.Repeat("x", maxDerivedString+1)
	err := WriteDerived(&bytes.Buffer{}, NewSet(Provenance{}, []Area{NewArea(long, "k", []Polygon{{Outer: square(0, 0, 1)}})}))
	if err == nil {
		t.Error("a name past the format's limit was written")
	}
}

// Ring assembly yields outlines and holes in two flat lists, and which hole
// belongs to which outline is a containment question. This is where it is
// answered.
func TestHolesArePairedWithTheOutlineThatHoldsThem(t *testing.T) {
	outer := square(0, 0, 10)
	other := square(0, 20, 10)
	hole := square(2, 2, 1)

	polys, orphans, _ := Polygons([]Ring{outer, other}, []Ring{hole})
	if len(orphans) != 0 {
		t.Fatalf("got %d orphans, want none", len(orphans))
	}
	if len(polys) != 2 {
		t.Fatalf("got %d polygons, want 2", len(polys))
	}
	if len(polys[0].Holes) != 1 {
		t.Errorf("the hole landed on %d of the first outline's holes, want 1", len(polys[0].Holes))
	}
	if len(polys[1].Holes) != 0 {
		t.Errorf("a hole was cut out of the outline that does not contain it")
	}
}

// Outlines nest. An enclave inside an enclave is rare on administrative
// boundaries and not impossible, and taking the first containing outline
// would cut the hole out of the wrong one.
func TestTheSmallestContainingOutlineTakesTheHole(t *testing.T) {
	big := square(0, 0, 100)
	small := square(10, 10, 10)
	hole := square(12, 12, 1)

	polys, orphans, _ := Polygons([]Ring{big, small}, []Ring{hole})
	if len(orphans) != 0 {
		t.Fatalf("got %d orphans, want none", len(orphans))
	}
	if len(polys[0].Holes) != 0 {
		t.Errorf("the hole was cut out of the larger outline, which also contains it")
	}
	if len(polys[1].Holes) != 1 {
		t.Errorf("the hole did not land on the smallest outline containing it")
	}
}

// A hole inside nothing means the outline that should contain it was cut off
// by the edge of the extract. Returned rather than discarded, because that is
// a fact about the data the caller may want to report.
func TestAHoleInsideNothingIsReturnedNotDiscarded(t *testing.T) {
	polys, orphans, _ := Polygons([]Ring{square(0, 0, 1)}, []Ring{square(50, 50, 1)})
	if len(orphans) != 1 {
		t.Errorf("got %d orphans, want 1", len(orphans))
	}
	for _, p := range polys {
		if len(p.Holes) != 0 {
			t.Error("an orphan hole was also cut out of an outline")
		}
	}
}

func TestPairingWithNothingToPair(t *testing.T) {
	polys, orphans, _ := Polygons([]Ring{square(0, 0, 1)}, nil)
	if len(polys) != 1 || len(polys[0].Holes) != 0 || orphans != nil {
		t.Errorf("Polygons with no holes = %+v, %+v", polys, orphans)
	}
	polys, orphans, _ = Polygons(nil, []Ring{square(0, 0, 1)})
	if len(polys) != 0 || len(orphans) != 1 {
		t.Errorf("Polygons with no outlines = %+v, %+v", polys, orphans)
	}
}

// Coordinates are rounded to the stored resolution, not truncated.
// Truncation moves every coordinate toward zero, which is south and west
// across most of the inhabited world -- a systematic shift of up to a
// centimetre in one direction rather than a rounding error in both.
func TestCoordinatesAreRoundedNotTruncated(t *testing.T) {
	// Each of these sits more than half a unit above a whole one, so
	// rounding and truncation disagree.
	want := Ring{
		{Lat: 0.00000016, Lon: 0.00000019},
		{Lat: 1.00000018, Lon: 2.00000017},
		{Lat: -3.00000016, Lon: -4.00000018},
	}
	var buf bytes.Buffer
	if err := WriteDerived(&buf, NewSet(Provenance{}, []Area{NewArea("n", "k", []Polygon{{Outer: want}})})); err != nil {
		t.Fatalf("WriteDerived: %v", err)
	}
	got, err := ReadDerived(&buf)
	if err != nil {
		t.Fatalf("ReadDerived: %v", err)
	}
	ring := got.areas[0].polygons[0].rings[0]
	for i := range want {
		if math.Abs(ring[i].Lat-want[i].Lat) > 5e-8 || math.Abs(ring[i].Lon-want[i].Lon) > 5e-8 {
			t.Errorf("point %d = %.8f,%.8f, want %.8f,%.8f -- rounded to the nearest unit, not toward zero",
				i, ring[i].Lat, ring[i].Lon, want[i].Lat, want[i].Lon)
		}
	}
}
