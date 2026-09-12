package pmtiles_test

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// TestDecodeDirectory_RejectsFieldValuesTooLargeForTheirField covers the three
// range checks a corrupt varint reaches, none of which a truncation or a
// single flipped byte lands on.
//
// Each of these decodes cleanly as a varint. What makes them corruption is the
// value, and every one of them turns into something the rest of the reader
// acts on: a length above 2^32 is narrowed by the uint32 conversion into a
// small read at a legitimate offset, so the archive hands back a fragment of
// itself as a tile; a run length above 2^32 is narrowed the same way and makes
// one entry answer for a different set of tile IDs than the writer intended;
// and a tile ID delta that overflows wraps the running ID back to a low
// number, which puts the entries out of order and makes the binary search
// return the wrong one.
//
// The byte strings are assembled with encoding/binary rather than written out
// in hex, so the fixture is the value the case is named for.
func TestDecodeDirectory_RejectsFieldValuesTooLargeForTheirField(t *testing.T) {
	const (
		pastUint32 = uint64(1) << 32 // one past the largest uint32
		maxUint64  = ^uint64(0)      // a tile ID nothing can be added to
	)

	// One entry, laid out as the format's five runs: count, tile ID deltas,
	// run lengths, lengths, offsets.
	oneEntry := func(idDelta, runLength, length, offset uint64) []byte {
		b := binary.AppendUvarint(nil, 1)
		b = binary.AppendUvarint(b, idDelta)
		b = binary.AppendUvarint(b, runLength)
		b = binary.AppendUvarint(b, length)
		return binary.AppendUvarint(b, offset)
	}

	cases := []struct {
		name    string
		in      []byte
		wantMsg string
	}{
		{
			name:    "a run length one past what a uint32 holds",
			in:      oneEntry(5, pastUint32, 10, 1),
			wantMsg: "run length",
		},
		{
			name:    "a length one past what a uint32 holds",
			in:      oneEntry(5, 1, pastUint32, 1),
			wantMsg: "larger than any tile or directory",
		},
		{
			name: "a tile ID delta that runs past the end of uint64",
			in: func() []byte {
				// Two entries: the first lands on the largest possible tile
				// ID, and the second asks for one more.
				b := binary.AppendUvarint(nil, 2)
				b = binary.AppendUvarint(b, maxUint64)
				b = binary.AppendUvarint(b, 1)
				b = binary.AppendUvarint(b, 1) // run lengths
				b = binary.AppendUvarint(b, 1)
				b = binary.AppendUvarint(b, 10) // lengths
				b = binary.AppendUvarint(b, 10)
				b = binary.AppendUvarint(b, 1) // offsets
				return binary.AppendUvarint(b, 0)
			}(),
			wantMsg: "overflows past tile ID",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := pmtiles.DecodeDirectory(c.in)
			if err == nil {
				t.Fatalf("DecodeDirectory accepted it and returned %+v", got)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("error was %q, and it should say %q", err, c.wantMsg)
			}
		})
	}

	// The paired case: the same directory with values that fit decodes, so the
	// three rejections above are about the magnitude and not about the shape
	// of the bytes. The largest legal run length and length are 2^32-1.
	ok, err := pmtiles.DecodeDirectory(oneEntry(5, 0xffffffff, 0xffffffff, 1))
	if err != nil {
		t.Fatalf("a directory at the top of each field's range did not decode: %v", err)
	}
	want := pmtiles.Entry{TileID: 5, Offset: 0, Length: 0xffffffff, RunLength: 0xffffffff}
	if len(ok) != 1 || ok[0] != want {
		t.Errorf("decoded %+v, want one entry %+v", ok, want)
	}
}

