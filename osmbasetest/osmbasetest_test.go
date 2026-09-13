package osmbasetest_test

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// These tests deliberately do NOT decode the fixtures with this module's own
// readers. Those readers are among the things the fixtures exist to test, so a
// round trip would only prove the two agree -- and an encoder and a decoder
// that are wrong in the same way agree perfectly. What is checked here is the
// bytes themselves, against the layout derived by hand in each comment. The
// round trips live in the pmtiles and mvt packages, where they belong.

// TestBuildArchive_ByteLayoutOfTheSmallestArchive pins the whole file for a
// one-tile archive, derived here from the specification.
//
// The root directory holds one entry, and a directory is five runs of varints:
// the count, the tile IDs as deltas, the run lengths, the lengths, and the
// offsets as offset+1. So one tile of one byte at ID 5 is
//
//	01     one entry
//	05     tile ID 5, a delta from 0
//	01     run length 1
//	01     length 1 byte
//	01     offset 0, written as offset+1
//
// which is five bytes. The metadata defaults to the two bytes "{}", there are
// no leaf directories, and the tile data is the single byte of tile content --
// so the sections are 127 + 5 + 2 + 0 + 1 and the archive is 135 bytes.
func TestBuildArchive_ByteLayoutOfTheSmallestArchive(t *testing.T) {
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{{ID: 5, Data: []byte("x")}},
	})
	if err != nil {
		t.Fatalf("BuildArchive: %v", err)
	}
	b := built.Bytes
	if len(b) != 135 {
		t.Fatalf("archive is %d bytes, want 135", len(b))
	}
	if string(b[0:7]) != "PMTiles" || b[7] != 3 {
		t.Errorf("archive starts %q, version byte %d; want %q and 3", b[0:7], b[7], "PMTiles")
	}
	if got, want := b[127:132], []byte{0x01, 0x05, 0x01, 0x01, 0x01}; !bytes.Equal(got, want) {
		t.Errorf("root directory = %v, want %v", got, want)
	}
	if got := string(b[132:134]); got != "{}" {
		t.Errorf("metadata = %q, want %q", got, "{}")
	}
	if got := string(b[134:]); got != "x" {
		t.Errorf("tile data = %q, want %q", got, "x")
	}

	u64 := func(off int) uint64 { return binary.LittleEndian.Uint64(b[off : off+8]) }
	for _, c := range []struct {
		name      string
		got, want uint64
	}{
		{"root offset", u64(8), 127},
		{"root length", u64(16), 5},
		{"metadata offset", u64(24), 132},
		{"metadata length", u64(32), 2},
		{"leaf offset", u64(40), 134},
		{"leaf length", u64(48), 0},
		{"tile data offset", u64(56), 134},
		{"tile data length", u64(64), 1},
		{"addressed tiles", u64(72), 1},
		{"tile entries", u64(80), 1},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if b[96] != 1 {
		t.Error("the clustered flag is not set on a clustered archive")
	}
	if b[97] != byte(pmtiles.CompressionNone) || b[98] != byte(pmtiles.CompressionNone) {
		t.Errorf("compression bytes are %d and %d; an unset compression must be written as none, not as unknown", b[97], b[98])
	}
	if built.LeafLevels != 0 || built.RootEntries != 1 {
		t.Errorf("reported shape is %d leaf levels and %d root entries, want 0 and 1", built.LeafLevels, built.RootEntries)
	}
	if built.TileDataOffset != 134 {
		t.Errorf("reported tile data offset %d, want 134", built.TileDataOffset)
	}
}

// TestEncodeDirectory_OffsetEncoding pins the two ways an offset is written.
//
// An offset equal to the previous entry's end is a single zero byte; any other
// offset is written as offset+1, which is what keeps zero free to mean
// "contiguous". The first entry is never written as zero, because there is no
// previous entry for it to follow and the decoder would compute 0-1.
func TestEncodeDirectory_OffsetEncoding(t *testing.T) {
	got := osmbasetest.EncodeDirectory([]pmtiles.Entry{
		{TileID: 0, Offset: 0, Length: 3, RunLength: 1},
		{TileID: 1, Offset: 3, Length: 4, RunLength: 2},
		{TileID: 3, Offset: 100, Length: 5, RunLength: 1},
	})
	want := []byte{
		0x03,             // three entries
		0x00, 0x01, 0x02, // tile IDs 0, 1, 3 as deltas
		0x01, 0x02, 0x01, // run lengths
		0x03, 0x04, 0x05, // lengths
		0x01, // entry 0 at offset 0, written as offset+1
		0x00, // entry 1 at offset 3, which is where entry 0 ended
		0x65, // entry 2 at offset 100, written as 101
	}
	if !bytes.Equal(got, want) {
		t.Errorf("directory = %v\nwant       %v", got, want)
	}
}

