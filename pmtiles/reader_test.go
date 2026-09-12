package pmtiles_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// anchors are the tile ID / coordinate pairs from the PMTiles specification's
// own table, used here so that the reader tests do not depend on this
// package's Hilbert conversion being right. If both were wrong in the same
// way, a fixture built with ZxyToID and read back with ZxyToID would agree
// with itself perfectly.
var anchors = []struct {
	id   uint64
	z    uint8
	x, y uint32
}{
	{0, 0, 0, 0},
	{1, 1, 0, 0},
	{2, 1, 0, 1},
	{3, 1, 1, 1},
	{4, 1, 1, 0},
	{5, 2, 0, 0},
	{19078479, 12, 3423, 1763},
}

// TestReader_ReturnsTheBytesItWasGiven is the first verifiable thing this
// package owes: golden tile bytes out of a synthetic archive, across every
// archive shape the format allows.
//
// Each shape is the same seven tiles, so a failure names the shape rather than
// the data. The leaf shapes also assert the DEPTH they were built to: ask for
// leaves and get none and the test would read every tile out of the root while
// claiming to cover leaf traversal.
func TestReader_ReturnsTheBytesItWasGiven(t *testing.T) {
	tiles := make([]osmbasetest.ArchiveTile, 0, len(anchors))
	for _, a := range anchors {
		tiles = append(tiles, osmbasetest.ArchiveTile{ID: a.id, Data: payload(a.id)})
	}

	cases := []struct {
		name       string
		archive    osmbasetest.Archive
		wantLevels int
	}{
		{
			name:    "leafless and uncompressed",
			archive: osmbasetest.Archive{Tiles: tiles},
		},
		{
			name: "leafless with gzip throughout",
			archive: osmbasetest.Archive{
				Tiles:               tiles,
				InternalCompression: pmtiles.CompressionGzip,
				TileCompression:     pmtiles.CompressionGzip,
			},
		},
		{
			name: "gzip directories and uncompressed tiles",
			archive: osmbasetest.Archive{
				Tiles:               tiles,
				InternalCompression: pmtiles.CompressionGzip,
				TileCompression:     pmtiles.CompressionNone,
			},
		},
		{
			name:       "one level of leaf directories",
			archive:    osmbasetest.Archive{Tiles: tiles, LeafSize: 3},
			wantLevels: 1,
		},
		{
			name:       "nested leaf directories",
			archive:    osmbasetest.Archive{Tiles: tiles, LeafSize: 2},
			wantLevels: 2,
		},
		{
			name: "nested leaf directories with gzip",
			archive: osmbasetest.Archive{
				Tiles:               tiles,
				LeafSize:            2,
				InternalCompression: pmtiles.CompressionGzip,
				TileCompression:     pmtiles.CompressionGzip,
			},
			wantLevels: 2,
		},
		{
			name:    "unclustered",
			archive: osmbasetest.Archive{Tiles: tiles, Unclustered: true},
		},
		{
			name:       "unclustered behind leaves",
			archive:    osmbasetest.Archive{Tiles: tiles, Unclustered: true, LeafSize: 3},
			wantLevels: 1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			built := build(t, c.archive)
			if built.LeafLevels != c.wantLevels {
				t.Fatalf("the fixture has %d levels of leaf directories, and this case is meant to exercise %d", built.LeafLevels, c.wantLevels)
			}
			r := reader(t, built)
			for _, a := range anchors {
				got, ok, err := r.Tile(a.z, a.x, a.y)
				if err != nil {
					t.Fatalf("Tile(%d/%d/%d): %v", a.z, a.x, a.y, err)
				}
				if !ok {
					t.Fatalf("Tile(%d/%d/%d) reported the tile absent", a.z, a.x, a.y)
				}
				if !bytes.Equal(got, payload(a.id)) {
					t.Errorf("Tile(%d/%d/%d) = %q, want %q", a.z, a.x, a.y, got, payload(a.id))
				}
			}
		})
	}
}