// TestReader_RefusesDirectoriesThatPointAtEachOther is the termination
// guarantee maxLeafDepth exists for.
//
// A leaf entry whose offset addresses its own directory is a legal-looking way
// to build an archive that never resolves: every lookup finds a leaf, follows
// it, and finds the same leaf again. Nothing in the format forbids it and
// there is no checksum to notice, so the only defence is the depth cap -- and
// without a test the cap is a constant that can be raised, removed or
// refactored into an unbounded loop with no failure to show for it. A render
// that hangs is worse than one that reports a broken archive, because it looks
// like slow work rather than like a fault.
//
// The archive is built by hand because no correct writer produces one. It is
// arranged so the root directory and the leaf directory are the SAME five
// bytes at two places in the file: one entry, tile ID 0, run length 0 (which
// is what marks a leaf), length 5, offset 0 -- an offset relative to the start
// of the leaf section, which is where those same bytes sit again.
func TestReader_RefusesDirectoriesThatPointAtEachOther(t *testing.T) {
	selfLeaf := osmbasetest.EncodeDirectory([]pmtiles.Entry{
		{TileID: 0, Offset: 0, Length: 5, RunLength: 0},
	})
	// The entry's Length must be the directory's own length for the bytes it
	// addresses to be that same directory. Five single-byte varints.
	if len(selfLeaf) != 5 {
		t.Fatalf("the self-referential directory is %d bytes and its entry claims 5; the fixture is not self-referential", len(selfLeaf))
	}

	h := make([]byte, pmtiles.HeaderSize)
	copy(h, "PMTiles")
	h[7] = 3
	put := func(off int, v uint64) { binary.LittleEndian.PutUint64(h[off:], v) }
	rootOffset := uint64(pmtiles.HeaderSize)
	leafOffset := rootOffset + uint64(len(selfLeaf))
	put(8, rootOffset)
	put(16, uint64(len(selfLeaf))) // root length
	put(24, leafOffset)            // metadata offset, an empty section
	put(32, 0)                     // metadata length
	put(40, leafOffset)            // leaf section offset
	put(48, uint64(len(selfLeaf))) // leaf section length
	put(56, leafOffset+uint64(len(selfLeaf)))
	put(64, 0) // no tile data at all
	h[97] = byte(pmtiles.CompressionNone)
	h[98] = byte(pmtiles.CompressionNone)
	h[101] = 15 // max zoom

	archive := append(append(append([]byte(nil), h...), selfLeaf...), selfLeaf...)

	r, err := pmtiles.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("NewReader on the hand-built archive: %v", err)
	}

	// Tile ID 0 is the one the cycle addresses. The lookup runs on its own
	// goroutine so that a reader which loops forever reports as a failure here
	// rather than as the whole package timing out several minutes later. The
	// distinction matters: "this test did not terminate" names the defect, and
	// a package-wide timeout names nothing.
	type result struct {
		data []byte
		ok   bool
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, ok, err := r.TileByID(0)
		done <- result{data, ok, err}
	}()
	var got result
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("TileByID did not return on an archive whose leaf directory points at itself; the lookup has no depth cap and a render would hang rather than report a broken archive")
	}
	data, ok, err := got.data, got.ok, got.err

	if err == nil {
		t.Fatalf("TileByID(0) returned ok=%v data=%q through a directory that points at itself", ok, data)
	}
	if ok || data != nil {
		t.Errorf("TileByID(0) reported ok=%v with %d bytes alongside its error", ok, len(data))
	}
	if !strings.Contains(err.Error(), "point at each other") {
		t.Errorf("error was %q, and it should say the directories point at each other", err)
	}
}