// TestBuildArchive_LeafStructure checks that the builder makes the archive
// shape a test asked for, because a test meaning to exercise nested leaves and
// silently getting a flat root would cover nothing and pass.
//
// Directories hold at most LeafSize entries. n tiles therefore need
// ceil(n/LeafSize) leaves, and if that is still more than LeafSize the leaves
// need a level above them, and so on until one directory holds what is left.
func TestBuildArchive_LeafStructure(t *testing.T) {
	cases := []struct {
		name        string
		tiles       int
		leafSize    int
		wantLevels  int
		wantRootLen int
	}{
		{"leafless by default", 8, 0, 0, 8},
		{"leafless when the root is big enough", 8, 8, 0, 8},
		{"one leaf level", 8, 4, 1, 2},
		{"one leaf level with a short last leaf", 7, 4, 1, 2},
		{"two leaf levels", 8, 2, 2, 2},
		{"three leaf levels", 9, 2, 3, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tiles := make([]osmbasetest.ArchiveTile, c.tiles)
			for i := range tiles {
				tiles[i] = osmbasetest.ArchiveTile{ID: uint64(i) * 10, Data: []byte{byte(i)}}
			}
			built, err := osmbasetest.BuildArchive(osmbasetest.Archive{Tiles: tiles, LeafSize: c.leafSize})
			if err != nil {
				t.Fatalf("BuildArchive: %v", err)
			}
			if built.LeafLevels != c.wantLevels {
				t.Errorf("%d tiles at LeafSize %d gave %d leaf levels, want %d", c.tiles, c.leafSize, built.LeafLevels, c.wantLevels)
			}
			if built.RootEntries != c.wantRootLen {
				t.Errorf("%d tiles at LeafSize %d gave a root of %d entries, want %d", c.tiles, c.leafSize, built.RootEntries, c.wantRootLen)
			}
		})
	}
}

// TestBuildArchive_ClusteredAndUnclusteredLayouts. Clustered means the blobs
// sit in tile ID order, which is what makes almost every offset the one-byte
// contiguous form; unclustered writes them backwards so that none of them is.
// Both are legal archives and a reader has to handle each.
func TestBuildArchive_ClusteredAndUnclusteredLayouts(t *testing.T) {
	tiles := []osmbasetest.ArchiveTile{
		{ID: 1, Data: []byte("aaa")},
		{ID: 2, Data: []byte("bb")},
		{ID: 3, Data: []byte("c")},
	}
	clustered, err := osmbasetest.BuildArchive(osmbasetest.Archive{Tiles: tiles})
	if err != nil {
		t.Fatalf("BuildArchive: %v", err)
	}
	unclustered, err := osmbasetest.BuildArchive(osmbasetest.Archive{Tiles: tiles, Unclustered: true})
	if err != nil {
		t.Fatalf("BuildArchive: %v", err)
	}

	if clustered.Bytes[96] != 1 {
		t.Error("the clustered archive does not have the clustered flag set")
	}
	if unclustered.Bytes[96] != 0 {
		t.Error("the unclustered archive has the clustered flag set")
	}
	if got := string(clustered.Bytes[clustered.TileDataOffset:]); got != "aaabbc" {
		t.Errorf("clustered tile data = %q, want %q", got, "aaabbc")
	}
	if got := string(unclustered.Bytes[unclustered.TileDataOffset:]); got != "cbbaaa" {
		t.Errorf("unclustered tile data = %q, want %q", got, "cbbaaa")
	}
	// The directories differ only in their offset run, which is the last three
	// bytes of each: three entries at IDs 1, 2 and 3 with lengths 3, 2 and 1.
	//
	// Clustered, the blobs sit at 0, 3 and 5, so the first is written as
	// offset+1 and the other two as the one-byte "directly after the previous
	// entry". Unclustered, they sit at 3, 1 and 0 -- the reverse layout -- and
	// none of those follows its predecessor, so all three are written as
	// offset+1: 4, 2 and 1.
	wantRoot := func(offsets ...byte) []byte {
		return append([]byte{0x03, 1, 1, 1, 1, 1, 1, 3, 2, 1}, offsets...)
	}
	rootOf := func(b []byte) []byte {
		length := binary.LittleEndian.Uint64(b[16:24])
		return b[127 : 127+length]
	}
	if got, want := rootOf(clustered.Bytes), wantRoot(1, 0, 0); !bytes.Equal(got, want) {
		t.Errorf("clustered root directory = %v, want %v", got, want)
	}
	if got, want := rootOf(unclustered.Bytes), wantRoot(4, 2, 1); !bytes.Equal(got, want) {
		t.Errorf("unclustered root directory = %v, want %v", got, want)
	}
}

