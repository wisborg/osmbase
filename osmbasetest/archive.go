// Package osmbasetest builds synthetic PMTiles archives and vector tiles for
// tests.
//
// It exists so that nothing in this module's test suite needs a real tile
// archive. A downloaded archive is tens of megabytes of somebody else's build,
// unreviewable in a diff, wrong to commit to a public repository, and absent
// on a fresh clone -- which is how a test ends up skipping silently and
// asserting nothing. The builders here are the whole content of every fixture,
// in the open, and they cost nothing to re-run with a different shape.
//
// The archive builder is deliberately an INDEPENDENT implementation of the
// PMTiles encoding rather than the reader run backwards. A fixture generated
// by inverting the code under test proves only that the code is
// self-consistent, and the failure that matters here -- a directory or a
// Hilbert conversion that is wrong in the same way at both ends -- is exactly
// the one such a fixture cannot see. The encoder below is written from the
// specification's own pseudocode.
//
// Everything it produces is deterministic: the same spec yields byte-identical
// output, so a test can compare bytes and a failure is reproducible.
package osmbasetest

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/wisborg/osmbase/pmtiles"
)

// ArchiveTile is one tile to place in a synthetic archive. It is named apart
// from the vector tile the other half of this package builds, because a
// fixture routinely uses both and "tile" alone would mean two things in one
// test.
type ArchiveTile struct {
	// ID is the PMTiles tile ID. Tiles are addressed by ID rather than by
	// z/x/y so that a fixture can place an entry anywhere without the test
	// having to agree with the Hilbert conversion it is there to check.
	ID uint64
	// Run is how many consecutive tile IDs this one entry serves, which is the
	// format's run-length encoding. 0 and 1 both mean one tile.
	Run int
	// Data is the tile's content BEFORE compression. The builder applies
	// Archive.TileCompression to it.
	Data []byte
}

// Archive describes a synthetic PMTiles archive.
//
// The zero value plus a tile or two is a valid leafless, uncompressed,
// clustered archive, which is the shape most tests want.
type Archive struct {
	// Tiles need not be sorted; the builder sorts them. Their runs must not
	// overlap.
	Tiles []ArchiveTile

	// InternalCompression covers the root directory, the metadata and every
	// leaf directory; TileCompression covers the tile blobs. The zero value of
	// each is pmtiles.CompressionUnknown, which the builder writes as
	// CompressionNone -- a fixture that declared "unknown" would be one no
	// reader could open, which is never what a test that did not set the field
	// meant.
	InternalCompression pmtiles.Compression
	TileCompression     pmtiles.Compression

	// LeafSize is the maximum number of entries in any one directory. 0 builds
	// a leafless archive whose root holds every entry. A value smaller than
	// the entry count builds leaves, and one smaller than the number of LEAVES
	// builds another level above them, which is how a nested-leaf fixture is
	// asked for: eight entries at LeafSize 2 gives four leaves, two
	// second-level leaves and a root of two.
	LeafSize int

	// Unclustered writes the tile blobs in descending ID order instead of
	// ascending, and clears the header's clustered flag. It exists to exercise
	// the offset encoding: a clustered archive stores almost every offset as
	// the one-byte "directly after the previous entry", and an unclustered one
	// stores every offset explicitly.
	Unclustered bool

	// Metadata is the JSON metadata section. nil writes an empty object.
	Metadata []byte

	TileType         pmtiles.TileType
	MinZoom, MaxZoom uint8
	MinLon, MinLat   float64
	MaxLon, MaxLat   float64
	CenterZoom       uint8
	CenterLon        float64
	CenterLat        float64
}

// BuiltArchive is an archive's bytes together with the shape they came out in.
//
// The shape is reported because a test that means to exercise nested leaves
// has no other way to know it did. Ask for LeafSize 2 with three tiles and the
// archive is one leaf level, not two, and a test asserting only that the tiles
// read back would pass while covering nothing it set out to cover.
type BuiltArchive struct {
	Bytes []byte
	// LeafLevels is 0 for a leafless archive, 1 when the root points at leaves
	// holding tiles, 2 when the root points at leaves holding leaves, and so
	// on.
	LeafLevels int
	// RootEntries is how many entries the root directory ended up with.
	RootEntries int
	// LeafDirectories is how many leaf directories were written, across every
	// level. A test about the leaf cache needs to know it built more leaves
	// than the cache holds, and there is no other way to tell.
	LeafDirectories int
	// TileDataOffset is where the tile blobs begin, for a test that wants to
	// corrupt them.
	TileDataOffset uint64
}

