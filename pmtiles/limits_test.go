package pmtiles_test

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// crafted describes an archive assembled here rather than by the fixture
// builder, because these tests need archives the builder deliberately refuses
// to write: a directory entry pointing outside its section, two leaves at one
// offset, a section that decompresses to hundreds of megabytes.
type crafted struct {
	root        []pmtiles.Entry
	leafSection []byte // already compressed as InternalCompression says
	metadata    []byte // already compressed
	tileData    []byte // tiles already compressed as TileCompression says
	internal    pmtiles.Compression
	tileComp    pmtiles.Compression
	// patch damages the finished header, for the fields no honest assembly
	// would produce.
	patch func(header []byte)
}

func craftArchive(t *testing.T, c crafted) []byte {
	t.Helper()
	if c.internal == 0 {
		c.internal = pmtiles.CompressionNone
	}
	if c.tileComp == 0 {
		c.tileComp = pmtiles.CompressionNone
	}
	root := osmbasetest.EncodeDirectory(c.root)
	if c.internal == pmtiles.CompressionGzip {
		root = gzipBytes(t, root)
	}

	rootOffset := uint64(pmtiles.HeaderSize)
	metaOffset := rootOffset + uint64(len(root))
	leafOffset := metaOffset + uint64(len(c.metadata))
	dataOffset := leafOffset + uint64(len(c.leafSection))

	h := make([]byte, pmtiles.HeaderSize)
	copy(h, "PMTiles")
	h[7] = 3
	put := func(off int, v uint64) { binary.LittleEndian.PutUint64(h[off:], v) }
	put(8, rootOffset)
	put(16, uint64(len(root)))
	put(24, metaOffset)
	put(32, uint64(len(c.metadata)))
	put(40, leafOffset)
	put(48, uint64(len(c.leafSection)))
	put(56, dataOffset)
	put(64, uint64(len(c.tileData)))
	h[96] = 1
	h[97] = byte(c.internal)
	h[98] = byte(c.tileComp)
	if c.patch != nil {
		c.patch(h)
	}

	var out bytes.Buffer
	out.Write(h)
	out.Write(root)
	out.Write(c.metadata)
	out.Write(c.leafSection)
	out.Write(c.tileData)
	return out.Bytes()
}

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(b); err != nil {
		t.Fatalf("gzipping: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzipping: %v", err)
	}
	return out.Bytes()
}

// zeroBomb returns the gzip of n zero bytes, which is the cheapest way to make
// a stream whose compressed and decompressed sizes are three orders of
// magnitude apart.
func zeroBomb(t *testing.T, n int) []byte {
	t.Helper()
	return gzipBytes(t, make([]byte, n))
}

// TestDefaultLimits pins the numbers, because they are a policy rather than a
// consequence and a silent change to one of them is a silent change to how
// much memory an untrusted archive can spend.
//
// Each is checked against what the format or real data actually contains: a
// conforming compressed root directory is under 16 KiB, so eight megabytes of
// decompressed directory is three orders of magnitude of slack; a dense vector
// tile is well under a megabyte against a limit of sixteen.
func TestDefaultLimits(t *testing.T) {
	l := pmtiles.DefaultLimits()
	if l.Directory != 8<<20 || l.Metadata != 4<<20 || l.Tile != 16<<20 {
		t.Errorf("DefaultLimits() = %+v, want 8 MiB, 4 MiB and 16 MiB", l)
	}
	if l.Directory < pmtiles.MaxRootDirectory {
		t.Errorf("the directory limit of %d is below the format's own compressed root cap of %d, so a conforming archive could not be opened", l.Directory, pmtiles.MaxRootDirectory)
	}
	r := reader(t, build(t, osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{{ID: 5, Data: []byte("tile")}},
	}))
	if r.Limits != l {
		t.Errorf("a new reader has limits %+v, want the defaults %+v", r.Limits, l)
	}
}