// TestBuildArchive_IsDeterministic. A fixture that encoded differently from
// run to run would make every byte-level assertion in this module flaky, and
// gzip is the usual way that happens -- a writer that stamped a modification
// time into the header would do it.
func TestBuildArchive_IsDeterministic(t *testing.T) {
	spec := osmbasetest.Archive{
		LeafSize:            2,
		InternalCompression: pmtiles.CompressionGzip,
		TileCompression:     pmtiles.CompressionGzip,
		Tiles: []osmbasetest.ArchiveTile{
			{ID: 5, Data: []byte("one")},
			{ID: 9, Run: 3, Data: []byte("two")},
			{ID: 40, Data: []byte("three")},
			{ID: 41, Data: []byte("four")},
		},
	}
	first, err := osmbasetest.BuildArchive(spec)
	if err != nil {
		t.Fatalf("BuildArchive: %v", err)
	}
	second, err := osmbasetest.BuildArchive(spec)
	if err != nil {
		t.Fatalf("BuildArchive: %v", err)
	}
	if !bytes.Equal(first.Bytes, second.Bytes) {
		t.Error("two builds of the same archive produced different bytes")
	}
}

// TestBuildArchive_RefusesArchivesNoReaderCouldTrust. The builder is allowed
// to write damaged archives only where a test asks for damage by hand; a
// fixture that quietly encoded an impossible one would send a test chasing a
// bug in the reader.
func TestBuildArchive_RefusesArchivesNoReaderCouldTrust(t *testing.T) {
	cases := []struct {
		name    string
		archive osmbasetest.Archive
		wantMsg string
	}{
		{
			name: "a tile with no bytes",
			archive: osmbasetest.Archive{
				Tiles: []osmbasetest.ArchiveTile{{ID: 1}},
			},
			wantMsg: "no data",
		},
		{
			name: "a tile inside another tile's run",
			archive: osmbasetest.Archive{
				Tiles: []osmbasetest.ArchiveTile{
					{ID: 10, Run: 5, Data: []byte("a")},
					{ID: 12, Data: []byte("b")},
				},
			},
			wantMsg: "falls inside the run",
		},
		{
			name: "a compression this module cannot write",
			archive: osmbasetest.Archive{
				Tiles:           []osmbasetest.ArchiveTile{{ID: 1, Data: []byte("a")}},
				TileCompression: pmtiles.CompressionBrotli,
			},
			wantMsg: "brotli",
		},
		{
			name: "one entry per leaf directory, which never reduces to a root",
			archive: osmbasetest.Archive{
				Tiles:    manyTiles(8),
				LeafSize: 1,
			},
			wantMsg: "never reduces to a root",
		},
		{
			name: "a root directory past the format's 16 KiB cap",
			archive: osmbasetest.Archive{
				Tiles: manyTiles(4000),
			},
			wantMsg: "caps it at",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := osmbasetest.BuildArchive(c.archive)
			if err == nil {
				t.Fatal("BuildArchive accepted it")
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("error was %q, and it should say %q", err, c.wantMsg)
			}
		})
	}
}

// TestBuildTile_ByteLayoutOfTheSmallestTile pins a whole vector tile, derived
// here from the schema's field numbers.
//
// A tile is repeated layers in field 3; a layer is a name in field 1, features
// in field 2, an extent in field 5 and a version in field 15; a feature is a
// geometry type in field 3 and a packed geometry in field 4. A protobuf field
// key is (number << 3 | wire type), so field 3 length-delimited is 0x1a and
// field 15 varint is 0x78.
//
//	1a 11               field 3, 17 bytes of layer
//	  0a 01 6c            field 1, one byte: the name "l"
//	  12 07               field 2, seven bytes of feature
//	    18 01               field 3 varint: geometry type 1, a point
//	    22 03               field 4, three bytes of packed geometry
//	      09                  MoveTo once: 1 | 1<<3
//	      02                  zigzag +1
//	      04                  zigzag +2
//	  28 80 20            field 5 varint: extent 4096
//	  78 02               field 15 varint: version 2
func TestBuildTile_ByteLayoutOfTheSmallestTile(t *testing.T) {
	got, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "l",
		Features: []osmbasetest.FeatureSpec{{
			Type:     mvt.GeomPoint,
			Geometry: mvt.Geometry{Points: []mvt.Point{{X: 1, Y: 2}}},
		}},
	}}})
	if err != nil {
		t.Fatalf("BuildTile: %v", err)
	}
	want := []byte{
		0x1a, 0x11,
		0x0a, 0x01, 'l',
		0x12, 0x07,
		0x18, 0x01,
		0x22, 0x03, 0x09, 0x02, 0x04,
		0x28, 0x80, 0x20,
		0x78, 0x02,
	}
	if !bytes.Equal(got, want) {
		t.Errorf("tile = % x\nwant   % x", got, want)
	}
}

