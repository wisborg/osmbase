package pmtiles_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/pmtiles"
)

// The fuzz targets in this file are checked in deliberately.
//
// An archive read here is a file somebody downloaded, and the parts of it that
// steer this code -- lengths, offsets, entry counts, compressed sizes -- are
// all written by whoever produced it. Running a fuzzer once and reporting that
// it found nothing is a statement about an afternoon; a target in the tree is
// a statement that keeps being true, and `go test` alone re-runs the seed
// corpus and every input a past run saved into testdata.
//
// Each target asserts more than "did not crash". A crash is the easy failure;
// the ones that matter here were a decoder that returned plausible bytes for
// input it had misread, and an input a thousandth the size of the memory it
// cost. So the targets assert the invariants a successful decode must satisfy,
// and a ceiling on time -- because the two real defects found by review were a
// decompression bomb and a quadratic loop, and a bare no-crash target would
// have found neither.

// timeCeiling is how long a decode of n bytes may take before it counts as a
// failure.
//
// It is deliberately loose: a fuzz worker shares the machine, and the point is
// to catch work that grows faster than its input rather than to benchmark. The
// slope is two microseconds per byte, which is several hundred times what a
// linear decode of real data costs and still an order of magnitude under the
// quadratic layer scan this repository actually had -- that one took 1.19s on
// 320 KB, against a ceiling here of 0.89s.
func timeCeiling(n int) time.Duration {
	return 250*time.Millisecond + time.Duration(n)*2*time.Microsecond
}

// FuzzParseHeader. The header is 127 bytes that say where everything else is,
// so a value it accepts is a value the rest of the reader will act on.
func FuzzParseHeader(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, pmtiles.HeaderSize))
	f.Add(seedArchive(f))
	f.Add(seedArchive(f)[:pmtiles.HeaderSize])

	f.Fuzz(func(t *testing.T, data []byte) {
		h, err := pmtiles.ParseHeader(data)
		if err != nil {
			return
		}
		// Everything ParseHeader promises its caller, restated. A header that
		// got past it with any of these false would send a reader to an
		// arbitrary offset with an arbitrary length.
		if h.Version != pmtiles.Version {
			t.Fatalf("accepted version %d", h.Version)
		}
		if h.RootLength == 0 || h.RootLength > pmtiles.MaxRootDirectory {
			t.Fatalf("accepted a root directory of %d bytes", h.RootLength)
		}
		if h.MaxZoom < h.MinZoom || h.MaxZoom > pmtiles.MaxZoom {
			t.Fatalf("accepted zooms %d..%d", h.MinZoom, h.MaxZoom)
		}
		if h.InternalCompression > pmtiles.CompressionZstd || h.TileCompression > pmtiles.CompressionZstd {
			t.Fatalf("accepted compressions %d and %d", h.InternalCompression, h.TileCompression)
		}
	})
}

// FuzzDecodeDirectory. A directory is five runs of varints that the reader
// then treats as file offsets, and its entry count is itself a varint out of
// the same untrusted bytes.
func FuzzDecodeDirectory(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x01, 0x05, 0x01, 0x01, 0x01})
	f.Add(osmbasetest.EncodeDirectory([]pmtiles.Entry{
		{TileID: 5, Offset: 0, Length: 10, RunLength: 1},
		{TileID: 6, Offset: 10, Length: 20, RunLength: 3},
		{TileID: 100, Offset: 0, Length: 7, RunLength: 0},
	}))
	f.Add([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x10, 1, 1, 1, 1})

	f.Fuzz(func(t *testing.T, data []byte) {
		start := time.Now()
		entries, err := pmtiles.DecodeDirectory(data)
		if elapsed := time.Since(start); elapsed > timeCeiling(len(data)) {
			t.Fatalf("decoding %d bytes took %v, past the ceiling of %v", len(data), elapsed, timeCeiling(len(data)))
		}
		if err != nil {
			return
		}
		// Four varints of at least one byte each per entry, so this is the
		// most any honest directory can hold. It is the amplification bound:
		// an Entry is 24 bytes, so a count that outran it would turn a small
		// buffer into a large allocation.
		if max := len(data) / 4; len(entries) > max {
			t.Fatalf("%d bytes decoded to %d entries, more than the %d they could hold", len(data), len(entries), max)
		}
		var last uint64
		for i, e := range entries {
			if e.Length == 0 {
				t.Fatalf("entry %d has length 0", i)
			}
			if i > 0 && e.TileID <= last {
				t.Fatalf("entry %d has tile ID %d after %d; entries must ascend, or the binary search over them is meaningless", i, e.TileID, last)
			}
			if e.Offset+uint64(e.Length) < e.Offset {
				t.Fatalf("entry %d has offset %d and length %d, which overflow", i, e.Offset, e.Length)
			}
			last = e.TileID
		}
	})
}

