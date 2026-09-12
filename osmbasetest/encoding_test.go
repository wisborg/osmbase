package osmbasetest_test

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// This package's whole claim is that it is an INDEPENDENT encoder: a fixture
// produced by running the decoder backwards proves only that the two agree,
// and an encoder and a decoder that misread the same line of the specification
// agree perfectly. The defence is byte-level assertions derived from the
// specification here in the test, and the parts of the encoder that lack one
// are the parts where that claim is unbacked.
//
// The three tests below close the parts that were reachable only through a
// round trip: the seven value encodings, the leaf directory section, and the
// command streams for lines and multipoints. Each number is worked out in the
// comment above it from the schema, not read back from either implementation.

// TestBuildValue_FieldNumbersAndWireTypesComeFromTheSchema pins all seven
// value encodings.
//
// Nothing pinned these before. Swap two of the seven field numbers in this
// encoder AND in the decoder -- which is exactly what happens when both are
// written from one misread of the schema -- and every test in this module
// still passes, while a tile from a real producer decodes every float as a
// double and every double as a float. Value.Kind would be wrong, Float64 would
// still hand back a plausible number, and a style keying on the kind would
// read zero out of the wrong field.
//
// A protobuf field key is (number << 3 | wire type). From the vector tile
// schema:
//
//	string_value = 1, length-delimited (2)  -> 1<<3|2 = 0x0a
//	float_value  = 2, fixed32 (5)           -> 2<<3|5 = 0x15
//	double_value = 3, fixed64 (1)           -> 3<<3|1 = 0x19
//	int_value    = 4, varint (0)            -> 4<<3|0 = 0x20
//	uint_value   = 5, varint (0)            -> 5<<3|0 = 0x28
//	sint_value   = 6, varint (0)            -> 6<<3|0 = 0x30
//	bool_value   = 7, varint (0)            -> 7<<3|0 = 0x38
func TestBuildValue_FieldNumbersAndWireTypesComeFromTheSchema(t *testing.T) {
	cases := []struct {
		name  string
		value mvt.Value
		want  []byte
	}{
		{
			// Two bytes of payload, so the length prefix is 0x02.
			name:  "a string",
			value: mvt.StringValue("ab"),
			want:  []byte{0x0a, 0x02, 'a', 'b'},
		},
		{
			// 2.5 as an IEEE-754 binary32 is sign 0, exponent 128 (0x80),
			// mantissa 0x200000: 0x40200000, little-endian on the wire.
			name:  "a float",
			value: mvt.FloatValue(2.5),
			want:  []byte{0x15, 0x00, 0x00, 0x20, 0x40},
		},
		{
			// 2.5 as a binary64 is 0x4004000000000000, little-endian.
			name:  "a double",
			value: mvt.DoubleValue(2.5),
			want:  []byte{0x19, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x40},
		},
		{
			// int_value is a plain varint of the two's-complement bits, which
			// is why a negative one costs ten bytes: -2 is 0xff..fe, and the
			// varint groups it into 0x7e then eight groups of 0x7f then the
			// last bit.
			name:  "a negative int, which protobuf spends ten bytes on",
			value: mvt.IntValue(-2),
			want:  []byte{0x20, 0xfe, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01},
		},
		{
			// 300 = 0b100101100 -> low seven bits 0101100 with the
			// continuation bit set (0xac), then 0b10 (0x02).
			name:  "a uint",
			value: mvt.UintValue(300),
			want:  []byte{0x28, 0xac, 0x02},
		},
		{
			// sint_value is zigzag: (n<<1)^(n>>63), so -2 becomes 3. This is
			// the encoding that makes a negative attribute one byte instead of
			// ten, and it is the one place in the value table where the same
			// number has two legal spellings.
			name:  "an sint, which is zigzagged",
			value: mvt.SintValue(-2),
			want:  []byte{0x30, 0x03},
		},
		{
			name:  "a true bool",
			value: mvt.BoolValue(true),
			want:  []byte{0x38, 0x01},
		},
		{
			// False is written, not omitted: a layer's value table is indexed
			// into, so an entry that encoded to nothing would shift every
			// index after it.
			name:  "a false bool",
			value: mvt.BoolValue(false),
			want:  []byte{0x38, 0x00},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := valueBytes(t, c.value)
			if !bytes.Equal(got, c.want) {
				t.Errorf("value %v encoded to % x\nwant                % x", c.value, got, c.want)
			}
		})
	}

	// A second, independent check on the two floating point cases: the bytes
	// above must be the ones the standard library produces for the same
	// number. This catches a transposed byte in the table itself.
	if got, want := valueBytes(t, mvt.FloatValue(2.5))[1:], binary.LittleEndian.AppendUint32(nil, math.Float32bits(2.5)); !bytes.Equal(got, want) {
		t.Errorf("float payload = % x, want % x", got, want)
	}
	if got, want := valueBytes(t, mvt.DoubleValue(2.5))[1:], binary.LittleEndian.AppendUint64(nil, math.Float64bits(2.5)); !bytes.Equal(got, want) {
		t.Errorf("double payload = % x, want % x", got, want)
	}
}

