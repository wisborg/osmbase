package osmpbf

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func TestDecodePrimitiveBlockReadsTheStringTableAndGroups(t *testing.T) {
	data := primitiveBlock(
		stringTable("", "boundary", "administrative", "name", "Horsens Kommune"),
		nil,
		[]byte("first group"), []byte("second group"),
	)

	b, err := DecodePrimitiveBlock(data)
	if err != nil {
		t.Fatalf("DecodePrimitiveBlock: %v", err)
	}

	want := []string{"", "boundary", "administrative", "name", "Horsens Kommune"}
	if len(b.Strings) != len(want) {
		t.Fatalf("the string table has %d entries, want %d", len(b.Strings), len(want))
	}
	for i, w := range want {
		if got := b.StringAt(i); got != w {
			t.Errorf("StringAt(%d) = %q, want %q", i, got, w)
		}
	}

	if len(b.Groups) != 2 {
		t.Fatalf("decoded %d groups, want 2", len(b.Groups))
	}
	if !bytes.Equal(b.Groups[0], []byte("first group")) || !bytes.Equal(b.Groups[1], []byte("second group")) {
		t.Errorf("groups decoded out of order or wrong: %q", b.Groups)
	}
}

// An index the table does not have resolves to "" rather than panicking or
// returning a neighbour. A pass over an extract resolves millions of tag
// indices, and one bad index in a downloaded file should mean "this tag does
// not match", not the end of the pass.
func TestStringAtOutOfRangeIsEmpty(t *testing.T) {
	b, err := DecodePrimitiveBlock(primitiveBlock(stringTable("", "name"), nil))
	if err != nil {
		t.Fatalf("DecodePrimitiveBlock: %v", err)
	}
	for _, i := range []int{-1, 2, 1 << 20} {
		if got := b.StringAt(i); got != "" {
			t.Errorf("StringAt(%d) = %q, want the empty string", i, got)
		}
	}
	if got := b.StringAt(1); got != "name" {
		t.Errorf("String(1) = %q, want %q; the guard must not swallow real entries", got, "name")
	}
}

// String returns a copy. The entries point into the reader's reused buffer,
// so a name kept across a call to Next would otherwise become whatever the
// next block put there.
func TestStringAtCopiesOutOfTheBuffer(t *testing.T) {
	data := primitiveBlock(stringTable("", "Horsens"), nil)
	b, err := DecodePrimitiveBlock(data)
	if err != nil {
		t.Fatalf("DecodePrimitiveBlock: %v", err)
	}
	kept := b.StringAt(1)
	for i := range data {
		data[i] = 'z'
	}
	if kept != "Horsens" {
		t.Errorf("the kept string became %q after its buffer was overwritten", kept)
	}
}

// Absent is the format's default, not zero. A block that omits granularity is
// using 100; reading it as 0 would multiply every coordinate in the block to
// nothing and stack its nodes on the origin.
func TestAbsentScalingFieldsTakeTheFormatDefaults(t *testing.T) {
	b, err := DecodePrimitiveBlock(primitiveBlock(stringTable(""), nil))
	if err != nil {
		t.Fatalf("DecodePrimitiveBlock: %v", err)
	}
	if b.Granularity != DefaultGranularity {
		t.Errorf("granularity = %d, want the default %d", b.Granularity, DefaultGranularity)
	}
	if b.DateGranularity != DefaultDateGranularity {
		t.Errorf("date granularity = %d, want the default %d", b.DateGranularity, DefaultDateGranularity)
	}
	if b.LatOffset != 0 || b.LonOffset != 0 {
		t.Errorf("offsets = %d,%d, want 0,0", b.LatOffset, b.LonOffset)
	}
}