// TestReader_RefusesASectionThatDecompressesPastItsLimit is the decompression
// bomb.
//
// The archive states a section's COMPRESSED length and never its decompressed
// one, so nothing before the decompressor knows what a section is about to
// cost. gzip reaches about a thousand to one: eighty kilobytes of archive
// buys eighty megabytes of directory, and a directory is then multiplied again
// into entries. Every section goes through one function, so the ceiling goes
// there.
//
// The root directory is tested at the default limit because a caller cannot
// lower it before NewReader reads the root; the others are tested at a lowered
// one, which also shows the field doing what it is for.
func TestReader_RefusesASectionThatDecompressesPastItsLimit(t *testing.T) {
	t.Run("a root directory", func(t *testing.T) {
		// One byte past the default of eight megabytes.
		bomb := zeroBomb(t, 8<<20+1)
		if len(bomb) > pmtiles.MaxRootDirectory {
			t.Fatalf("the bomb compresses to %d bytes, past the format's root cap of %d, so ParseHeader would refuse it for the wrong reason", len(bomb), pmtiles.MaxRootDirectory)
		}
		archive := craftArchive(t, crafted{internal: pmtiles.CompressionGzip})
		// Replace the root directory with the bomb, keeping the header honest
		// about its compressed length.
		archive = append(archive[:pmtiles.HeaderSize:pmtiles.HeaderSize], bomb...)
		binary.LittleEndian.PutUint64(archive[16:], uint64(len(bomb)))

		_, err := pmtiles.NewReader(bytes.NewReader(archive))
		assertLimitError(t, err, "the root directory")
	})

	t.Run("a tile", func(t *testing.T) {
		bomb := zeroBomb(t, 4<<20)
		archive := craftArchive(t, crafted{
			tileComp: pmtiles.CompressionGzip,
			root:     []pmtiles.Entry{{TileID: 5, Offset: 0, Length: uint32(len(bomb)), RunLength: 1}},
			tileData: bomb,
		})
		r, err := pmtiles.NewReader(bytes.NewReader(archive))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		r.Limits.Tile = 1 << 20
		_, _, err = r.Tile(2, 0, 0)
		assertLimitError(t, err, "tile 2/0/0")

		// Raised deliberately, the same archive reads: the limit is a policy
		// the caller owns, not a refusal to work.
		r.Limits.Tile = 8 << 20
		data, ok, err := r.Tile(2, 0, 0)
		if err != nil || !ok {
			t.Fatalf("with the limit raised: ok %v, err %v", ok, err)
		}
		if len(data) != 4<<20 {
			t.Errorf("tile is %d bytes, want %d", len(data), 4<<20)
		}
	})

	t.Run("the metadata", func(t *testing.T) {
		bomb := zeroBomb(t, 4<<20)
		archive := craftArchive(t, crafted{
			internal: pmtiles.CompressionGzip,
			root:     []pmtiles.Entry{{TileID: 5, Offset: 0, Length: 1, RunLength: 1}},
			metadata: bomb,
			tileData: []byte("x"),
		})
		r, err := pmtiles.NewReader(bytes.NewReader(archive))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		r.Limits.Metadata = 1 << 20
		_, err = r.Metadata()
		assertLimitError(t, err, "the metadata")
	})

	t.Run("a leaf directory", func(t *testing.T) {
		bomb := zeroBomb(t, 4<<20)
		archive := craftArchive(t, crafted{
			internal:    pmtiles.CompressionGzip,
			root:        []pmtiles.Entry{{TileID: 0, Offset: 0, Length: uint32(len(bomb)), RunLength: 0}},
			leafSection: bomb,
		})
		r, err := pmtiles.NewReader(bytes.NewReader(archive))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		r.Limits.Directory = 1 << 20
		_, _, err = r.TileByID(5)
		assertLimitError(t, err, "a leaf directory")
	})
}

// TestReader_RefusesAnUncompressedSectionPastItsLimit. The limit is about how
// much memory a section may occupy, and enforcing it only on gzip would be a
// rule about gzip instead.
func TestReader_RefusesAnUncompressedSectionPastItsLimit(t *testing.T) {
	big := bytes.Repeat([]byte("t"), 4<<10)
	archive := craftArchive(t, crafted{
		root:     []pmtiles.Entry{{TileID: 5, Offset: 0, Length: uint32(len(big)), RunLength: 1}},
		tileData: big,
	})
	r, err := pmtiles.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	r.Limits.Tile = 1 << 10
	_, _, err = r.Tile(2, 0, 0)
	assertLimitError(t, err, "tile 2/0/0")
}