// valueBytes encodes one tag value and returns just the value message, by
// building a one-feature tile and cutting the layer's value table out of it.
//
// The layer is laid out name, features, keys, values, extent, version, and
// every one of those before the value table is a known length for this
// fixture, so the value message can be located without decoding anything.
func valueBytes(t *testing.T, v mvt.Value) []byte {
	t.Helper()
	data, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "l",
		Features: []osmbasetest.FeatureSpec{{
			Type:     mvt.GeomPoint,
			Geometry: mvt.Geometry{Points: []mvt.Point{pt(0, 0)}},
			Tags:     []osmbasetest.Tag{{Key: "k", Value: v}},
		}},
	}}})
	if err != nil {
		t.Fatalf("BuildTile: %v", err)
	}
	// Field 4 of the layer, length-delimited, is the value table entry. Find
	// the key byte followed by a length that reaches the extent field.
	const valueKey = byte(4<<3 | 2)
	for i := 0; i < len(data)-1; i++ {
		if data[i] != valueKey {
			continue
		}
		n := int(data[i+1])
		end := i + 2 + n
		// The extent field (0x28) follows the value table in this fixture.
		if end < len(data) && data[end] == 0x28 {
			return data[i+2 : end]
		}
	}
	t.Fatalf("no value message found in % x", data)
	return nil
}

// TestBuildArchive_ByteLayoutOfALeafDirectorySection pins the one part of the
// archive encoder that only a round trip through the reader was checking.
//
// Two things here are decisions a reader must agree with and which nothing
// stated in bytes: a leaf entry's offset is relative to the START OF THE LEAF
// SECTION rather than to the file, and each leaf directory's first tile ID
// delta is measured from ZERO rather than continuing from the previous leaf.
// Encode either of those the same wrong way at both ends and every round trip
// passes while a real archive reads a directory from the wrong place.
//
// Four tiles at LeafSize 2 gives two leaves of two entries and a root of two
// leaf entries. The tiles are "aaa", "bb", "c" and "dddd" at IDs 10, 20, 30
// and 40, so, clustered, the blobs sit at offsets 0, 3, 5 and 6.
//
//	leaf 0: 02          two entries
//	        0a 0a       tile IDs 10 and 20, as deltas from 0
//	        01 01       run lengths
//	        03 02       lengths 3 and 2
//	        01          offset 0, written as offset+1
//	        00          offset 3, which is where the entry before it ended
//
//	leaf 1: 02          two entries
//	        1e 0a       tile IDs 30 and 40; the first delta restarts from 0
//	        01 01       run lengths
//	        01 04       lengths 1 and 4
//	        06          offset 5, written as offset+1
//	        00          offset 6, contiguous
//
//	root:   02          two entries
//	        0a 14       tile IDs 10 and 30, the first ID of each leaf
//	        00 00       run length 0, which is what marks a leaf
//	        09 09       each leaf directory is nine bytes
//	        01          leaf 0 at leaf-section offset 0, as offset+1
//	        00          leaf 1 at offset 9, contiguous with leaf 0
func TestBuildArchive_ByteLayoutOfALeafDirectorySection(t *testing.T) {
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		LeafSize: 2,
		Tiles: []osmbasetest.ArchiveTile{
			{ID: 10, Data: []byte("aaa")},
			{ID: 20, Data: []byte("bb")},
			{ID: 30, Data: []byte("c")},
			{ID: 40, Data: []byte("dddd")},
		},
	})
	if err != nil {
		t.Fatalf("BuildArchive: %v", err)
	}
	if built.LeafLevels != 1 || built.RootEntries != 2 {
		t.Fatalf("the fixture is %d leaf levels with a root of %d entries, want 1 and 2", built.LeafLevels, built.RootEntries)
	}

	b := built.Bytes
	u64 := func(off int) uint64 { return binary.LittleEndian.Uint64(b[off : off+8]) }
	rootOffset, rootLength := u64(8), u64(16)
	leafOffset, leafLength := u64(40), u64(48)

	wantRoot := []byte{0x02, 0x0a, 0x14, 0x00, 0x00, 0x09, 0x09, 0x01, 0x00}
	wantLeaves := []byte{
		0x02, 0x0a, 0x0a, 0x01, 0x01, 0x03, 0x02, 0x01, 0x00,
		0x02, 0x1e, 0x0a, 0x01, 0x01, 0x01, 0x04, 0x06, 0x00,
	}
	if got := b[rootOffset : rootOffset+rootLength]; !bytes.Equal(got, wantRoot) {
		t.Errorf("root directory = % x\nwant            % x", got, wantRoot)
	}
	if got := b[leafOffset : leafOffset+leafLength]; !bytes.Equal(got, wantLeaves) {
		t.Errorf("leaf section = % x\nwant           % x", got, wantLeaves)
	}

	// The sections abut, which is what makes the offsets above mean what the
	// comment says they mean. Metadata defaults to the two bytes "{}".
	if rootOffset != pmtiles.HeaderSize || rootLength != 9 {
		t.Errorf("root is %d bytes at offset %d, want 9 at %d", rootLength, rootOffset, pmtiles.HeaderSize)
	}
	if want := rootOffset + rootLength + 2; leafOffset != want {
		t.Errorf("leaf section starts at %d, want %d: the header, the root and two bytes of metadata", leafOffset, want)
	}
	if got := u64(56); got != leafOffset+leafLength {
		t.Errorf("tile data starts at %d, want %d, immediately after the leaf section", got, leafOffset+leafLength)
	}
	if got := string(b[u64(56):]); got != "aaabbcdddd" {
		t.Errorf("tile data = %q, want %q", got, "aaabbcdddd")
	}
}

