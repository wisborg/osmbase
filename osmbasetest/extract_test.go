package osmbasetest_test

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
)

// Extract's whole claim is the one the package header makes: it is an
// INDEPENDENT encoder of the PBF format, so that a fixture cannot agree with
// the decoder by construction. Nothing here decodes a fixture with osmpbf.
// Every number below is worked out in the comment above it from the PBF
// schema -- fileformat.proto and osmformat.proto -- and the bytes are
// compared against that.
//
// Before these tests the builder had no assertion of any kind on its output:
// it was reached only through boundary/osm, which decodes it with osmpbf and
// therefore proves only that the two agree. The failures that matter most in
// this format are exactly the ones both sides can share -- a delta
// accumulated the wrong way, a member type numbered from the wrong end, a
// dense tag run that drifts one node out of step.

// blob is one framed block of a PBF file, already inflated.
type blob struct {
	kind    string
	payload []byte
}

// blobs takes a file apart by hand, from fileformat.proto:
//
//	uint32 big-endian    length of the BlobHeader
//	BlobHeader { 1: string type, 3: int32 datasize }
//	Blob       { 2: int32 raw_size, 3: bytes zlib_data }
//
// Strict about the field numbers and the order, because those are part of
// what is being checked: a header that numbered its type field 2 would be
// read by nothing.
func blobs(t *testing.T, file []byte) []blob {
	t.Helper()
	var out []blob
	for len(file) > 0 {
		if len(file) < 4 {
			t.Fatalf("%d bytes left over, too few for a blob header length", len(file))
		}
		n := int(binary.BigEndian.Uint32(file[:4]))
		file = file[4:]
		if len(file) < n {
			t.Fatalf("blob header wants %d bytes and %d are left", n, len(file))
		}
		header, rest := file[:n], file[n:]

		kind, header := takeBytes(t, header, 1, "BlobHeader.type")
		size, header := takeVarint(t, header, 3, "BlobHeader.datasize")
		if len(header) != 0 {
			t.Fatalf("BlobHeader has %d bytes past datasize: % x", len(header), header)
		}
		if len(rest) < int(size) {
			t.Fatalf("blob declares %d bytes and %d are left", size, len(rest))
		}
		body, rest := rest[:size], rest[size:]

		raw, body := takeVarint(t, body, 2, "Blob.raw_size")
		compressed, body := takeBytes(t, body, 3, "Blob.zlib_data")
		if len(body) != 0 {
			t.Fatalf("Blob has %d bytes past zlib_data", len(body))
		}

		zr, err := zlib.NewReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatalf("Blob.zlib_data is not zlib: %v", err)
		}
		payload, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("inflating: %v", err)
		}
		// A blob that lies about how far it inflates is the malformed file
		// osmpbf's hardening refuses; a fixture builder that produced one
		// would fail tests for a reason that has nothing to do with them.
		if int(raw) != len(payload) {
			t.Errorf("%s blob declares raw_size %d and inflates to %d", kind, raw, len(payload))
		}
		out = append(out, blob{kind: string(kind), payload: payload})
		file = rest
	}
	return out
}

// takeVarint reads one varint field with the given number off the front.
func takeVarint(t *testing.T, b []byte, field int, what string) (uint64, []byte) {
	t.Helper()
	b = takeTag(t, b, field, 0, what)
	v, n := binary.Uvarint(b)
	if n <= 0 {
		t.Fatalf("%s: not a varint", what)
	}
	return v, b[n:]
}

// takeBytes reads one length-delimited field with the given number.
func takeBytes(t *testing.T, b []byte, field int, what string) ([]byte, []byte) {
	t.Helper()
	b = takeTag(t, b, field, 2, what)
	n, read := binary.Uvarint(b)
	if read <= 0 {
		t.Fatalf("%s: not a length", what)
	}
	b = b[read:]
	if uint64(len(b)) < n {
		t.Fatalf("%s: declares %d bytes and %d are left", what, n, len(b))
	}
	return b[:n], b[n:]
}

// takeTag checks the protobuf key, which is (field number << 3 | wire type).
func takeTag(t *testing.T, b []byte, field, wire int, what string) []byte {
	t.Helper()
	want := binary.AppendUvarint(nil, uint64(field)<<3|uint64(wire))
	if len(b) < len(want) || !bytes.Equal(b[:len(want)], want) {
		t.Fatalf("%s: expected the key for field %d wire %d (% x), got % x", what, field, wire, want, b)
	}
	return b[len(want):]
}