// TestEncodeGeometry_CursorPersistsAcrossRings pins the delta encoding of a
// polygon with a hole, which is where the cursor rule shows.
//
// The exterior is the 10x10 square from (0,0) and the hole is the 2x2 square
// from (2,2). The hole's opening MoveTo is a delta from the exterior's LAST
// point (0,10), not from its first: +2,-8. ClosePath does not move the cursor,
// so an encoder that reset it to the ring's start would write +2,+2 here and
// produce a tile that draws the hole eight units too low.
func TestEncodeGeometry_CursorPersistsAcrossRings(t *testing.T) {
	got, err := osmbasetest.EncodeGeometry(mvt.GeomPolygon, mvt.Geometry{Polygons: []mvt.Polygon{{
		Exterior: mvt.Ring{pt(0, 0), pt(10, 0), pt(10, 10), pt(0, 10)},
		Holes:    []mvt.Ring{{pt(2, 2), pt(2, 4), pt(4, 4), pt(4, 2)}},
	}}})
	if err != nil {
		t.Fatalf("EncodeGeometry: %v", err)
	}
	want := []uint32{
		9, 0, 0, // MoveTo (0,0)
		26, 20, 0, 0, 20, 19, 0, // LineTo +10,0 then 0,+10 then -10,0
		15,       // ClosePath, cursor stays at (0,10)
		9, 4, 15, // MoveTo +2,-8 -> (2,2)
		26, 0, 4, 4, 0, 0, 3, // LineTo 0,+2 then +2,0 then 0,-2
		15, // ClosePath
	}
	if len(got) != len(want) {
		t.Fatalf("geometry = %v\nwant       %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("geometry = %v\nwant       %v", got, want)
		}
	}
}

// TestBuildTile_IsDeterministic. The key and value tables are built in
// first-appearance order rather than by ranging over a map, so that one spec
// has one encoding.
func TestBuildTile_IsDeterministic(t *testing.T) {
	spec := osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: "roads",
		Features: []osmbasetest.FeatureSpec{
			{
				Type: mvt.GeomPoint,
				Tags: []osmbasetest.Tag{
					{Key: "name", Value: mvt.StringValue("a")},
					{Key: "kind", Value: mvt.StringValue("path")},
					{Key: "layer", Value: mvt.IntValue(-1)},
				},
				Geometry: mvt.Geometry{Points: []mvt.Point{pt(1, 1)}},
			},
			{
				Type: mvt.GeomPoint,
				Tags: []osmbasetest.Tag{
					{Key: "kind", Value: mvt.StringValue("path")},
					{Key: "name", Value: mvt.StringValue("b")},
				},
				Geometry: mvt.Geometry{Points: []mvt.Point{pt(2, 2)}},
			},
		},
	}}}
	for i := 0; i < 20; i++ {
		first, err := osmbasetest.BuildTile(spec)
		if err != nil {
			t.Fatalf("BuildTile: %v", err)
		}
		second, err := osmbasetest.BuildTile(spec)
		if err != nil {
			t.Fatalf("BuildTile: %v", err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("two builds of the same tile produced different bytes on pass %d", i)
		}
	}
}

// pt spells a point with its fields named, which go vet wants for a struct
// from another package and which costs nothing here.
func pt(x, y int32) mvt.Point { return mvt.Point{X: x, Y: y} }

func manyTiles(n int) []osmbasetest.ArchiveTile {
	tiles := make([]osmbasetest.ArchiveTile, n)
	for i := range tiles {
		tiles[i] = osmbasetest.ArchiveTile{ID: uint64(i) * 1000, Data: []byte("tile")}
	}
	return tiles
}