func assertLimitError(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s decompressed without hitting the limit", what)
	}
	if !strings.Contains(err.Error(), what) {
		t.Errorf("error was %q, and it should name %q", err, what)
	}
	if !strings.Contains(err.Error(), "Reader.Limits") {
		t.Errorf("error was %q, and it should say which knob raises the limit", err)
	}
}

// TestReader_RefusesAnEntryOutsideItsSection is the offset wrap.
//
// A directory entry's offset is relative to the tile data section, and the
// section base is added to it in uint64. With an offset near 2^64 that
// addition wraps back to the start of the file: measured before the fix, an
// entry with offset 2^64-141 and length 9 returned ok, no error, and the bytes
// "PMTiles\x03\x7f" -- the archive's own header, served as a tile.
//
// The header states where the tile data section starts AND how long it is, so
// the check is available and costs nothing. Both directions are covered: an
// offset outside a section that does not itself overflow, and one whose
// offset+length wraps.
func TestReader_RefusesAnEntryOutsideItsSection(t *testing.T) {
	cases := []struct {
		name    string
		entry   pmtiles.Entry
		patch   func([]byte)
		wantMsg string
	}{
		{
			name:    "an offset that wraps the section base back to the header",
			entry:   pmtiles.Entry{TileID: 5, Offset: 1<<64 - 141, Length: 9, RunLength: 1},
			wantMsg: "the tile data section",
		},
		{
			name:    "an offset and length that overflow each other",
			entry:   pmtiles.Entry{TileID: 5, Offset: 1<<64 - 5, Length: 10, RunLength: 1},
			wantMsg: "overflows",
		},
		{
			name:    "an entry one byte past the end of the section",
			entry:   pmtiles.Entry{TileID: 5, Offset: 1, Length: 4, RunLength: 1},
			wantMsg: "only 4 bytes long",
		},
		{
			name:  "an offset that wraps when the section claims the whole address space",
			entry: pmtiles.Entry{TileID: 5, Offset: 1<<64 - 8, Length: 4, RunLength: 1},
			// A tile data length of 2^64-1 is a lie no file can satisfy, and
			// it is the only way to reach the base+offset overflow: without
			// it the length check catches the entry first.
			patch:   func(h []byte) { binary.LittleEndian.PutUint64(h[64:], 1<<64-1) },
			wantMsg: "overflow",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			archive := craftArchive(t, crafted{
				root:     []pmtiles.Entry{c.entry},
				tileData: []byte("tile"),
				patch:    c.patch,
			})
			r, err := pmtiles.NewReader(bytes.NewReader(archive))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			data, ok, err := r.Tile(2, 0, 0)
			if err == nil {
				t.Fatalf("Tile returned ok=%v and %q for an entry outside the tile data section", ok, data)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("error was %q, and it should say %q", err, c.wantMsg)
			}
		})
	}
}

// TestReader_RefusesALeafOutsideItsSection is the same hole on the other
// section, which has its own base and its own length in the header.
func TestReader_RefusesALeafOutsideItsSection(t *testing.T) {
	archive := craftArchive(t, crafted{
		root:        []pmtiles.Entry{{TileID: 0, Offset: 1<<64 - 200, Length: 20, RunLength: 0}},
		leafSection: []byte("not a directory"),
		tileData:    []byte("tile"),
	})
	r, err := pmtiles.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, ok, err := r.TileByID(5); err == nil {
		t.Fatalf("TileByID returned ok=%v and no error for a leaf outside the leaf section", ok)
	} else if !strings.Contains(err.Error(), "leaf directory section") {
		t.Errorf("error was %q, and it should name the leaf directory section", err)
	}
}

