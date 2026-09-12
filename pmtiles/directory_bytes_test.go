package pmtiles_test

import (
	"bytes"
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// Every directory this package decodes in a test that expects it to SUCCEED
// arrives from osmbasetest.EncodeDirectory. That encoder is pinned to the
// specification byte for byte in its own package, so the arrangement is sound
// -- but it is sound in two steps, and the second step is a round trip. The
// only hand-written directory bytes in the pmtiles package are the corrupt
// ones, where the assertion is merely "this fails".
//
// So a decoder that read the five runs in a different order, or computed the
// contiguous offset differently, is caught today by the encoder's byte tests
// failing rather than by anything here, and the day someone rewrites those
// fixtures the decoder has nothing of its own left. This test gives it one
// directory whose bytes and whose meaning are both written out by hand from
// section 4.2 of the specification.

// TestDecodeDirectory_ADirectoryWrittenOutByHand decodes a directory whose
// every byte is derived in the comment below, and checks the entries against
// what those bytes mean rather than against what an encoder produced.
//
// The directory carries all four things a real one does: a run of several
// tiles, a leaf entry, an offset written as the one-byte "directly after the
// previous entry", and an offset far enough away to need two varint bytes --
// which is also a tile ID delta large enough to need two.
//
//	04           four entries
//
//	tile ID deltas, each from the previous ID:
//	05           5, so entry 0 is tile 5
//	03           +3, entry 1 is tile 8
//	a4 02        +292: 292 is 0b1_0010_0100, so the low seven bits 0100100
//	             with the continuation bit is 0xa4 and the rest is 0x02.
//	             Entry 2 is tile 300.
//	01           +1, entry 3 is tile 301
//
//	run lengths, written as they are:
//	03           entry 0 serves tiles 5, 6 and 7
//	00           entry 1 is a LEAF; zero is the only marker for that
//	01           entry 2 serves one tile
//	01           entry 3 serves one tile
//
//	lengths, written as they are, every one greater than zero:
//	0a 14 05 07  10, 20, 5 and 7 bytes
//
//	offsets, as offset+1 or as 0 when the entry sits at the previous one's end:
//	01           entry 0 at offset 0, written as 0+1
//	00           entry 1 at offset 10, which is 0+10, the end of entry 0
//	85 07        entry 2 at offset 900: 901 is 0b11_1000_0101, so 0x85 then
//	             0x07. Entry 1 ended at 30, so this cannot be the zero form.
//	00           entry 3 at offset 905, which is 900+5, the end of entry 2
//
// Nineteen bytes in all.
func TestDecodeDirectory_ADirectoryWrittenOutByHand(t *testing.T) {
	raw := []byte{
		0x04,
		0x05, 0x03, 0xa4, 0x02, 0x01,
		0x03, 0x00, 0x01, 0x01,
		0x0a, 0x14, 0x05, 0x07,
		0x01, 0x00, 0x85, 0x07, 0x00,
	}
	if len(raw) != 19 {
		t.Fatalf("the fixture is %d bytes and the derivation above says 19", len(raw))
	}

	want := []pmtiles.Entry{
		{TileID: 5, Offset: 0, Length: 10, RunLength: 3},
		{TileID: 8, Offset: 10, Length: 20, RunLength: 0},
		{TileID: 300, Offset: 900, Length: 5, RunLength: 1},
		{TileID: 301, Offset: 905, Length: 7, RunLength: 1},
	}

	got, err := pmtiles.DecodeDirectory(raw)
	if err != nil {
		t.Fatalf("DecodeDirectory: %v", err)
	}
	assertEntries(t, got, want)

	// The meanings those entries carry, said as behaviour rather than as
	// struct fields: the run answers for three tile IDs and not a fourth, and
	// the leaf answers for none of its own.
	for _, id := range []uint64{5, 6, 7} {
		if !got[0].Covers(id) {
			t.Errorf("entry 0 does not cover tile %d, and its run of 3 starts at 5", id)
		}
	}
	if got[0].Covers(8) {
		t.Error("entry 0 covers tile 8, which is one past the end of its run of 3")
	}
	if !got[1].IsLeaf() {
		t.Error("entry 1 has run length 0 and is not reported as a leaf")
	}
	if got[1].Covers(8) {
		t.Error("the leaf entry claims to serve tile 8 itself; a leaf serves no tile directly")
	}

	// A cross-check in the other direction: the module's own encoder, written
	// independently from the same section of the specification, must produce
	// exactly these bytes from exactly these entries. The assertion above does
	// not depend on this one -- it is here so that a change to either spelling
	// has to be a deliberate change to both.
	if enc := osmbasetest.EncodeDirectory(want); !bytes.Equal(enc, raw) {
		t.Errorf("EncodeDirectory produced % x\nthe hand-derived bytes are % x", enc, raw)
	}
}

// TestDecodeDirectory_TheZeroOffsetMeansTheEndOfThePreviousEntryNotItsStart
// isolates the one arithmetic decision in the whole encoding.
//
// A zero offset means "the previous entry's offset PLUS its length". Reading
// it as the previous entry's offset alone is a decoder that works perfectly
// on every entry of length zero -- of which the format has none -- and serves
// the previous tile's bytes for every entry after the first, which is a real
// tile and decodes cleanly.
//
// The lengths here are all different and none of them divides the others, so
// the expected offsets below can only come out right one way. They are
// accumulated in the test from the lengths rather than copied from anywhere.
func TestDecodeDirectory_TheZeroOffsetMeansTheEndOfThePreviousEntryNotItsStart(t *testing.T) {
	lengths := []uint32{3, 11, 7, 29, 5}
	raw := []byte{byte(len(lengths))}
	for range lengths {
		raw = append(raw, 0x01) // tile ID deltas: 1, 2, 3, 4, 5
	}
	for range lengths {
		raw = append(raw, 0x01) // run lengths
	}
	for _, l := range lengths {
		raw = append(raw, byte(l))
	}
	raw = append(raw, 0x01) // the first entry's offset 0, as offset+1
	for range lengths[1:] {
		raw = append(raw, 0x00) // and the rest contiguous
	}

	got, err := pmtiles.DecodeDirectory(raw)
	if err != nil {
		t.Fatalf("DecodeDirectory: %v", err)
	}
	if len(got) != len(lengths) {
		t.Fatalf("decoded %d entries, want %d", len(got), len(lengths))
	}
	var want uint64
	for i, l := range lengths {
		if got[i].Offset != want {
			t.Errorf("entry %d is at offset %d, want %d: the sum of the %d lengths before it", i, got[i].Offset, want, i)
		}
		if got[i].Length != l {
			t.Errorf("entry %d is %d bytes, want %d", i, got[i].Length, l)
		}
		want += uint64(l)
	}
	// The last entry's end is the total, which is the sanity check that the
	// blobs really do abut with no gap and no overlap.
	if last := got[len(got)-1]; last.Offset+uint64(last.Length) != want {
		t.Errorf("the entries end at %d and their lengths sum to %d", last.Offset+uint64(last.Length), want)
	}
}