// TestExtract_BlocksAreOrderedAsARealExtractOrdersThem pins the file's shape:
// an OSMHeader, then one OSMData block of nodes, one of ways, one of
// relations, in that order.
//
// The three-pass pipeline does not depend on the order -- it reads the file
// three times precisely so it need not -- which is what makes the order
// deletable with every other test green, and why it is asserted here. A
// fixture whose relations came first would stop resembling the files this
// library exists to read, and the day something does depend on the order it
// would be a fixture nobody could reason about.
//
// The header payload is HeaderBlock field 4, required_features, a repeated
// string: key 4<<3|2 = 0x22, length 14, then "OsmSchema-V0.6".
//
// Each data block is a PrimitiveBlock whose field 2 is a PrimitiveGroup. The
// first key inside that group says which kind of element it holds, from
// osmformat.proto: dense = 2 (0x12), ways = 3 (0x1a), relations = 4 (0x22).
func TestExtract_BlocksAreOrderedAsARealExtractOrdersThem(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 0.0000003, 0.0000005).
		Way(7, []int64{1}).
		Relation(9, []osmbasetest.ExtractMember{{Type: "way", ID: 7, Role: "outer"}}, "name", "X")

	got := blobs(t, e.Bytes())
	if len(got) != 4 {
		t.Fatalf("the file holds %d blobs, want OSMHeader plus one OSMData each for nodes, ways and relations", len(got))
	}

	if got[0].kind != "OSMHeader" {
		t.Errorf("the first blob is %q, want OSMHeader", got[0].kind)
	}
	wantHeader := append([]byte{0x22, 0x0e}, "OsmSchema-V0.6"...)
	if !bytes.Equal(got[0].payload, wantHeader) {
		t.Errorf("header payload = % x, want % x (required_features, field 4)", got[0].payload, wantHeader)
	}

	wantGroupKey := []byte{0x12, 0x1a, 0x22} // dense nodes, ways, relations
	for i, want := range wantGroupKey {
		b := got[i+1]
		if b.kind != "OSMData" {
			t.Fatalf("blob %d is %q, want OSMData", i+1, b.kind)
		}
		_, rest := takeBytes(t, b.payload, 1, "PrimitiveBlock.stringtable")
		group, _ := takeBytes(t, rest, 2, "PrimitiveBlock.primitivegroup")
		if len(group) == 0 || group[0] != want {
			t.Errorf("data block %d holds a group beginning % x, want key %#x", i+1, group[:min(2, len(group))], want)
		}
	}
}

// An element kind the extract has none of gets no block at all, rather than
// an empty one: a producer writes no such block, and a PrimitiveBlock holding
// an empty group is a thing readers are not obliged to accept.
func TestExtract_AKindWithNoElementsGetsNoBlock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func() *osmbasetest.Extract
		want  []string
	}{
		{"nodes only", func() *osmbasetest.Extract {
			return osmbasetest.NewExtract().Node(1, 0, 0)
		}, []string{"OSMHeader", "OSMData"}},
		{"nothing at all", func() *osmbasetest.Extract {
			return osmbasetest.NewExtract()
		}, []string{"OSMHeader"}},
		{"nodes and relations but no ways", func() *osmbasetest.Extract {
			e := osmbasetest.NewExtract().Node(1, 0, 0)
			return e.Relation(9, []osmbasetest.ExtractMember{{Type: "node", ID: 1}}, "name", "X")
		}, []string{"OSMHeader", "OSMData", "OSMData"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var kinds []string
			for _, b := range blobs(t, tc.build().Bytes()) {
				kinds = append(kinds, b.kind)
			}
			if len(kinds) != len(tc.want) {
				t.Fatalf("blobs = %v, want %v", kinds, tc.want)
			}
		})
	}
}