// TestReader_TwoLeavesAtOneOffsetDoNotCollide.
//
// The leaf cache is keyed on the entry, and an entry is an offset AND a
// length. Keyed on the offset alone, two leaf entries naming one offset with
// different lengths collide, and whichever was read first is served for both.
//
// The archive below makes that observable. Its leaf section is one directory
// split across two concatenated gzip members: the first member alone
// decompresses to a TRUNCATED directory, which is not readable at all, and the
// two together decompress to a complete one serving tiles 10 to 14 and 20 to
// 24. The root has a leaf entry for each length, both at offset 0.
//
// So the long entry is legitimate and the short one is damaged. Reading the
// long one first fills the cache; the short one must then still be read on its
// own and still fail. Keyed on the offset alone it does not -- it hits the
// cache and serves tile 10 out of a directory its own bytes do not contain,
// which is one directory standing in for another.
//
// The query order is the point of the test and not an accident of it.
func TestReader_TwoLeavesAtOneOffsetDoNotCollide(t *testing.T) {
	full := osmbasetest.EncodeDirectory([]pmtiles.Entry{
		{TileID: 10, Offset: 0, Length: 5, RunLength: 5},
		{TileID: 20, Offset: 5, Length: 6, RunLength: 5},
	})
	head, tail := full[:3], full[3:]
	if _, err := pmtiles.DecodeDirectory(head); err == nil {
		t.Fatal("the truncated half decodes on its own, so this fixture proves nothing")
	}

	short := gzipBytes(t, head)
	section := append(append([]byte(nil), short...), gzipBytes(t, tail)...)

	archive := craftArchive(t, crafted{
		internal: pmtiles.CompressionGzip,
		root: []pmtiles.Entry{
			{TileID: 10, Offset: 0, Length: uint32(len(short)), RunLength: 0},
			{TileID: 20, Offset: 0, Length: uint32(len(section)), RunLength: 0},
		},
		leafSection: section,
		tileData:    []byte("AAAAABBBBBB"),
	})
	r, err := pmtiles.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	// The long, legitimate leaf first. This is what fills the cache at
	// offset 0.
	got, ok, err := r.TileByID(20)
	if err != nil || !ok {
		t.Fatalf("TileByID(20): ok %v, err %v", ok, err)
	}
	if string(got) != "BBBBBB" {
		t.Errorf("tile 20 = %q, want %q", got, "BBBBBB")
	}

	// The short leaf, at the same offset. Its own bytes are a truncated
	// directory, so this must fail.
	got, ok, err = r.TileByID(10)
	if err == nil {
		t.Fatalf("TileByID(10) returned ok=%v and %q; the cache served the other leaf at offset 0", ok, got)
	}
	if !strings.Contains(err.Error(), "leaf directory") {
		t.Errorf("error was %q, and it should say which directory could not be read", err)
	}
}

// TestReader_ReadsThroughMoreLeavesThanTheCacheHolds exercises the branch that
// drops the leaf cache when it is full, which nothing else reaches: every
// other fixture builds a handful of leaves and the cache holds sixty-four.
//
// Eight hundred tiles at twelve entries per directory gives sixty-seven leaves
// at the first level and six above them, seventy-three in all, so the cache
// fills and is cleared at least once. Every tile must still read back.
func TestReader_ReadsThroughMoreLeavesThanTheCacheHolds(t *testing.T) {
	const count = 800
	tiles := make([]osmbasetest.ArchiveTile, count)
	for i := range tiles {
		tiles[i] = osmbasetest.ArchiveTile{ID: uint64(i), Data: []byte(fmt.Sprintf("tile-%d", i))}
	}
	built := build(t, osmbasetest.Archive{Tiles: tiles, LeafSize: 12})
	if built.LeafDirectories <= 64 {
		t.Fatalf("the fixture has %d leaf directories, which does not fill a cache of 64", built.LeafDirectories)
	}
	r := reader(t, built)
	// Twice through, so the second pass reads leaves that the first pass
	// cached and then dropped.
	for pass := 0; pass < 2; pass++ {
		for i := 0; i < count; i++ {
			got, ok, err := r.TileByID(uint64(i))
			if err != nil || !ok {
				t.Fatalf("pass %d, tile %d: ok %v, err %v", pass, i, ok, err)
			}
			if want := fmt.Sprintf("tile-%d", i); string(got) != want {
				t.Fatalf("pass %d, tile %d = %q, want %q", pass, i, got, want)
			}
		}
	}
}