func TestDecodePrimitiveBlockReadsScaling(t *testing.T) {
	// Written through an int64 variable because a negative constant cannot be
	// converted to uint64 at compile time, and these offsets are signed values
	// carried in an unsigned varint.
	var negativeOffset int64 = -1_000_000_000

	extra := pbVarint(17, 1000)                                    // granularity
	extra = append(extra, pbVarint(18, 60000)...)                  // date_granularity
	extra = append(extra, pbVarint(19, uint64(negativeOffset))...) // lat_offset, negative
	extra = append(extra, pbVarint(20, 2e9)...)                    // lon_offset

	b, err := DecodePrimitiveBlock(primitiveBlock(stringTable(""), extra))
	if err != nil {
		t.Fatalf("DecodePrimitiveBlock: %v", err)
	}
	if b.Granularity != 1000 || b.DateGranularity != 60000 {
		t.Errorf("granularity %d, date granularity %d, want 1000 and 60000", b.Granularity, b.DateGranularity)
	}
	// A negative offset is a plain varint in this format, not a zigzag one.
	// Read as zigzag it would come back as a large positive number and put
	// the block's coordinates in the wrong hemisphere.
	if b.LatOffset != -1e9 {
		t.Errorf("lat offset = %d, want -1000000000", b.LatOffset)
	}
	if b.LonOffset != 2e9 {
		t.Errorf("lon offset = %d, want 2000000000", b.LonOffset)
	}
}

func TestDegrees(t *testing.T) {
	for _, tc := range []struct {
		name             string
		granularity      int32
		latOff, lonOff   int64
		lat, lon         int64
		wantLat, wantLon float64
	}{
		{
			name:        "the usual block, granularity 100 and no offset",
			granularity: DefaultGranularity, lat: 557000000, lon: 98500000,
			wantLat: 55.7, wantLon: 9.85,
		},
		{
			name: "an offset block", granularity: DefaultGranularity,
			latOff: -1e9, lonOff: 5e8, lat: 567000000, lon: 93500000,
			wantLat: 55.7, wantLon: 9.85,
		},
		{
			name: "a coarser granularity", granularity: 1000,
			lat: 55700000, lon: 9850000, wantLat: 55.7, wantLon: 9.85,
		},
		{
			name: "the southern and western hemispheres", granularity: DefaultGranularity,
			lat: -338680000, lon: 1512090000, wantLat: -33.868, wantLon: 151.209,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := PrimitiveBlock{Granularity: tc.granularity, LatOffset: tc.latOff, LonOffset: tc.lonOff}
			gotLat, gotLon := b.Degrees(tc.lat, tc.lon)
			// A nanodegree is about 0.1 mm, so anything looser than this
			// would accept a scaling error the size of a street.
			if math.Abs(gotLat-tc.wantLat) > 1e-9 || math.Abs(gotLon-tc.wantLon) > 1e-9 {
				t.Errorf("Degrees = %.9f,%.9f, want %.9f,%.9f", gotLat, gotLon, tc.wantLat, tc.wantLon)
			}
		})
	}
}

// A granularity of zero does not fail on its own -- it quietly multiplies
// every coordinate in the block to the block's origin, and the pipeline
// downstream would draw a whole municipality as a single point.
func TestAGranularityOfZeroIsRefused(t *testing.T) {
	var negative int64 = -100
	for _, g := range []uint64{0, uint64(negative)} {
		data := primitiveBlock(stringTable(""), pbVarint(17, g))
		_, err := DecodePrimitiveBlock(data)
		if err == nil {
			t.Errorf("a granularity encoded as %d was accepted; coordinates are scaled by it", g)
			continue
		}
		if !strings.Contains(err.Error(), "granularity") {
			t.Errorf("DecodePrimitiveBlock: %v, want an error naming the granularity", err)
		}
	}
}

// A field this does not read must be skipped, not treated as the end of the
// message. Real blocks carry several -- the optional metadata this pipeline
// has no use for -- and a decoder that stopped at the first would find no
// groups in any real file.
func TestUnknownPrimitiveBlockFieldsAreSkipped(t *testing.T) {
	extra := pbBytes(30, []byte("a field from a later schema"))
	extra = append(extra, pbVarint(31, 7)...)
	extra = append(extra, pbVarint(17, 100)...)

	b, err := DecodePrimitiveBlock(primitiveBlock(stringTable("", "name"), extra, []byte("group")))
	if err != nil {
		t.Fatalf("DecodePrimitiveBlock: %v", err)
	}
	if len(b.Groups) != 1 || b.StringAt(1) != "name" {
		t.Errorf("unknown fields cost the block its contents: %d groups, StringAt(1)=%q", len(b.Groups), b.StringAt(1))
	}
}

func TestMalformedPrimitiveBlocksAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"a length that overruns the message", []byte{0x0a, 0x7f}},
		{"a field number of zero", []byte{0x00, 0x01}},
		{"a string table whose entry overruns it", pbBytes(1, []byte{0x0a, 0x7f})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodePrimitiveBlock(tc.data); err == nil {
				t.Error("malformed bytes decoded as a valid block")
			}
		})
	}
}

// A block that sets one scaling field leaves the others at the format's
// defaults. Neither of the tests either side of this one sees that: one sets
// every field and the other sets none, so a decoder that applied the defaults
// only to a block with no scaling fields at all -- or dropped them as soon as
// it saw one -- passes both. Real blocks are the partial case: producers write
// granularity and the offsets and leave date_granularity out.
func TestSomeScalingFieldsPresentLeavesTheRestAtTheirDefaults(t *testing.T) {
	var latOffset int64 = -500_000_000
	// Granularity and one offset written, date_granularity and the other
	// offset left out -- the shape a real producer writes. 500 rather than
	// 100 so the assertion on it cannot be satisfied by the default.
	extra := append(pbVarint(17, 500), pbVarint(19, uint64(latOffset))...)

	b, err := DecodePrimitiveBlock(primitiveBlock(stringTable(""), extra))
	if err != nil {
		t.Fatalf("DecodePrimitiveBlock: %v", err)
	}
	if b.LatOffset != latOffset {
		t.Errorf("lat offset = %d, want %d", b.LatOffset, latOffset)
	}
	if b.Granularity != 500 {
		t.Errorf("granularity = %d, want the 500 the block declared", b.Granularity)
	}
	if b.DateGranularity != DefaultDateGranularity {
		t.Errorf("date granularity = %d, want the default %d; a field present next to it "+
			"must not cost it its default", b.DateGranularity, DefaultDateGranularity)
	}
	if b.LonOffset != 0 {
		t.Errorf("lon offset = %d, want 0; only the latitude offset was written", b.LonOffset)
	}
}

// Every field is cut short in turn, and none of them decodes as a block.
//
// The branches are copy-pasted, one per field number, and the copy where the
// error is dropped is not loud: for the two length-delimited fields the
// reader has already stepped over the length it could not honour, so a
// dropped error there leaves a block with no string table and no groups and
// nothing reporting a problem -- a file that decodes as empty rather than as
// broken. The scalar rows are here as the boundary of the same table; they
// are weaker, because the shared reader does not advance past a varint it
// could not read and the next tag read fails whatever this branch did.
func TestAFieldCutShortIsRefusedWhicheverFieldItIs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field int
		wire  int
	}{
		{"a string table", 1, protobufWireBytes},
		{"a primitive group", 2, protobufWireBytes},
		{"a granularity", 17, protobufWireVarint},
		{"a date granularity", 18, protobufWireVarint},
		{"a latitude offset", 19, protobufWireVarint},
		{"a longitude offset", 20, protobufWireVarint},
		{"a field this decoder does not read", 30, protobufWireVarint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var data []byte
			if tc.wire == protobufWireBytes {
				// A length longer than the bytes that follow it.
				data = append(pbTag(tc.field, tc.wire), 0x7f)
			} else {
				// A varint with its continuation bit set and nothing after.
				data = append(pbTag(tc.field, tc.wire), 0xff)
			}
			b, err := DecodePrimitiveBlock(data)
			if err == nil {
				t.Fatalf("a truncated %s decoded as a valid block: %d strings, %d groups",
					tc.name, len(b.Strings), len(b.Groups))
			}
		})
	}
}