// TestReader_ReadsATileFromASecondLeafDirectory pins the case a fixture with
// one leaf would silently miss: an archive whose leaves are all reachable
// through the first entry of the root exercises none of the search that picks
// the right leaf.
func TestReader_ReadsATileFromASecondLeafDirectory(t *testing.T) {
	// Four tiles at two per leaf gives two leaves, so IDs 30 and 40 are behind
	// the first root entry and 50 and 60 behind the second.
	built := build(t, osmbasetest.Archive{
		LeafSize: 2,
		Tiles: []osmbasetest.ArchiveTile{
			{ID: 30, Data: []byte("thirty")},
			{ID: 40, Data: []byte("forty")},
			{ID: 50, Data: []byte("fifty")},
			{ID: 60, Data: []byte("sixty")},
		},
	})
	if built.LeafLevels != 1 || built.RootEntries != 2 {
		t.Fatalf("the fixture is %d leaf levels with a root of %d entries, want 1 and 2", built.LeafLevels, built.RootEntries)
	}
	r := reader(t, built)
	for id, want := range map[uint64]string{30: "thirty", 40: "forty", 50: "fifty", 60: "sixty"} {
		got, ok, err := r.TileByID(id)
		if err != nil {
			t.Fatalf("TileByID(%d): %v", id, err)
		}
		if !ok {
			t.Fatalf("TileByID(%d) reported the tile absent", id)
		}
		if string(got) != want {
			t.Errorf("TileByID(%d) = %q, want %q", id, got, want)
		}
	}
}

// TestReader_ARunServesEveryTileInIt. Run-length encoding is how an archive
// stores a stretch of identical tiles once, and the run length is the only
// record of which IDs the single blob answers for.
func TestReader_ARunServesEveryTileInIt(t *testing.T) {
	built := build(t, osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{
			{ID: 10, Run: 4, Data: []byte("ocean")},
			{ID: 20, Data: []byte("coast")},
		},
	})
	r := reader(t, built)
	for id := uint64(10); id <= 13; id++ {
		got, ok, err := r.TileByID(id)
		if err != nil || !ok {
			t.Fatalf("TileByID(%d) = ok %v, err %v; the run covers IDs 10 to 13", id, ok, err)
		}
		if string(got) != "ocean" {
			t.Errorf("TileByID(%d) = %q, want %q", id, got, "ocean")
		}
	}
	// ID 14 is one past the end of the run and there is no entry for it.
	if _, ok, err := r.TileByID(14); ok || err != nil {
		t.Errorf("TileByID(14) = ok %v, err %v; it is past the end of the run", ok, err)
	}
}

// TestReader_AbsentTileIsNotAnError. Most coordinates are not in any archive,
// and that is an answer rather than a failure -- an archive covering one city
// holds nothing for the rest of the planet. A caller must be able to tell "no
// tile here" from "this archive is broken", because the first closes the
// layout up around a gap and the second has to be reported.
func TestReader_AbsentTileIsNotAnError(t *testing.T) {
	built := build(t, osmbasetest.Archive{
		LeafSize: 2,
		Tiles: []osmbasetest.ArchiveTile{
			{ID: 100, Data: []byte("a")},
			{ID: 200, Data: []byte("b")},
			{ID: 300, Data: []byte("c")},
			{ID: 400, Data: []byte("d")},
		},
	})
	r := reader(t, built)
	for _, id := range []uint64{0, 99, 101, 250, 401, 1 << 40} {
		data, ok, err := r.TileByID(id)
		if err != nil {
			t.Errorf("TileByID(%d) failed with %v, and a missing tile is not a failure", id, err)
		}
		if ok || data != nil {
			t.Errorf("TileByID(%d) returned %q, and the archive holds no such tile", id, data)
		}
	}
}

// TestReader_RefusesACompressionItCannotDecode. Brotli and zstd are in the
// format and not in the standard library, and this module admits no
// compression dependency. The failure has to name the compression: an archive
// that cannot be read is a thing the user can fix, and only if they are told
// what it was encoded with.
func TestReader_RefusesACompressionItCannotDecode(t *testing.T) {
	built := build(t, osmbasetest.Archive{
		Tiles:               []osmbasetest.ArchiveTile{{ID: 5, Data: []byte("tile")}},
		InternalCompression: pmtiles.CompressionGzip,
		TileCompression:     pmtiles.CompressionGzip,
	})

	cases := []struct {
		name    string
		offset  int
		code    byte
		wantMsg string
	}{
		{"brotli tiles", 98, byte(pmtiles.CompressionBrotli), "brotli"},
		{"zstd tiles", 98, byte(pmtiles.CompressionZstd), "zstd"},
		{"tiles with no recorded compression", 98, byte(pmtiles.CompressionUnknown), "compression code 0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := append([]byte(nil), built.Bytes...)
			b[c.offset] = c.code
			r, err := pmtiles.NewReader(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			_, _, err = r.Tile(2, 0, 0)
			if err == nil {
				t.Fatal("the tile decoded despite a compression this reader does not implement")
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("error was %q, and it should say %q", err, c.wantMsg)
			}
		})
	}

	// The same for the directories, which fails at open rather than at lookup.
	b := append([]byte(nil), built.Bytes...)
	b[97] = byte(pmtiles.CompressionZstd)
	if _, err := pmtiles.NewReader(bytes.NewReader(b)); err == nil || !strings.Contains(err.Error(), "zstd") {
		t.Errorf("opening an archive with zstd directories gave %v, want an error naming zstd", err)
	}
}