// FuzzReadArchive drives a whole archive through the reader: header, root,
// leaves, tiles.
//
// This is the target that covers the two things the pieces cannot. The limits
// are lowered so a bomb is caught in milliseconds rather than by allocating
// the default sixteen megabytes, and every returned tile is measured against
// them -- which is the regression test for a reader that had no ceiling at all
// and turned an 81 KB archive into 653 MiB of allocation.
func FuzzReadArchive(f *testing.F) {
	f.Add(seedArchive(f))
	f.Add(seedLeafArchive(f))
	f.Add(make([]byte, pmtiles.HeaderSize))
	f.Add([]byte{})

	const limit = 1 << 16

	f.Fuzz(func(t *testing.T, data []byte) {
		start := time.Now()
		r, err := pmtiles.NewReader(bytes.NewReader(data))
		if err != nil {
			return
		}
		r.Limits = pmtiles.Limits{Directory: limit, Metadata: limit, Tile: limit}

		if meta, err := r.Metadata(); err == nil && int64(len(meta)) > limit {
			t.Fatalf("metadata came back %d bytes against a limit of %d", len(meta), limit)
		}
		for _, id := range []uint64{0, 1, 5, 6, 100, 19078479} {
			tile, ok, err := r.TileByID(id)
			if err != nil {
				continue
			}
			if ok && len(tile) == 0 {
				t.Fatalf("tile %d reported present with no bytes", id)
			}
			if int64(len(tile)) > limit {
				t.Fatalf("tile %d came back %d bytes against a limit of %d", id, len(tile), limit)
			}
		}
		if elapsed := time.Since(start); elapsed > timeCeiling(len(data)) {
			t.Fatalf("reading %d bytes took %v, past the ceiling of %v", len(data), elapsed, timeCeiling(len(data)))
		}
	})
}

func seedArchive(f *testing.F) []byte {
	f.Helper()
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles: []osmbasetest.ArchiveTile{
			{ID: 0, Data: []byte("zero")},
			{ID: 5, Run: 3, Data: []byte("five")},
			{ID: 19078479, Data: []byte("deep")},
		},
		TileType: pmtiles.TileTypeMVT,
		Metadata: []byte(`{"name":"seed"}`),
	})
	if err != nil {
		f.Fatalf("building the seed archive: %v", err)
	}
	return built.Bytes
}

func seedLeafArchive(f *testing.F) []byte {
	f.Helper()
	tiles := make([]osmbasetest.ArchiveTile, 16)
	for i := range tiles {
		tiles[i] = osmbasetest.ArchiveTile{ID: uint64(i) * 3, Data: []byte{byte(i)}}
	}
	built, err := osmbasetest.BuildArchive(osmbasetest.Archive{
		Tiles:               tiles,
		LeafSize:            2,
		InternalCompression: pmtiles.CompressionGzip,
		TileCompression:     pmtiles.CompressionGzip,
	})
	if err != nil {
		f.Fatalf("building the seed archive: %v", err)
	}
	return built.Bytes
}
