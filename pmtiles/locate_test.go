package pmtiles_test

import (
	"bytes"
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// locateFixture is an archive of four tiles at zoom 12, each with distinct
// bytes, behind a leaf directory so that locating one costs a directory read.
//
// The IDs are consecutive so the tiles are Hilbert neighbours, which is what
// makes them contiguous in the file -- the property the acquisition step's
// coalescing rests on, asserted here at its source rather than only where it
// is used.
func locateFixture(t *testing.T) (osmbasetest.BuiltArchive, []uint64) {
	t.Helper()
	base, err := pmtiles.ZxyToID(12, 3423, 1763)
	if err != nil {
		t.Fatalf("ZxyToID: %v", err)
	}
	ids := []uint64{base, base + 1, base + 2, base + 3}
	tiles := make([]osmbasetest.ArchiveTile, len(ids))
	for i, id := range ids {
		body := make([]byte, 1000+i*100)
		for j := range body {
			body[j] = byte(i*31 + j)
		}
		tiles[i] = osmbasetest.ArchiveTile{ID: id, Data: body}
	}
	return build(t, osmbasetest.Archive{Tiles: tiles, LeafSize: 2}), ids
}

// TestLocate_NamesExactlyTheBytesRawTileReturns is the property the whole fetch
// plan rests on: the offset and length a plan is costed from must address the
// same bytes the download will later store.
//
// The expected value is derived rather than pinned. The archive's bytes are in
// hand, so slicing them at the reported offset and length and comparing with
// what RawTile hands back checks the two independently; a Location that was
// off by the tile data section's base, or that reported a decompressed length,
// would differ here and nowhere else.
func TestLocate_NamesExactlyTheBytesRawTileReturns(t *testing.T) {
	built, ids := locateFixture(t)
	r, err := pmtiles.NewReader(bytes.NewReader(built.Bytes))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	for _, id := range ids {
		z, x, y, err := pmtiles.IDToZxy(id)
		if err != nil {
			t.Fatalf("IDToZxy(%d): %v", id, err)
		}
		raw, ok, err := r.RawTile(z, x, y)
		if err != nil || !ok {
			t.Fatalf("RawTile(%d/%d/%d): ok %v, err %v", z, x, y, ok, err)
		}
		loc, ok, err := r.Locate(z, x, y)
		if err != nil || !ok {
			t.Fatalf("Locate(%d/%d/%d): ok %v, err %v", z, x, y, ok, err)
		}
		if loc.Length != int64(len(raw)) {
			t.Errorf("Locate(%d/%d/%d).Length = %d, but RawTile returned %d bytes", z, x, y, loc.Length, len(raw))
		}
		if loc.Offset < 0 || loc.End() > int64(len(built.Bytes)) {
			t.Fatalf("Locate(%d/%d/%d) = %d..%d, outside a %d-byte archive", z, x, y, loc.Offset, loc.End(), len(built.Bytes))
		}
		if got := built.Bytes[loc.Offset:loc.End()]; !bytes.Equal(got, raw) {
			t.Errorf("the bytes at Locate(%d/%d/%d) are not the ones RawTile returned", z, x, y)
		}
	}
}

// TestLocate_ConsecutiveTilesAreContiguousInTheArchive measures the ordering
// property coalescing depends on.
//
// PMTiles stores tiles in Hilbert ID order, so tiles with consecutive IDs sit
// end to end in a clustered archive. That is why a cell's sub-pyramid -- which
// is a contiguous run of IDs at each zoom, since the Hilbert curve is
// hierarchical -- can be fetched in a handful of requests rather than
// eighty-five. If this stops holding, coalescing stops paying and the
// acquisition step's request counts rise without anything failing.
func TestLocate_ConsecutiveTilesAreContiguousInTheArchive(t *testing.T) {
	built, ids := locateFixture(t)
	r, err := pmtiles.NewReader(bytes.NewReader(built.Bytes))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	var prev pmtiles.Location
	for i, id := range ids {
		z, x, y, err := pmtiles.IDToZxy(id)
		if err != nil {
			t.Fatalf("IDToZxy(%d): %v", id, err)
		}
		loc, ok, err := r.Locate(z, x, y)
		if err != nil || !ok {
			t.Fatalf("Locate(%d/%d/%d): ok %v, err %v", z, x, y, ok, err)
		}
		if i > 0 && loc.Offset != prev.End() {
			t.Errorf("tile ID %d starts at %d and the tile before it ends at %d; consecutive IDs are not contiguous", id, loc.Offset, prev.End())
		}
		prev = loc
	}
}

// TestLocate_ReadsNoTileData is what a dry run's promise reduces to.
//
// "Report exactly what would be downloaded and stop" is only true if planning
// touches the archive's index and nothing else. The tile data section's range
// comes from the header, and no read may fall inside it.
func TestLocate_ReadsNoTileData(t *testing.T) {
	built, ids := locateFixture(t)
	h, err := pmtiles.ParseHeader(built.Bytes)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	src := newCountingSource(built.Bytes)
	r, err := pmtiles.NewReader(src)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	src.takeReads()

	for _, id := range ids {
		z, x, y, err := pmtiles.IDToZxy(id)
		if err != nil {
			t.Fatalf("IDToZxy(%d): %v", id, err)
		}
		if _, ok, err := r.Locate(z, x, y); err != nil || !ok {
			t.Fatalf("Locate(%d/%d/%d): ok %v, err %v", z, x, y, ok, err)
		}
	}
	dataStart := int64(h.TileDataOffset)
	dataEnd := dataStart + int64(h.TileDataLength)
	for _, read := range src.takeReads() {
		if read.offset < dataEnd && read.offset+int64(read.length) > dataStart {
			t.Errorf("locating tiles read %d bytes at offset %d, which is inside the tile data section at %d..%d; a plan must not download what it is costing",
				read.length, read.offset, dataStart, dataEnd)
		}
	}
}

// TestLocate_ARunOfIdenticalTilesSharesOneLocation is the deduplication a plan
// has to honour.
//
// The format serves a run of consecutive tile IDs from a single stored blob,
// which is why a cell over open water is a few kilobytes rather than
// eighty-five separate tiles. A planner that added up a Location per tile
// would quote four times the true cost here, and far more than that over
// ocean. The expected total is derived: four tiles, one blob, so the bytes to
// transfer are that one blob's length however many tiles ask for it.
func TestLocate_ARunOfIdenticalTilesSharesOneLocation(t *testing.T) {
	base, err := pmtiles.ZxyToID(12, 3423, 1763)
	if err != nil {
		t.Fatalf("ZxyToID: %v", err)
	}
	body := bytes.Repeat([]byte{0xAB}, 512)
	built := build(t, osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{{ID: base, Run: 4, Data: body}},
	})
	r, err := pmtiles.NewReader(bytes.NewReader(built.Bytes))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	seen := map[pmtiles.Location]int{}
	for id := base; id < base+4; id++ {
		z, x, y, err := pmtiles.IDToZxy(id)
		if err != nil {
			t.Fatalf("IDToZxy(%d): %v", id, err)
		}
		loc, ok, err := r.Locate(z, x, y)
		if err != nil || !ok {
			t.Fatalf("Locate(%d/%d/%d): ok %v, err %v", z, x, y, ok, err)
		}
		seen[loc]++
	}
	if len(seen) != 1 {
		t.Fatalf("four tiles of one run reported %d distinct locations, want 1: %v", len(seen), seen)
	}
	for loc, n := range seen {
		if n != 4 {
			t.Errorf("the shared location was reported %d times, want 4", n)
		}
		if loc.Length != int64(len(body)) {
			t.Errorf("the shared location is %d bytes, want the one stored blob's %d", loc.Length, len(body))
		}
	}
}

// TestLocate_ATileTheArchiveHasNotGotIsNotAnError. Most coordinates in most
// archives hold no tile, and a planner that read that as a failure could not
// cost a coastal cell.
func TestLocate_ATileTheArchiveHasNotGotIsNotAnError(t *testing.T) {
	built, _ := locateFixture(t)
	r, err := pmtiles.NewReader(bytes.NewReader(built.Bytes))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	loc, ok, err := r.Locate(12, 0, 0)
	if err != nil {
		t.Fatalf("Locate(12/0/0): %v", err)
	}
	if ok {
		t.Errorf("Locate(12/0/0) = %+v, true; the fixture holds no tile there", loc)
	}
}