// BuildArchive encodes a whole PMTiles v3 archive.
func BuildArchive(a Archive) (BuiltArchive, error) {
	internal := orNone(a.InternalCompression)
	tileComp := orNone(a.TileCompression)

	tiles := append([]ArchiveTile(nil), a.Tiles...)
	slices.SortFunc(tiles, func(a, b ArchiveTile) int { return cmp.Compare(a.ID, b.ID) })
	var addressed uint64
	for i, t := range tiles {
		run := t.Run
		if run == 0 {
			run = 1
		}
		if run < 0 {
			return BuiltArchive{}, fmt.Errorf("osmbasetest: tile ID %d has run length %d", t.ID, run)
		}
		if len(t.Data) == 0 {
			return BuiltArchive{}, fmt.Errorf("osmbasetest: tile ID %d has no data, and the format requires every entry to have some bytes", t.ID)
		}
		if i > 0 {
			prev := tiles[i-1]
			prevRun := uint64(max(prev.Run, 1))
			if t.ID < prev.ID+prevRun {
				return BuiltArchive{}, fmt.Errorf("osmbasetest: tile ID %d falls inside the run of %d tiles starting at ID %d", t.ID, prevRun, prev.ID)
			}
		}
		addressed += uint64(run)
	}

	// Compress every blob, then lay them out. Clustered means ascending ID
	// order and therefore contiguous offsets; unclustered walks the same
	// blobs backwards so that no entry's offset is the previous one's end.
	blobs := make([][]byte, len(tiles))
	for i, t := range tiles {
		b, err := compressBytes(tileComp, t.Data)
		if err != nil {
			return BuiltArchive{}, fmt.Errorf("osmbasetest: compressing tile ID %d: %w", t.ID, err)
		}
		blobs[i] = b
	}
	order := make([]int, len(tiles))
	for i := range order {
		order[i] = i
	}
	if a.Unclustered {
		for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
	}
	var tileData bytes.Buffer
	offsets := make([]uint64, len(tiles))
	for _, i := range order {
		offsets[i] = uint64(tileData.Len())
		tileData.Write(blobs[i])
	}

	entries := make([]pmtiles.Entry, len(tiles))
	for i, t := range tiles {
		entries[i] = pmtiles.Entry{
			TileID:    t.ID,
			Offset:    offsets[i],
			Length:    uint32(len(blobs[i])),
			RunLength: uint32(max(t.Run, 1)),
		}
	}

	root, leafSection, shape, err := buildDirectories(entries, a.LeafSize, internal)
	if err != nil {
		return BuiltArchive{}, err
	}

	metadata := a.Metadata
	if metadata == nil {
		metadata = []byte("{}")
	}
	metaBlob, err := compressBytes(internal, metadata)
	if err != nil {
		return BuiltArchive{}, fmt.Errorf("osmbasetest: compressing the metadata: %w", err)
	}

	// Sections in the order the format lays them out.
	rootOffset := uint64(pmtiles.HeaderSize)
	metaOffset := rootOffset + uint64(len(root))
	leafOffset := metaOffset + uint64(len(metaBlob))
	dataOffset := leafOffset + uint64(len(leafSection))

	h := make([]byte, pmtiles.HeaderSize)
	copy(h, "PMTiles")
	h[7] = 3
	put64 := func(off int, v uint64) { binary.LittleEndian.PutUint64(h[off:], v) }
	put64(8, rootOffset)
	put64(16, uint64(len(root)))
	put64(24, metaOffset)
	put64(32, uint64(len(metaBlob)))
	put64(40, leafOffset)
	put64(48, uint64(len(leafSection)))
	put64(56, dataOffset)
	put64(64, uint64(tileData.Len()))
	put64(72, addressed)
	put64(80, uint64(len(entries)))
	put64(88, uint64(len(entries)))
	if !a.Unclustered {
		h[96] = 1
	}
	h[97] = byte(internal)
	h[98] = byte(tileComp)
	h[99] = byte(a.TileType)
	h[100] = a.MinZoom
	h[101] = a.MaxZoom
	putPosition(h[102:110], a.MinLon, a.MinLat)
	putPosition(h[110:118], a.MaxLon, a.MaxLat)
	h[118] = a.CenterZoom
	putPosition(h[119:127], a.CenterLon, a.CenterLat)

	var out bytes.Buffer
	out.Write(h)
	out.Write(root)
	out.Write(metaBlob)
	out.Write(leafSection)
	out.Write(tileData.Bytes())

	return BuiltArchive{
		Bytes:           out.Bytes(),
		LeafLevels:      shape.levels,
		RootEntries:     shape.rootEntries,
		LeafDirectories: shape.leaves,
		TileDataOffset:  dataOffset,
	}, nil
}

