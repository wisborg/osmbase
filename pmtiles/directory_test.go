package pmtiles_test

import (
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// TestDecodeDirectory_ContiguousOffsetsAreStoredAsZero covers the encoding
// that a clustered archive uses for almost every entry it has.
//
// An offset equal to the end of the previous entry is written as a single zero
// byte instead of as offset+1, so a directory whose tiles sit end to end is a
// fifth of the size. The expected offsets below are derived from the lengths,
// not from the encoder: entry 0 starts at 0, and each entry after it starts
// where the previous one ended.
func TestDecodeDirectory_ContiguousOffsetsAreStoredAsZero(t *testing.T) {
	in := []pmtiles.Entry{
		{TileID: 5, Offset: 0, Length: 10, RunLength: 1},
		{TileID: 6, Offset: 10, Length: 20, RunLength: 1},
		{TileID: 7, Offset: 30, Length: 5, RunLength: 1},
	}
	encoded := osmbasetest.EncodeDirectory(in)

	// One byte for the count, three tile ID deltas, three run lengths, three
	// lengths, then the offsets: 1 for entry 0 (offset+1) and one zero byte
	// each for the two that follow. Every one of those is a single-byte
	// varint, so the whole directory is 13 bytes. A directory that spelled
	// each offset out would be 15.
	if len(encoded) != 13 {
		t.Errorf("directory encoded to %d bytes, want 13; the contiguous offsets are not being written as zero", len(encoded))
	}

	got, err := pmtiles.DecodeDirectory(encoded)
	if err != nil {
		t.Fatalf("DecodeDirectory: %v", err)
	}
	assertEntries(t, got, in)
}

// TestDecodeDirectory_RunLengthsCoverSeveralTiles. One entry serving a run of
// identical tiles is how an archive stores an ocean: the offsets are shared,
// so the run length is the only thing saying which IDs the entry answers for.
func TestDecodeDirectory_RunLengthsCoverSeveralTiles(t *testing.T) {
	in := []pmtiles.Entry{
		{TileID: 100, Offset: 0, Length: 7, RunLength: 4},
		{TileID: 104, Offset: 7, Length: 9, RunLength: 1},
		{TileID: 200, Offset: 16, Length: 3, RunLength: 64},
	}
	got, err := pmtiles.DecodeDirectory(osmbasetest.EncodeDirectory(in))
	if err != nil {
		t.Fatalf("DecodeDirectory: %v", err)
	}
	assertEntries(t, got, in)
}

// TestDecodeDirectory_LeafEntriesAreRunLengthZero. There is no type byte in a
// directory entry: a run length of zero is the only thing that distinguishes a
// pointer to another directory from a pointer to a tile.
func TestDecodeDirectory_LeafEntriesAreRunLengthZero(t *testing.T) {
	in := []pmtiles.Entry{
		{TileID: 0, Offset: 0, Length: 40, RunLength: 0},
		{TileID: 500, Offset: 40, Length: 60, RunLength: 0},
	}
	got, err := pmtiles.DecodeDirectory(osmbasetest.EncodeDirectory(in))
	if err != nil {
		t.Fatalf("DecodeDirectory: %v", err)
	}
	assertEntries(t, got, in)
	for i, e := range got {
		if !e.IsLeaf() {
			t.Errorf("entry %d reports IsLeaf false with run length %d", i, e.RunLength)
		}
		if e.Covers(e.TileID) {
			t.Errorf("leaf entry %d claims to cover tile %d itself; a leaf serves no tile directly", i, e.TileID)
		}
	}
}

// TestDecodeDirectory_NonContiguousOffsets covers an unclustered archive,
// where a blob can sit anywhere and every offset is written out in full --
// including one that goes backwards, which deduplicating writers produce when
// two tile IDs share one blob.
func TestDecodeDirectory_NonContiguousOffsets(t *testing.T) {
	in := []pmtiles.Entry{
		{TileID: 1, Offset: 900, Length: 10, RunLength: 1},
		{TileID: 2, Offset: 100, Length: 10, RunLength: 1},
		{TileID: 3, Offset: 900, Length: 10, RunLength: 1}, // shares entry 1's blob
	}
	got, err := pmtiles.DecodeDirectory(osmbasetest.EncodeDirectory(in))
	if err != nil {
		t.Fatalf("DecodeDirectory: %v", err)
	}
	assertEntries(t, got, in)
}

// TestDecodeDirectory_RejectsCorruption checks that damaged bytes produce an
// error rather than a directory that decodes and points somewhere wrong.
//
// Every case here is one that would otherwise succeed: the spec's own decoding
// pseudocode computes the first entry's offset as value-1, which for a value
// of zero wraps to 2^64-1; a repeated tile ID leaves a binary search choosing
// arbitrarily between two entries; a length of zero reads no bytes and reports
// an absent tile as present and empty; and an entry count of 2^60 is an
// allocation of exabytes before a single entry is read.
func TestDecodeDirectory_RejectsCorruption(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"no bytes at all", nil},
		{"a count of zero entries", []byte{0x00}},
		{
			"a count that overflows a varint",
			[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		},
		{
			// One entry promised, nothing following it.
			"truncated after the count",
			[]byte{0x01},
		},
		{
			// count 1, tile ID 5, run length 1, length 10, offset varint 0.
			"a zero offset on the first entry",
			[]byte{0x01, 0x05, 0x01, 0x0a, 0x00},
		},
		{
			// count 2, IDs 5 and 5+0, run lengths 1 and 1, lengths 10 and 10,
			// offsets 1 and 0.
			"two entries at the same tile ID",
			[]byte{0x02, 0x05, 0x00, 0x01, 0x01, 0x0a, 0x0a, 0x01, 0x00},
		},
		{
			// count 1, tile ID 5, run length 1, length 0, offset 1.
			"an entry of zero length",
			[]byte{0x01, 0x05, 0x01, 0x00, 0x01},
		},
		{
			// A count of 2^60 followed by four bytes.
			"a count far larger than the bytes could hold",
			append([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x10}, 1, 1, 1, 1),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := pmtiles.DecodeDirectory(c.in)
			if err == nil {
				t.Fatalf("DecodeDirectory accepted %v and returned %+v", c.in, got)
			}
		})
	}
}

// TestDecodeDirectory_SurvivesEveryTruncation feeds every prefix of a valid
// directory back in. Each one must be an error and none may panic, which is
// the property that matters for a format read straight off a network range
// request: a short read is the normal failure, not an exotic one.
func TestDecodeDirectory_SurvivesEveryTruncation(t *testing.T) {
	full := osmbasetest.EncodeDirectory([]pmtiles.Entry{
		{TileID: 5, Offset: 0, Length: 10, RunLength: 1},
		{TileID: 6, Offset: 10, Length: 2000, RunLength: 3},
		{TileID: 40000, Offset: 2010, Length: 5, RunLength: 1},
	})
	if _, err := pmtiles.DecodeDirectory(full); err != nil {
		t.Fatalf("the undamaged directory did not decode: %v", err)
	}
	for n := 0; n < len(full); n++ {
		if _, err := pmtiles.DecodeDirectory(full[:n]); err == nil {
			t.Errorf("a directory truncated to %d of %d bytes decoded without error", n, len(full))
		}
	}
}

func assertEntries(t *testing.T, got, want []pmtiles.Entry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("decoded %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