// TestEncodeGeometry_LineAndPointCommandStreams pins the two geometry shapes
// whose bytes only a round trip was checking. The polygon case already has
// one, and it is the polygon case that carries the cursor rule -- but a line
// and a multipoint each choose a command count, and getting that wrong
// produces a stream a lenient decoder still reads.
//
// A command integer is (id | count<<3): MoveTo once is 1|1<<3 = 9, LineTo once
// is 2|1<<3 = 10, and a MoveTo repeated twice is 1|2<<3 = 17. Parameters are
// zigzag deltas from a cursor that starts at (0,0) and persists across
// commands: v becomes (v<<1)^(v>>31), so 5 -> 10, 7 -> 14, -2 -> 3, -5 -> 9.
func TestEncodeGeometry_LineAndPointCommandStreams(t *testing.T) {
	cases := []struct {
		name string
		typ  mvt.GeomType
		in   mvt.Geometry
		want []uint32
	}{
		{
			// One MoveTo with a count of two, not two MoveTo commands. The
			// second point's parameters are a delta from the first: from (5,7)
			// to (3,2) is -2 and -5.
			name: "a multipoint is one MoveTo repeated",
			typ:  mvt.GeomPoint,
			in:   mvt.Geometry{Points: []mvt.Point{pt(5, 7), pt(3, 2)}},
			want: []uint32{17, 10, 14, 3, 9},
		},
		{
			// Two parts, so two MoveTo commands. The second part's MoveTo is a
			// delta from where the first part ended, at (2,10): +98 and +90,
			// which zigzag to 196 and 180. Then (100,100) to (90,80) is -10
			// and -20, which zigzag to 19 and 39.
			name: "a two-part linestring is a second MoveTo",
			typ:  mvt.GeomLineString,
			in: mvt.Geometry{Lines: [][]mvt.Point{
				{pt(2, 2), pt(2, 10)},
				{pt(100, 100), pt(90, 80)},
			}},
			want: []uint32{9, 4, 4, 10, 0, 16, 9, 196, 180, 10, 19, 39},
		},
		{
			// Three vertices after the opening MoveTo is one LineTo with a
			// count of three: 2|3<<3 = 26.
			name: "a line's vertices share one LineTo command",
			typ:  mvt.GeomLineString,
			in:   mvt.Geometry{Lines: [][]mvt.Point{{pt(0, 0), pt(1, 0), pt(1, 1), pt(0, 1)}}},
			want: []uint32{9, 0, 0, 26, 2, 0, 0, 2, 1, 0},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := osmbasetest.EncodeGeometry(c.typ, c.in)
			if err != nil {
				t.Fatalf("EncodeGeometry: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("geometry = %v\nwant       %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("geometry = %v\nwant       %v", got, c.want)
				}
			}
		})
	}
}
