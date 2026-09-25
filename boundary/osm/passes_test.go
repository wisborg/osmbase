package osm

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/osmpbf"
)

// The tests in this file close the paths a fixture of one tidy boundary
// leaves untouched: a relation that names its ways in the order a mapper
// arranged them rather than in id order, a border way two neighbours share, a
// node that really is at 0,0, and a read that fails on the second or third
// pass rather than the first.

// A relation lists its members in the order a mapper arranged them, which
// bears no relation to the order the file stores the ways in -- OSM way ids
// are assigned by time of creation, so a boundary's members are essentially
// shuffled. Every fixture in pipeline_test.go happens to name its ways in
// ascending id order, which is the one arrangement in which pass 2's binary
// search works whether or not the wanted set was ever sorted.
//
// The coordinates are derived from the fixture's own parameters below: way w
// is given the nodes 10w and 10w+1, at latitude 55 + w/100 and longitude
// 9 + w/100 and a tenth further along.
func TestWaysAreResolvedWhateverOrderTheRelationNamesThem(t *testing.T) {
	order := []int64{30, 12, 21} // neither ascending nor descending

	e := osmbasetest.NewExtract()
	// Nodes and ways go into the file in ascending id order, as a real
	// extract holds them, so only the relation's member list is shuffled.
	for _, w := range []int64{12, 21, 30} {
		e.Node(10*w, latFor(w), lonFor(w))
		e.Node(10*w+1, latFor(w)+0.1, lonFor(w)+0.1)
		e.Way(w, []int64{10 * w, 10*w + 1})
	}
	var members []osmbasetest.ExtractMember
	for _, w := range order {
		members = append(members, osmbasetest.ExtractMember{Type: "way", ID: w, Role: "outer"})
	}
	e.Relation(100, members, "boundary", "administrative", "admin_level", "8", "name", "Horsens")

	got, err := Read(from(e.Bytes()), Options{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || len(got[0].Ways) != len(order) {
		t.Fatalf("got %+v, want one boundary of %d ways", got, len(order))
	}
	for i, w := range order {
		way := got[0].Ways[i]
		if way.ID != w {
			t.Errorf("way %d of the outline is %d, want %d in the order the relation names them", i, way.ID, w)
		}
		want := []Point{{latFor(w), lonFor(w)}, {latFor(w) + 0.1, lonFor(w) + 0.1}}
		if !samePoints(way.Points, want) {
			t.Errorf("way %d has points %v, want %v -- its own geometry, not another way's", w, way.Points, want)
		}
	}
}

func latFor(way int64) float64 { return 55 + float64(way)/100 }
func lonFor(way int64) float64 { return 9 + float64(way)/100 }

func samePoints(got, want []Point) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if math.Abs(got[i].Lat-want[i].Lat) > 1e-7 || math.Abs(got[i].Lon-want[i].Lon) > 1e-7 {
			return false
		}
	}
	return true
}

// Two administrative areas that touch share the whole way along their common
// border, and the extract holds that way once. It must reach both outlines --
// and it must not be mistaken for the other thing that puts one way id in
// front of pass 2 twice, which is a file that holds the way twice and IS
// refused (TestAWayHeldTwiceIsRefused). The two cases are one line apart in
// the code and opposite in their answers.
//
// The two copies must also not share a backing array. Part 5 reverses the
// ways that run the wrong way round a ring, in place, and a reversal of one
// neighbour's border that also reversed the other's would produce a ring that
// fails to close in a file that is perfectly good.
func TestABorderWaySharedByTwoNeighboursReachesBothOutlines(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 55.70, 9.50).
		Node(2, 55.80, 9.50).
		Node(3, 55.70, 9.40).
		Node(4, 55.80, 9.60).
		Way(10, []int64{1, 2}). // the shared border
		Way(11, []int64{2, 3}).
		Way(12, []int64{2, 4})
	shared := osmbasetest.ExtractMember{Type: "way", ID: 10, Role: "outer"}
	e.Relation(100, []osmbasetest.ExtractMember{shared, {Type: "way", ID: 11, Role: "outer"}},
		"boundary", "administrative", "admin_level", "8", "name", "West")
	e.Relation(101, []osmbasetest.ExtractMember{shared, {Type: "way", ID: 12, Role: "outer"}},
		"boundary", "administrative", "admin_level", "8", "name", "East")

	got, err := Read(from(e.Bytes()), Options{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d boundaries, want 2", len(got))
	}

	border := []Point{{55.70, 9.50}, {55.80, 9.50}}
	var shares []*Way
	for i := range got {
		found := false
		for j := range got[i].Ways {
			w := &got[i].Ways[j]
			if w.ID != 10 {
				continue
			}
			found = true
			shares = append(shares, w)
			if !samePoints(w.Points, border) {
				t.Errorf("%s got the shared border as %v, want %v", got[i].Name, w.Points, border)
			}
		}
		if !found {
			t.Errorf("%s did not get the border way it shares with its neighbour", got[i].Name)
		}
	}
	if len(shares) != 2 {
		t.Fatalf("the shared way reached %d outlines, want 2", len(shares))
	}

	// Reversing one neighbour's copy must leave the other's alone.
	first := shares[0].Points
	first[0], first[1] = first[1], first[0]
	if !samePoints(shares[1].Points, border) {
		t.Errorf("reversing one neighbour's copy of the border changed the other's to %v; the two share a backing array",
			shares[1].Points)
	}
}