// TestReader_RawTileReturnsTheStoredBytes. A store keeps tiles compressed
// exactly as the source held them, so the bytes that come out here must be the
// gzip stream and not the tile.
func TestReader_RawTileReturnsTheStoredBytes(t *testing.T) {
	built := build(t, osmbasetest.Archive{
		Tiles:           []osmbasetest.ArchiveTile{{ID: 5, Data: []byte("the tile content")}},
		TileCompression: pmtiles.CompressionGzip,
	})
	r := reader(t, built)

	raw, ok, err := r.RawTile(2, 0, 0)
	if err != nil || !ok {
		t.Fatalf("RawTile: ok %v, err %v", ok, err)
	}
	if bytes.Equal(raw, []byte("the tile content")) {
		t.Fatal("RawTile returned the decompressed tile; it must return the bytes as stored")
	}
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		t.Errorf("RawTile returned %v, which does not start with the gzip magic", raw[:min(2, len(raw))])
	}

	plain, ok, err := r.Tile(2, 0, 0)
	if err != nil || !ok {
		t.Fatalf("Tile: ok %v, err %v", ok, err)
	}
	if string(plain) != "the tile content" {
		t.Errorf("Tile = %q, want %q", plain, "the tile content")
	}
}

// TestReader_Metadata reads the JSON section back, which is where a slice's
// attribution string comes from.
func TestReader_Metadata(t *testing.T) {
	const meta = `{"name":"synthetic","attribution":"nobody"}`
	for _, c := range []pmtiles.Compression{pmtiles.CompressionNone, pmtiles.CompressionGzip} {
		built := build(t, osmbasetest.Archive{
			Tiles:               []osmbasetest.ArchiveTile{{ID: 5, Data: []byte("tile")}},
			InternalCompression: c,
			Metadata:            []byte(meta),
		})
		got, err := reader(t, built).Metadata()
		if err != nil {
			t.Fatalf("Metadata with %v directories: %v", c, err)
		}
		if string(got) != meta {
			t.Errorf("Metadata with %v directories = %q, want %q", c, got, meta)
		}
	}
}

// TestReader_SurvivesEveryTruncation truncates a working archive at every
// length and requires an error, never a panic and never a tile.
//
// This is the shape a network failure takes, and it is the shape a half-copied
// file on disk takes. A decoder that indexes into a short buffer crashes the
// whole render; one that returns a short tile hands plausible rubbish to the
// vector tile decoder.
func TestReader_SurvivesEveryTruncation(t *testing.T) {
	built := build(t, osmbasetest.Archive{
		LeafSize:            2,
		InternalCompression: pmtiles.CompressionGzip,
		TileCompression:     pmtiles.CompressionGzip,
		Tiles: []osmbasetest.ArchiveTile{
			{ID: 10, Data: []byte("first tile")},
			{ID: 20, Data: []byte("second tile")},
			{ID: 30, Data: []byte("third tile")},
			{ID: 40, Data: []byte("fourth tile")},
		},
	})
	for n := 0; n < len(built.Bytes); n++ {
		r, err := pmtiles.NewReader(bytes.NewReader(built.Bytes[:n]))
		if err != nil {
			continue // Refusing to open a short archive is the right answer.
		}
		for _, id := range []uint64{10, 20, 30, 40} {
			data, ok, err := r.TileByID(id)
			if err != nil {
				continue
			}
			if ok && !isOneOfTheTiles(data) {
				t.Fatalf("an archive truncated to %d of %d bytes served %q for tile %d", n, len(built.Bytes), data, id)
			}
		}
	}
}