// TestExtract_DenseNodeBlockByteLayout derives the whole node block by hand.
//
// Two nodes, ids 1 and 4, at 0.0000003,0.0000005 and 0.0000002,0.0000009.
// The block's granularity is 100 nanodegrees, so a coordinate in units is
// degrees times 1e7: the latitudes are 3 and 2 and the longitudes 5 and 9.
//
// DenseNodes holds three parallel runs, each delta-coded from zero and then
// zigzagged (n<<1 ^ n>>63):
//
//	id   1, 4  -> deltas 1, 3   -> zigzag 2, 6
//	lat  3, 2  -> deltas 3, -1  -> zigzag 6, 1      <- the negative delta
//	lon  5, 9  -> deltas 5, 4   -> zigzag 10, 8
//
// so, with keys (field << 3 | 2) of 0x0a for id, 0x42 for lat and 0x4a for
// lon, DenseNodes is twelve bytes:
//
//	0a 02 02 06   42 02 06 01   4a 02 0a 08
//
// It sits in PrimitiveGroup field 2 (key 0x12) and that in PrimitiveBlock
// field 2 (key 0x12 again). The string table is the reserved empty string
// alone -- one entry, field 1 of StringTable, so 0a 00 inside a field-1
// wrapper -- and granularity is field 17, a varint, whose key 17<<3|0 = 136
// is itself two bytes: 88 01, then 100 = 0x64.
//
// Nothing is tagged, so keys_vals (field 10) is absent entirely rather than
// present and empty. That is what a producer writes, and it is the case that
// leaves the reader's "no tags at all" path exercised.
func TestExtract_DenseNodeBlockByteLayout(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 0.0000003, 0.0000005).
		Node(4, 0.0000002, 0.0000009)

	got := blobs(t, e.Bytes())
	if len(got) != 2 {
		t.Fatalf("the file holds %d blobs, want a header and one data block", len(got))
	}
	want := []byte{
		0x0a, 0x02, 0x0a, 0x00, // stringtable: one entry, ""
		0x12, 0x0e, // primitivegroup, 14 bytes
		0x12, 0x0c, // dense, 12 bytes
		0x0a, 0x02, 0x02, 0x06, // ids
		0x42, 0x02, 0x06, 0x01, // lats
		0x4a, 0x02, 0x0a, 0x08, // lons
		0x88, 0x01, 0x64, // granularity 100
	}
	if !bytes.Equal(got[1].payload, want) {
		t.Errorf("node block = % x\nwant           % x", got[1].payload, want)
	}
}

// TestExtract_DenseTagsAreOneRunWithATerminatorPerNode pins the encoding the
// format is most easily got wrong in: keys_vals is ONE flat run for the whole
// block, each node's key/value indices followed by a zero, and a node with no
// tags contributing a bare zero. Drop the terminator for untagged nodes and
// every tag after the first untagged one lands on the wrong node -- a drift
// that produces a perfectly well-formed file in which a boundary carries the
// name of a bus stop.
//
// Two nodes, the first tagged place=town and the second untagged. The string
// table is "", "place", "town", so the run is 1, 2, 0 for the first node and
// 0 for the second: 01 02 00 00, under key 10<<3|2 = 0x52.
func TestExtract_DenseTagsAreOneRunWithATerminatorPerNode(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 0, 0, "place", "town").
		Node(2, 0, 0)

	got := blobs(t, e.Bytes())
	table, rest := takeBytes(t, got[1].payload, 1, "stringtable")
	wantTable := []byte{0x0a, 0x00, 0x0a, 0x05, 'p', 'l', 'a', 'c', 'e', 0x0a, 0x04, 't', 'o', 'w', 'n'}
	if !bytes.Equal(table, wantTable) {
		t.Errorf("string table = % x, want % x -- index 0 is the reserved empty string", table, wantTable)
	}

	group, _ := takeBytes(t, rest, 2, "primitivegroup")
	dense, _ := takeBytes(t, group, 2, "dense")
	want := []byte{
		0x0a, 0x02, 0x02, 0x02, // ids 1, 2
		0x42, 0x02, 0x00, 0x00, // lats 0, 0
		0x4a, 0x02, 0x00, 0x00, // lons 0, 0
		0x52, 0x04, 0x01, 0x02, 0x00, 0x00, // keys_vals: place=town, 0, then 0 for the untagged node
	}
	if !bytes.Equal(dense, want) {
		t.Errorf("dense nodes = % x\nwant        % x", dense, want)
	}
}