// A relation that names the same way twice -- a mapper's duplicate, or a way
// used in two roles -- is the third case that puts one id in front of pass 2
// twice, and it is not an error either. Both entries carry the geometry.
func TestARelationNamingOneWayTwiceGetsItTwice(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 55.70, 9.50).Node(2, 55.80, 9.50).
		Way(10, []int64{1, 2})
	e.Relation(100, []osmbasetest.ExtractMember{
		{Type: "way", ID: 10, Role: "outer"},
		{Type: "way", ID: 10, Role: "inner"},
	}, "boundary", "administrative", "admin_level", "8", "name", "Horsens")

	got, err := Read(from(e.Bytes()), Options{})
	if err != nil {
		t.Fatalf("Read: %v; a relation naming a way twice is not a file holding it twice", err)
	}
	if len(got[0].Ways) != 2 {
		t.Fatalf("the outline has %d ways, want both of the relation's entries", len(got[0].Ways))
	}
	want := []Point{{55.70, 9.50}, {55.80, 9.50}}
	for i, w := range got[0].Ways {
		if !samePoints(w.Points, want) {
			t.Errorf("entry %d has points %v, want %v", i, w.Points, want)
		}
	}
	if got[0].Ways[0].Role != "outer" || got[0].Ways[1].Role != "inner" {
		t.Errorf("roles came back %q,%q, want outer,inner -- each entry keeps its own role",
			got[0].Ways[0].Role, got[0].Ways[1].Role)
	}
}

// The pipeline refuses a node the extract does not hold BECAUSE filling it
// with the zero value would put it at 0,0, which is a real place. That
// reasoning only holds if 0,0 is reachable as a coordinate, and a presence
// flag is what makes the two distinguishable: a pass 3 that decided presence
// by testing points[i] against the zero Point would refuse a file that is
// entirely correct, and would pass every other test in this package.
//
// The paired test is TestANodeMissingFromTheExtractIsAnError. Between them,
// an implementation that reported everything present fails the first and one
// that reported anything zero-valued absent fails this.
func TestANodeAtTheOriginIsAPlaceAndNotAnAbsence(t *testing.T) {
	for _, tc := range []struct {
		name string
		lat  float64
		lon  float64
	}{
		{"null island", 0, 0},
		{"on the equator", 0, 9.5},
		{"on the prime meridian", 55.7, 0},
		{"south and west of both", -33.86, -70.67},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := osmbasetest.NewExtract().
				Node(1, tc.lat, tc.lon).
				Node(2, tc.lat+0.1, tc.lon+0.1).
				Way(10, []int64{1, 2})
			e.Relation(100, []osmbasetest.ExtractMember{{Type: "way", ID: 10, Role: "outer"}},
				"boundary", "administrative", "admin_level", "8", "name", "Somewhere")

			got, err := Read(from(e.Bytes()), Options{})
			if err != nil {
				t.Fatalf("Read: %v; the node is in the extract, at %v,%v", err, tc.lat, tc.lon)
			}
			want := []Point{{tc.lat, tc.lon}, {tc.lat + 0.1, tc.lon + 0.1}}
			if !samePoints(got[0].Ways[0].Points, want) {
				t.Errorf("points = %v, want %v", got[0].Ways[0].Points, want)
			}
		})
	}
}