// TestParseHeader_KeepsTheThreeTileCountsApart. The header carries three
// different counts in three consecutive 8-byte slots -- addressed tiles, tile
// entries and tile contents -- and they are equal in almost every archive a
// test would build, so a header parsed with any two of them transposed reads
// back exactly right.
//
// They are not interchangeable. Addressed tiles is the count BEFORE run-length
// encoding and is what a "this archive holds n tiles" summary means; tile
// entries is how many directory entries describe tiles and is what a byte-range
// plan is sized against; tile contents is the number of distinct blobs and is
// what says whether the writer deduplicated. Reading them in the wrong order
// reports a plausible number for every one of them.
//
// The header is built here rather than by the fixture builder precisely so the
// three values differ: a builder writes what a real archive would, and a real
// archive of this shape has them equal.
func TestParseHeader_KeepsTheThreeTileCountsApart(t *testing.T) {
	const (
		addressed = 900
		entries   = 90
		contents  = 9
	)
	h := make([]byte, pmtiles.HeaderSize)
	copy(h, "PMTiles")
	h[7] = 3
	put := func(off int, v uint64) { binary.LittleEndian.PutUint64(h[off:], v) }
	put(8, pmtiles.HeaderSize) // root offset
	put(16, 5)                 // root length, any non-zero value under the cap
	put(72, addressed)
	put(80, entries)
	put(88, contents)
	h[97] = byte(pmtiles.CompressionNone)
	h[98] = byte(pmtiles.CompressionNone)
	h[101] = 15

	got, err := pmtiles.ParseHeader(h)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	for _, c := range []struct {
		name      string
		got, want uint64
	}{
		{"addressed tiles, at byte 72", got.AddressedTiles, addressed},
		{"tile entries, at byte 80", got.TileEntries, entries},
		{"tile contents, at byte 88", got.TileContents, contents},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestReader_AddressedTilesCountsTheRunNotTheEntry is the same distinction
// seen through a real archive: the counts come apart exactly when run-length
// encoding is in use, which is the case they exist to describe.
//
// Three entries covering 4, 1 and 64 tile IDs is 69 addressed tiles in 3
// entries, both derived from the fixture's own run lengths.
func TestReader_AddressedTilesCountsTheRunNotTheEntry(t *testing.T) {
	built := build(t, osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{
			{ID: 10, Run: 4, Data: []byte("ocean")},
			{ID: 20, Data: []byte("coast")},
			{ID: 100, Run: 64, Data: []byte("more ocean")},
		},
	})
	h := reader(t, built).Header()
	if h.AddressedTiles != 4+1+64 {
		t.Errorf("addressed tiles = %d, want %d: the runs cover 4, 1 and 64 tile IDs", h.AddressedTiles, 4+1+64)
	}
	if h.TileEntries != 3 {
		t.Errorf("tile entries = %d, want 3: there are three directory entries", h.TileEntries)
	}
}

// TestReader_RefusesCoordinatesAndIDsItCannotAddress. Every public entry point
// takes a coordinate or an ID from a caller, and an out-of-range one has a
// valid-looking answer available: wrapping x past the edge of its zoom names a
// real tile at the other side of the world, which the archive would then serve
// without complaint.
//
// Tile, RawTile and TileByID are listed separately because each does its own
// conversion, and a guard added to one of them is not a guard on the others.
func TestReader_RefusesCoordinatesAndIDsItCannotAddress(t *testing.T) {
	built := build(t, osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{{ID: 5, Data: []byte("tile")}},
	})
	r := reader(t, built)

	// Zoom 2 is four tiles square, so x or y of 4 is off the edge.
	if _, ok, err := r.Tile(2, 4, 0); err == nil {
		t.Errorf("Tile(2, 4, 0) returned ok=%v and no error, and x=4 is outside zoom 2", ok)
	}
	if _, ok, err := r.RawTile(2, 0, 4); err == nil {
		t.Errorf("RawTile(2, 0, 4) returned ok=%v and no error, and y=4 is outside zoom 2", ok)
	}
	// The first ID past the last tile of the deepest addressable zoom.
	var pastTheEnd uint64
	for i := uint8(0); i <= pmtiles.MaxZoom; i++ {
		pastTheEnd += uint64(1) << (2 * i)
	}
	if _, ok, err := r.TileByID(pastTheEnd); err == nil {
		t.Errorf("TileByID(%d) returned ok=%v and no error, and that ID names no tile", pastTheEnd, ok)
	}

	// The paired case, so the three assertions above are not passing merely
	// because everything errors: the same calls with coordinates inside their
	// zoom succeed. ID 5 is tile 2/0/0 by the specification's own table.
	if _, ok, err := r.Tile(2, 0, 0); err != nil || !ok {
		t.Errorf("Tile(2, 0, 0) = ok %v, err %v; that tile is in the archive", ok, err)
	}
	if _, ok, err := r.RawTile(2, 0, 0); err != nil || !ok {
		t.Errorf("RawTile(2, 0, 0) = ok %v, err %v; that tile is in the archive", ok, err)
	}
	if _, ok, err := r.TileByID(5); err != nil || !ok {
		t.Errorf("TileByID(5) = ok %v, err %v; that tile is in the archive", ok, err)
	}
}

// TestReader_MetadataIsAbsentRatherThanEmpty. The spec requires a metadata
// section, an archive can still lack one, and that is not a reason to refuse
// to read tiles. What the caller must be able to see is the difference between
// "no metadata section" and "a metadata section holding nothing", because the
// attribution string is read from it and an empty string is a credit that
// would be drawn as blank.
func TestReader_MetadataIsAbsentRatherThanEmpty(t *testing.T) {
	built := build(t, osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{{ID: 5, Data: []byte("tile")}},
	})
	// Zero the metadata length in the header, which is what an archive with no
	// metadata section looks like.
	b := append([]byte(nil), built.Bytes...)
	binary.LittleEndian.PutUint64(b[32:], 0)
	r, err := pmtiles.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, err := r.Metadata()
	if err != nil {
		t.Errorf("Metadata on an archive with no metadata section failed with %v, and a missing section is not a failure", err)
	}
	if got != nil {
		t.Errorf("Metadata returned %q, want nil so the caller can tell an absent section from an empty one", got)
	}
	// The tiles still read, which is the reason absence is not an error here.
	if _, ok, err := r.Tile(2, 0, 0); err != nil || !ok {
		t.Errorf("Tile after a missing metadata section = ok %v, err %v", ok, err)
	}
}

// TestNewReader_RefusesANilSource, because the alternative is a reader that
// panics on the first range read, at a point that says nothing about where the
// nil came from.
func TestNewReader_RefusesANilSource(t *testing.T) {
	if _, err := pmtiles.NewReader(nil); err == nil {
		t.Error("NewReader(nil) returned a reader")
	}
}