// buildDirectories splits entries into a root directory and, if asked, one or
// more levels of leaf directories.
//
// Levels are built from the bottom up: the entries are cut into directories of
// at most leafSize, each of which becomes one leaf-pointing entry in the level
// above, and the process repeats until a level fits in a single directory.
// That is what a real writer does, and it is the only way to get a nested-leaf
// fixture without hand-placing every offset.
func buildDirectories(entries []pmtiles.Entry, leafSize int, internal pmtiles.Compression) (root, leafSection []byte, shape directoryShape, err error) {
	if leafSize < 0 {
		return nil, nil, shape, fmt.Errorf("osmbasetest: LeafSize %d is negative", leafSize)
	}
	level := entries
	var leaves bytes.Buffer
	if leafSize > 0 {
		for len(level) > leafSize {
			var next []pmtiles.Entry
			for start := 0; start < len(level); start += leafSize {
				end := min(start+leafSize, len(level))
				chunk := level[start:end]
				blob, err := compressBytes(internal, EncodeDirectory(chunk))
				if err != nil {
					return nil, nil, shape, fmt.Errorf("osmbasetest: compressing a leaf directory: %w", err)
				}
				next = append(next, pmtiles.Entry{
					TileID:    chunk[0].TileID,
					Offset:    uint64(leaves.Len()),
					Length:    uint32(len(blob)),
					RunLength: 0,
				})
				leaves.Write(blob)
				shape.leaves++
			}
			level = next
			shape.levels++
		}
	}
	root, err = compressBytes(internal, EncodeDirectory(level))
	if err != nil {
		return nil, nil, shape, fmt.Errorf("osmbasetest: compressing the root directory: %w", err)
	}
	if len(root) > pmtiles.MaxRootDirectory {
		return nil, nil, shape, fmt.Errorf("osmbasetest: the root directory compresses to %d bytes and the format caps it at %d; use a smaller LeafSize", len(root), pmtiles.MaxRootDirectory)
	}
	shape.rootEntries = len(level)
	return root, leaves.Bytes(), shape, nil
}

// directoryShape is what buildDirectories produced, reported back so a test
// can assert it got the archive it asked for.
type directoryShape struct {
	levels      int
	leaves      int
	rootEntries int
}

// EncodeDirectory writes a directory in the format's five runs: the entry
// count, then every tile ID as a delta, every run length, every length, and
// every offset as offset+1 or as 0 when the entry sits immediately after the
// previous one.
//
// The specification's own encoder pseudocode (appendix A.1) omits the entry
// count its decoder then reads; section 4.2 lists it as the first of the five
// parts. The prose is right and the pseudocode is incomplete, so the count is
// written here.
// It is exported so a test can build a directory by hand -- deliberately
// damaged, or with a shape BuildArchive would never produce.
func EncodeDirectory(entries []pmtiles.Entry) []byte {
	var buf []byte
	buf = binary.AppendUvarint(buf, uint64(len(entries)))
	var last uint64
	for _, e := range entries {
		buf = binary.AppendUvarint(buf, e.TileID-last)
		last = e.TileID
	}
	for _, e := range entries {
		buf = binary.AppendUvarint(buf, uint64(e.RunLength))
	}
	for _, e := range entries {
		buf = binary.AppendUvarint(buf, uint64(e.Length))
	}
	var next uint64
	for i, e := range entries {
		if i > 0 && e.Offset == next {
			buf = binary.AppendUvarint(buf, 0)
		} else {
			buf = binary.AppendUvarint(buf, e.Offset+1)
		}
		next = e.Offset + uint64(e.Length)
	}
	return buf
}

// compressBytes applies one of the two compressions this module implements.
//
// gzip output is deterministic here because the writer's header carries no
// modification time unless one is set, which is what lets a test compare two
// archives byte for byte.
func compressBytes(c pmtiles.Compression, b []byte) ([]byte, error) {
	switch c {
	case pmtiles.CompressionNone:
		return b, nil
	case pmtiles.CompressionGzip:
		var out bytes.Buffer
		zw := gzip.NewWriter(&out)
		if _, err := zw.Write(b); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
	return nil, fmt.Errorf("osmbasetest: cannot write %s, which this module does not implement", c)
}

// orNone turns the zero value into CompressionNone. See Archive.
func orNone(c pmtiles.Compression) pmtiles.Compression {
	if c == pmtiles.CompressionUnknown {
		return pmtiles.CompressionNone
	}
	return c
}

// putPosition writes the longitude/latitude pair the header packs into eight
// bytes as hundred-nanodegree signed integers, longitude first.
func putPosition(b []byte, lon, lat float64) {
	binary.LittleEndian.PutUint32(b[0:4], uint32(int32(lon*1e7)))
	binary.LittleEndian.PutUint32(b[4:8], uint32(int32(lat*1e7)))
}