// TestExtract_WayByteLayout derives the way block by hand.
//
// A Way's id is field 1 and a PLAIN varint, not zigzag -- unlike everything
// else in this format that repeats -- and its refs are field 8, delta-coded
// and zigzagged. Tags are the two parallel runs of string indices, keys in
// field 2 and values in field 3, both plain int32.
//
// Way 7, refs 1 and 4, tagged boundary=administrative. The table is "",
// "boundary", "administrative", so keys is [1] and values is [2]; refs
// delta to 1, 3 and zigzag to 2, 6.
func TestExtract_WayByteLayout(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 0, 0).Node(4, 0, 0).
		Way(7, []int64{1, 4}, "boundary", "administrative")

	got := blobs(t, e.Bytes())
	if len(got) != 3 {
		t.Fatalf("the file holds %d blobs, want a header, the nodes and the ways", len(got))
	}
	_, rest := takeBytes(t, got[2].payload, 1, "stringtable")
	group, _ := takeBytes(t, rest, 2, "primitivegroup")
	way, after := takeBytes(t, group, 3, "ways")
	if len(after) != 0 {
		t.Errorf("the way group holds %d bytes past the one way", len(after))
	}
	want := []byte{
		0x08, 0x07, // id 7, a plain varint
		0x12, 0x01, 0x01, // keys: "boundary"
		0x1a, 0x01, 0x02, // vals: "administrative"
		0x42, 0x02, 0x02, 0x06, // refs 1, 4 as zigzagged deltas
	}
	if !bytes.Equal(way, want) {
		t.Errorf("way = % x\nwant  % x", way, want)
	}
}

// TestExtract_RelationMemberTypesAreNumberedAsTheSchemaNumbersThem is the one
// that nothing else in the module pins.
//
// osmpbf's own fixtures write member types as int32(MemberWay) and the like,
// so they agree with the constants whatever those constants are: renumber the
// enum and every test in osmpbf still passes. The numbering comes from
// osmformat.proto -- NODE = 0, WAY = 1, RELATION = 2 -- and this is where it
// is written as literal bytes.
//
// Relation 9, tagged name=X, with a node member 3 as admin_centre, a way
// member 7 as outer and a relation member 5 as subarea. Tags are encoded
// before the members, so the table fills up ""(0), "name"(1), "X"(2),
// "admin_centre"(3), "outer"(4), "subarea"(5).
//
//	roles_sid  field 8,  plain int32:   3, 4, 5
//	memids     field 9,  zigzag delta:  3, 7, 5 -> 3, 4, -2 -> 6, 8, 3
//	types      field 10, plain int32:   0, 1, 2
func TestExtract_RelationMemberTypesAreNumberedAsTheSchemaNumbersThem(t *testing.T) {
	e := osmbasetest.NewExtract()
	e.Relation(9, []osmbasetest.ExtractMember{
		{Type: "node", ID: 3, Role: "admin_centre"},
		{Type: "way", ID: 7, Role: "outer"},
		{Type: "relation", ID: 5, Role: "subarea"},
	}, "name", "X")

	got := blobs(t, e.Bytes())
	if len(got) != 2 {
		t.Fatalf("the file holds %d blobs, want a header and the relations", len(got))
	}
	table, rest := takeBytes(t, got[1].payload, 1, "stringtable")
	wantTable := bytes.Join([][]byte{
		{0x0a, 0x00},
		append([]byte{0x0a, 0x04}, "name"...),
		append([]byte{0x0a, 0x01}, "X"...),
		append([]byte{0x0a, 0x0c}, "admin_centre"...),
		append([]byte{0x0a, 0x05}, "outer"...),
		append([]byte{0x0a, 0x07}, "subarea"...),
	}, nil)
	if !bytes.Equal(table, wantTable) {
		t.Errorf("string table = % x\nwant         % x", table, wantTable)
	}

	group, _ := takeBytes(t, rest, 2, "primitivegroup")
	rel, after := takeBytes(t, group, 4, "relations")
	if len(after) != 0 {
		t.Errorf("the relation group holds %d bytes past the one relation", len(after))
	}
	want := []byte{
		0x08, 0x09, // id 9
		0x12, 0x01, 0x01, // keys: "name"
		0x1a, 0x01, 0x02, // vals: "X"
		0x42, 0x03, 0x03, 0x04, 0x05, // roles_sid
		0x4a, 0x03, 0x06, 0x08, 0x03, // memids, zigzagged deltas, the last negative
		0x52, 0x03, 0x00, 0x01, 0x02, // types: NODE, WAY, RELATION
	}
	if !bytes.Equal(rel, want) {
		t.Errorf("relation = % x\nwant       % x", rel, want)
	}
}