// The extract is opened once per pass, and a file can stop being readable
// between them -- a download that is still running, a stream that breaks, a
// temporary file something else deleted. A pass that swallowed the failure
// would return the boundaries it had got so far, with the geometry of the
// pass that failed silently missing: an outline with no ways, or ways with no
// points, and no error to say why.
func TestAFailureOpeningTheExtractOnAnyPassIsReported(t *testing.T) {
	file := square(t)

	for pass := 1; pass <= 3; pass++ {
		t.Run(fmt.Sprintf("pass %d", pass), func(t *testing.T) {
			want := errors.New("the extract went away")
			calls := 0
			open := func() (io.ReadCloser, error) {
				calls++
				if calls == pass {
					return nil, want
				}
				return io.NopCloser(bytes.NewReader(file)), nil
			}
			got, err := Read(open, Options{})
			if !errors.Is(err, want) {
				t.Fatalf("Read = %+v, %v; want the opener's error from pass %d", got, err, pass)
			}
			if calls != pass {
				t.Errorf("the pipeline opened the extract %d times before reporting, want %d", calls, pass)
			}
		})
	}
}

// A file that cannot be read past a point is not a file with no boundaries in
// it. Swallowed, a short read reports an extract of whatever was decoded
// before the damage -- which for a truncated relation block is
// ErrNoBoundaries, telling the caller their region has no administrative
// areas when what happened is that their download is incomplete.
func TestDamageInTheFileIsReportedNotReadAsAnEmptyExtract(t *testing.T) {
	good := square(t)

	for _, tc := range []struct {
		name string
		file []byte
	}{
		{
			// Cut inside the last blob, which is the relations. Pass 1 walks
			// into the cut.
			name: "truncated mid-blob",
			file: good[:len(good)-5],
		},
		{
			// A well-framed OSMData blob whose payload is not a decodable
			// PrimitiveBlock: field 1 declares 255 bytes of string table and
			// the block ends. Framing is fine, so this reaches the decode.
			name: "a data blob that is not a primitive block",
			file: append(append([]byte{}, good...), dataBlob(t, []byte{0x0a, 0xff, 0x01})...),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Read(from(tc.file), Options{})
			if err == nil {
				t.Fatalf("Read = %+v, nil; a damaged file was read as a whole one", got)
			}
			if errors.Is(err, ErrNoBoundaries) {
				t.Errorf("Read: %v; damage was reported as a region with no boundaries", err)
			}
		})
	}
}

// dataBlob frames a payload as an OSMData blob, written here from the file
// format's own schema rather than by running osmbasetest or osmpbf backwards:
// a four-byte big-endian BlobHeader length, BlobHeader{1: type, 3: datasize},
// then Blob{2: raw_size, 3: zlib_data}.
func dataBlob(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("deflating: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("deflating: %v", err)
	}

	blob := append([]byte{0x10}, binary.AppendUvarint(nil, uint64(len(payload)))...) // field 2, varint
	blob = append(blob, 0x1a)                                                        // field 3, bytes
	blob = binary.AppendUvarint(blob, uint64(buf.Len()))
	blob = append(blob, buf.Bytes()...)

	header := append([]byte{0x0a, byte(len("OSMData"))}, "OSMData"...) // field 1, bytes
	header = append(header, 0x18)                                      // field 3, varint
	header = binary.AppendUvarint(header, uint64(len(blob)))

	out := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
	out = append(out, header...)
	return append(out, blob...)
}

// The header block is not a primitive block, and handing it to the element
// decoder is undefined at best. It happens to decode to nothing today, which
// is why dropping the check costs no test -- so this counts the walks
// instead, which is what the check is actually for.
//
// The file holds four blobs: one OSMHeader and one OSMData for each of nodes,
// ways and relations. That layout is pinned in osmbasetest by
// TestExtract_BlocksAreOrderedAsARealExtractOrdersThem.
func TestOnlyDataBlocksReachTheWalk(t *testing.T) {
	var walks int
	err := eachBlock(from(square(t)), nil, func(int, *osmpbf.PrimitiveBlock) error {
		walks++
		return nil
	})
	if err != nil {
		t.Fatalf("eachBlock: %v", err)
	}
	if walks != 3 {
		t.Errorf("the walk saw %d blocks, want the 3 OSMData blocks of a 4-blob file", walks)
	}
}