// TestReader_CorruptedBytesFailCleanly flips bytes throughout a working
// archive and reads through every one of the results.
//
// This is a cheap stand-in for a fuzzer, and what it looks for is a crash, or a
// failure that escapes without being named -- not a wrong tile.
//
// It does not demand the right tile because PMTiles carries no checksum
// anywhere, so a flipped byte in an offset can name a different blob WITHIN the
// tile data section, and the bytes found there are a real tile. That much no
// reader can detect.
//
// It is only that much, though. An offset landing outside the tile data section
// altogether IS detectable, because the header states where that section starts
// and how long it is -- and until those two fields were used, an offset near
// 2^64 wrapped round the addition and served the archive's own header as a
// tile. TestReader_RefusesAnEntryOutsideItsSection covers that directly; here
// it shows up as the flips that now fail instead of returning bytes.
//
// What the reader does owe, and what is checked here, is that a damaged archive
// never panics, never reports a tile present with no bytes, and never returns a
// bare io error: every failure is wrapped with what was being read when it
// happened, because "unexpected EOF" on its own tells a user nothing about
// which archive is broken or where.
func TestReader_CorruptedBytesFailCleanly(t *testing.T) {
	built := build(t, osmbasetest.Archive{
		LeafSize: 2,
		Tiles: []osmbasetest.ArchiveTile{
			{ID: 10, Data: []byte("first tile")},
			{ID: 20, Data: []byte("second tile")},
			{ID: 30, Data: []byte("third tile")},
			{ID: 40, Data: []byte("fourth tile")},
		},
	})
	for i := range built.Bytes {
		for _, flip := range []byte{0xff, 0x01, 0x80} {
			b := append([]byte(nil), built.Bytes...)
			b[i] ^= flip
			r, err := pmtiles.NewReader(bytes.NewReader(b))
			if err != nil {
				if !strings.HasPrefix(err.Error(), "pmtiles: ") {
					t.Fatalf("byte %d flipped with %#x: NewReader failed with an unnamed error: %v", i, flip, err)
				}
				continue
			}
			for _, id := range []uint64{10, 20, 30, 40, 11, 25, 1 << 50} {
				data, ok, err := r.TileByID(id)
				switch {
				case err != nil && !strings.HasPrefix(err.Error(), "pmtiles: "):
					t.Fatalf("byte %d flipped with %#x: tile %d failed with an unnamed error: %v", i, flip, id, err)
				case err == nil && ok && len(data) == 0:
					t.Fatalf("byte %d flipped with %#x: tile %d reported present with no bytes", i, flip, id)
				}
			}
		}
	}
}

// TestOpen_ReadsAnArchiveFromDisk covers the path a CLI takes, including that
// Close releases the file it opened.
func TestOpen_ReadsAnArchiveFromDisk(t *testing.T) {
	built := build(t, osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{{ID: 5, Data: []byte("on disk")}},
	})
	path := filepath.Join(t.TempDir(), "synthetic.pmtiles")
	if err := os.WriteFile(path, built.Bytes, 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	r, err := pmtiles.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	got, ok, err := r.Tile(2, 0, 0)
	if err != nil || !ok {
		t.Fatalf("Tile: ok %v, err %v", ok, err)
	}
	if string(got) != "on disk" {
		t.Errorf("Tile = %q, want %q", got, "on disk")
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestOpen_ReportsAMissingFile, with the path in the message, because the path
// is the only thing that makes the failure actionable.
func TestOpen_ReportsAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.pmtiles")
	_, err := pmtiles.Open(path)
	if err == nil {
		t.Fatal("Open succeeded on a file that does not exist")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error was %q, and it should name %s", err, path)
	}
}

// TestReader_RootEntriesIsACopy. The root directory is the archive's index and
// a caller planning byte ranges will hold it; handing out the reader's own
// slice would let that caller corrupt every subsequent lookup.
func TestReader_RootEntriesIsACopy(t *testing.T) {
	built := build(t, osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{
			{ID: 5, Data: []byte("tile")},
			{ID: 6, Data: []byte("other")},
		},
	})
	r := reader(t, built)
	entries := r.RootEntries()
	if len(entries) != 2 {
		t.Fatalf("root has %d entries, want 2", len(entries))
	}
	entries[0].TileID = 999
	if again := r.RootEntries(); again[0].TileID != 5 {
		t.Errorf("changing the returned entries changed the reader's own root: tile ID is now %d", again[0].TileID)
	}
}

func payload(id uint64) []byte {
	return []byte(strings.Repeat("t", 1+int(id%7)) + "-" + string(rune('a'+id%26)))
}

func isOneOfTheTiles(data []byte) bool {
	for _, want := range []string{"first tile", "second tile", "third tile", "fourth tile"} {
		if string(data) == want {
			return true
		}
	}
	return false
}

func reader(t *testing.T, built osmbasetest.BuiltArchive) *pmtiles.Reader {
	t.Helper()
	r, err := pmtiles.NewReader(bytes.NewReader(built.Bytes))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return r
}