// Each block carries its own string table, as a real file's do: an index is
// only meaningful inside the block it was read from. A builder that shared
// one table across blocks would produce a file no reader could resolve, and
// no round trip through this module would notice, because the pipeline never
// compares an index from one block against another.
func TestExtract_EachBlockCarriesItsOwnStringTable(t *testing.T) {
	e := osmbasetest.NewExtract().
		Node(1, 0, 0, "place", "town").
		Way(7, []int64{1}, "boundary", "administrative")

	got := blobs(t, e.Bytes())
	nodeTable, _ := takeBytes(t, got[1].payload, 1, "stringtable")
	wayTable, _ := takeBytes(t, got[2].payload, 1, "stringtable")

	// Index 1 is the first string the block itself needed, and they differ.
	if !bytes.Contains(nodeTable, []byte("place")) || bytes.Contains(nodeTable, []byte("boundary")) {
		t.Errorf("the node block's table is % x; it should hold its own strings and no others", nodeTable)
	}
	if !bytes.Contains(wayTable, []byte("boundary")) || bytes.Contains(wayTable, []byte("place")) {
		t.Errorf("the way block's table is % x; it should hold its own strings and no others", wayTable)
	}
	if !bytes.HasPrefix(nodeTable, []byte{0x0a, 0x00}) || !bytes.HasPrefix(wayTable, []byte{0x0a, 0x00}) {
		t.Error("a block's string table does not begin with the empty string the format reserves at index 0")
	}
}

// TestExtract_CoordinateScalingRoundsRatherThanTruncates pins the choice the
// builder's own comment makes.
//
// 0.00000016 degrees is 1.6 units at a granularity of 100 nanodegrees.
// Rounded that is 2, truncated it is 1 -- and truncation loses up to a unit
// TOWARDS ZERO, so a fixture in the northern hemisphere drifts south and one
// in the southern drifts north, which is why the negative case is here too.
// The drift is a tenth of a metre, far too small to fail an assertion written
// with a tolerance, and exactly large enough to make an exact round-trip
// assertion impossible to write.
//
// One node, so the lat run is a single delta from zero: zigzag(2) = 4 and
// zigzag(-2) = 3, against the truncated zigzag(1) = 2 and zigzag(-1) = 1.
func TestExtract_CoordinateScalingRoundsRatherThanTruncates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lat      float64
		wantLat  byte
		truncate byte
	}{
		{"north of the equator", 0.00000016, 4, 2},
		{"south of it", -0.00000016, 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := osmbasetest.NewExtract().Node(1, tc.lat, 0)
			got := blobs(t, e.Bytes())
			_, rest := takeBytes(t, got[1].payload, 1, "stringtable")
			group, _ := takeBytes(t, rest, 2, "primitivegroup")
			dense, _ := takeBytes(t, group, 2, "dense")
			_, dense = takeBytes(t, dense, 1, "ids")
			lats, _ := takeBytes(t, dense, 8, "lats")

			if len(lats) != 1 {
				t.Fatalf("the lat run is %d bytes, want one node's delta", len(lats))
			}
			if lats[0] == tc.truncate {
				t.Fatalf("lat encoded as %#x, the truncation of %v; want %#x, its rounding",
					lats[0], tc.lat, tc.wantLat)
			}
			if lats[0] != tc.wantLat {
				t.Errorf("lat encoded as %#x, want %#x", lats[0], tc.wantLat)
			}
		})
	}
}

// The builder's doc comment promises byte-identical output for identical
// calls, and a test may rely on it. It builds its string table in a map, so
// the day something iterates that map instead of the ordered slice beside it
// the promise goes quietly.
func TestExtract_IsDeterministic(t *testing.T) {
	build := func() []byte {
		e := osmbasetest.NewExtract()
		for i := int64(1); i <= 20; i++ {
			e.Node(i, 55.7+float64(i)/1000, 9.5+float64(i)/1000, "place", "town", "name", "Somewhere")
		}
		e.Way(30, []int64{1, 2, 3}, "boundary", "administrative")
		e.Way(31, []int64{3, 4, 1}, "boundary", "administrative")
		e.Relation(40, []osmbasetest.ExtractMember{
			{Type: "way", ID: 30, Role: "outer"},
			{Type: "node", ID: 1, Role: "admin_centre"},
			{Type: "way", ID: 31, Role: "outer"},
		}, "boundary", "administrative", "admin_level", "8", "name", "Somewhere")
		return e.Bytes()
	}

	first, second := build(), build()
	if len(first) == 0 {
		t.Fatal("the builder produced nothing to compare")
	}
	if !bytes.Equal(first, second) {
		t.Errorf("two identical builds differ: %d bytes against %d", len(first), len(second))
	}
}