// An error from the walk stops the pass rather than being counted and
// carried on with.
func TestAnErrorFromTheWalkStopsThePass(t *testing.T) {
	want := errors.New("enough")
	var walks int
	err := eachBlock(from(square(t)), nil, func(int, *osmpbf.PrimitiveBlock) error {
		walks++
		return want
	})
	if !errors.Is(err, want) {
		t.Errorf("eachBlock: %v, want the walk's own error", err)
	}
	if walks != 1 {
		t.Errorf("the walk ran %d times after returning an error, want 1", walks)
	}
}

// name:<language> is preferred over name, but an OSM tag exists with an empty
// value as readily as it exists with a real one -- admin_level carries "" in
// the same extract. An empty name:de means the feature has no German name,
// not that it has no name: taken literally it would leave the boundary
// nameless, and a nameless boundary is DROPPED, so the whole area disappears
// from the output of a caller who asked for German.
//
// Paired with TestTheLanguageIsPreferredWhenTheExtractCarriesIt: one says a
// present translation wins, this says a present-but-empty one does not.
func TestAnEmptyTranslationFallsBackToTheLocalName(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 55.70, 9.50).Node(2, 55.80, 9.50).
		Way(10, []int64{1, 2})
	e.Relation(100, []osmbasetest.ExtractMember{{Type: "way", ID: 10, Role: "outer"}},
		"boundary", "administrative", "admin_level", "8", "name", "Horsens", "name:de", "")

	got, err := Read(from(e.Bytes()), Options{Language: "de"})
	if err != nil {
		t.Fatalf("Read: %v; an empty name:de dropped the boundary entirely", err)
	}
	if len(got) != 1 || got[0].Name != "Horsens" {
		t.Errorf("got %+v, want the boundary named Horsens", got)
	}
}

// freeze clips the spare capacity append left behind, and that is not a
// tidiness: the set is built by append over several million ids, so the slice
// arrives holding up to a quarter more memory than it needs, and it is then
// held for the rest of the run. TestIDSetRetainedSize measures the same thing
// through the allocator, where a quarter is inside its tolerance.
func TestAFrozenSetKeepsNoSpareCapacity(t *testing.T) {
	s := newIDSet(1 << 20)
	for i := 0; i < 5000; i++ {
		if err := s.add(int64(i % 4000)); err != nil { // some duplicates, as a real pass has
			t.Fatalf("add: %v", err)
		}
	}
	if cap(s.ids) <= len(s.ids) {
		t.Fatalf("append left no spare capacity to clip (len %d, cap %d); the test proves nothing",
			len(s.ids), cap(s.ids))
	}
	s.freeze()
	if cap(s.ids) != len(s.ids) {
		t.Errorf("a frozen set of %d ids holds capacity for %d", len(s.ids), cap(s.ids))
	}
}

// DefaultLimits are what every caller who does not set Limits gets, and
// nothing else in the suite constrains their magnitude: every fixture is a
// handful of elements, so the defaults could be cut to single figures and the
// package would stay green while refusing every real extract with an error
// about a limit the caller never chose.
//
// The floor here is the measurement in docs/locate.md rather than the values
// the file happens to hold: Denmark carries on the order of a few thousand
// administrative relations whose boundaries reference a few million distinct
// nodes, and the comment on DefaultLimits promises an order of magnitude of
// headroom on that. A country extract is what this pipeline is for, so the
// defaults have to clear it with room, not merely reach it.
func TestTheDefaultLimitsClearACountryExtractWithHeadroom(t *testing.T) {
	const (
		relationsInACountry = 3_000     // "a few thousand"
		nodesInACountry     = 3_000_000 // "a few million distinct"
		waysInACountry      = 300_000   // between the two, by inspection of what names what
		headroom            = 10        // "an order of magnitude", from the doc comment
	)

	for _, tc := range []struct {
		what string
		got  int
		want int
	}{
		{"boundaries", DefaultLimits().Boundaries, relationsInACountry * headroom},
		{"ways", DefaultLimits().Ways, waysInACountry * headroom},
		{"nodes", DefaultLimits().Nodes, nodesInACountry * headroom},
	} {
		if tc.got < tc.want {
			t.Errorf("DefaultLimits().%s is %d, want at least %d -- a country extract plus the headroom promised",
				tc.what, tc.got, tc.want)
		}
	}
}
