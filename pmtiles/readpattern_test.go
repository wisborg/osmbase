package pmtiles_test

import (
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// How many times the archive is READ is a correctness concern for this package
// rather than a performance one, which is why these assert a count instead of
// measuring a duration.
//
// Reader is built on io.ReaderAt so that one implementation serves a local file
// now and an HTTP range source at acquisition time. Over a range source every
// call to ReadAt is a request and a round trip, so a reader that fetches one
// section in fourteen pieces turns one tile into twenty-eight requests and a
// cell of eighty-five tiles into two and a half thousand. That is slow, and
// more to the point it is the traffic pattern most likely to look like abuse to
// a host this project has no agreement with. Against the bytes.Reader every
// other test in this package uses, two calls and twenty-eight are
// indistinguishable.
//
// The fixtures are sized on purpose. io.SectionReader truncates a read to what
// is left of its section, so any section of 512 bytes or fewer is one call
// however it is fetched, and a fixture with five-byte tiles cannot tell a
// reader that reads once from one that grows a buffer. Every section measured
// below is comfortably past that.

// bigRootArchive is leafless, so its root directory holds an entry per tile and
// is a few kilobytes: enough that fetching it in instalments is visible.
func bigRootArchive(t *testing.T) osmbasetest.BuiltArchive {
	t.Helper()
	tiles := make([]osmbasetest.ArchiveTile, 400)
	for i := range tiles {
		tiles[i] = osmbasetest.ArchiveTile{ID: uint64(i), Data: []byte("tile")}
	}
	built := build(t, osmbasetest.Archive{Tiles: tiles})
	h, err := pmtiles.ParseHeader(built.Bytes)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if h.RootLength <= 512 {
		t.Fatalf("the fixture's root directory is %d bytes; under 512 a section reader fetches it in one call whatever the reader does, and this fixture would prove nothing", h.RootLength)
	}
	return built
}

// realisticArchive has leaf directories of a kilobyte or two and tiles of
// sixteen kilobytes, which are the orders of magnitude a real vector tile
// archive has. Nothing is compressed, so a section's stored length is exactly
// its size -- and the stored length is what drives the read pattern, since
// every byte is fetched before anything is decompressed.
func realisticArchive(t *testing.T) osmbasetest.BuiltArchive {
	t.Helper()
	const (
		count    = 600
		tileSize = 16 << 10
	)
	tiles := make([]osmbasetest.ArchiveTile, count)
	for i := range tiles {
		body := make([]byte, tileSize)
		for j := range body {
			body[j] = byte(i + j)
		}
		tiles[i] = osmbasetest.ArchiveTile{ID: uint64(i), Data: body}
	}
	built := build(t, osmbasetest.Archive{Tiles: tiles, LeafSize: 200})
	if built.LeafLevels != 1 || built.LeafDirectories != 3 {
		t.Fatalf("the fixture has %d leaf levels and %d leaf directories, want 1 and 3", built.LeafLevels, built.LeafDirectories)
	}
	return built
}

// TestReader_OpeningAnArchiveCostsTwoReads: the header, then the root
// directory it points at, each in one call.
//
// Two is pinned exactly rather than bounded. The format caps the header plus
// the compressed root at 16 KiB precisely so a latency-sensitive client can
// fetch both in ONE request; this reader does not do that yet, so two is the
// honest count and a prefetch would later make it one. What must not happen is
// the count rising with the size of the root, which is what it did.
func TestReader_OpeningAnArchiveCostsTwoReads(t *testing.T) {
	built := bigRootArchive(t)
	h, err := pmtiles.ParseHeader(built.Bytes)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}

	src := newCountingSource(built.Bytes)
	if _, err := pmtiles.NewReader(src); err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	reads := src.takeReads()
	if len(reads) != 2 {
		t.Fatalf("opening the archive took %d reads, want 2 -- the header and the root directory: %s", len(reads), src.describe(reads))
	}
	if reads[0].length != pmtiles.HeaderSize || reads[0].offset != 0 {
		t.Errorf("the first read was %d bytes at %d, want the %d-byte header at 0", reads[0].length, reads[0].offset, pmtiles.HeaderSize)
	}
	if reads[1].length != int(h.RootLength) || reads[1].offset != int64(h.RootOffset) {
		t.Errorf("the second read was %d bytes at %d, want the whole %d-byte root directory at %d", reads[1].length, reads[1].offset, h.RootLength, h.RootOffset)
	}
}

// TestReader_FetchingATileCostsOneReadPerSection is the regression test.
//
// A tile behind one level of leaf directories is two sections: the leaf, then
// the tile. Each must be exactly one read, whatever it weighs.
//
// The warm case is its pair. With the leaf already cached a second tile in the
// same leaf must be exactly one read, which is what separates "one read per
// section" from "one read, because the leaf was never fetched at all".
func TestReader_FetchingATileCostsOneReadPerSection(t *testing.T) {
	built := realisticArchive(t)
	src := newCountingSource(built.Bytes)
	r, err := pmtiles.NewReader(src)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	src.takeReads()

	if _, ok, err := r.TileByID(250); err != nil || !ok {
		t.Fatalf("TileByID(250): ok %v, err %v", ok, err)
	}
	cold := src.takeReads()
	if len(cold) != 2 {
		t.Errorf("fetching a tile behind one leaf took %d reads, want 2 -- the leaf directory and the tile: %s", len(cold), src.describe(cold))
	}

	if _, ok, err := r.TileByID(251); err != nil || !ok {
		t.Fatalf("TileByID(251): ok %v, err %v", ok, err)
	}
	warm := src.takeReads()
	if len(warm) != 1 {
		t.Errorf("fetching a second tile from a cached leaf took %d reads, want 1 -- the tile alone: %s", len(warm), src.describe(warm))
	}
}

// TestReader_FetchingATileThroughNestedLeavesCostsOneReadPerLevel. Two levels
// of leaf directory are three sections, and three reads: a per-level cost is
// the design, a per-kilobyte one is the defect.
func TestReader_FetchingATileThroughNestedLeavesCostsOneReadPerLevel(t *testing.T) {
	tiles := make([]osmbasetest.ArchiveTile, 600)
	for i := range tiles {
		body := make([]byte, 4<<10)
		for j := range body {
			body[j] = byte(i + j)
		}
		tiles[i] = osmbasetest.ArchiveTile{ID: uint64(i), Data: body}
	}
	built := build(t, osmbasetest.Archive{Tiles: tiles, LeafSize: 20})
	if built.LeafLevels != 2 {
		t.Fatalf("the fixture has %d leaf levels, want 2", built.LeafLevels)
	}

	src := newCountingSource(built.Bytes)
	r, err := pmtiles.NewReader(src)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	src.takeReads()

	if _, ok, err := r.TileByID(313); err != nil || !ok {
		t.Fatalf("TileByID(313): ok %v, err %v", ok, err)
	}
	reads := src.takeReads()
	if len(reads) != 3 {
		t.Errorf("fetching a tile behind two leaf levels took %d reads, want 3 -- two directories and the tile: %s", len(reads), src.describe(reads))
	}
}

// TestReader_ReadCountDoesNotGrowWithSectionSize states the defect's shape
// directly, and is the assertion that does not depend on any fixture being the
// right size.
//
// The same archive twice over, one with tiles of eight bytes and one with tiles
// of a megabyte. The counts must be equal: the cost of a fetch is a function of
// how many sections it touches, never of how large they are. A reader that
// grows a buffer geometrically passes every other test in this package and
// fails this one -- it took 21 reads for the megabyte tile and 2 for the eight
// bytes.
func TestReader_ReadCountDoesNotGrowWithSectionSize(t *testing.T) {
	countFor := func(size int) (int, []readRecord) {
		t.Helper()
		tiles := make([]osmbasetest.ArchiveTile, 40)
		for i := range tiles {
			body := make([]byte, size)
			for j := range body {
				body[j] = byte(i + j)
			}
			tiles[i] = osmbasetest.ArchiveTile{ID: uint64(i), Data: body}
		}
		built := build(t, osmbasetest.Archive{Tiles: tiles, LeafSize: 8})
		src := newCountingSource(built.Bytes)
		r, err := pmtiles.NewReader(src)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		src.takeReads()
		if _, ok, err := r.TileByID(20); err != nil || !ok {
			t.Fatalf("TileByID(20): ok %v, err %v", ok, err)
		}
		reads := src.takeReads()
		return len(reads), reads
	}

	small, _ := countFor(8)
	large, largeReads := countFor(1 << 20)
	if small != large {
		t.Errorf("fetching an 8-byte tile took %d reads and a 1 MiB tile took %d; the request count is following the section size, not the section count", small, large)
	}
	if large != 2 {
		t.Errorf("fetching a tile behind one leaf took %d reads, want 2: %s", large, newCountingSource(nil).describe(largeReads))
	}
}
