package pmtiles

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
)

// Entry is one directory entry. It describes either a run of tiles in the tile
// data section or a leaf directory in the leaf directory section, and which of
// the two is decided by RunLength alone.
type Entry struct {
	// TileID is the tile this entry addresses, or for a leaf, the first tile
	// the leaf covers.
	TileID uint64
	// Offset is relative to the start of the tile data section for a tile
	// entry, and to the start of the leaf directory section for a leaf.
	Offset uint64
	// Length is the stored -- that is, still compressed -- length, and is
	// always greater than zero.
	Length uint32
	// RunLength is the number of consecutive tile IDs this entry serves. Zero
	// means the entry points at a leaf directory rather than at a tile, and is
	// the only marker for that; there is no separate type byte.
	RunLength uint32
}

// IsLeaf reports whether this entry points at another directory.
func (e Entry) IsLeaf() bool { return e.RunLength == 0 }

// Covers reports whether this entry serves the given tile ID directly.
//
// A leaf entry never does, and that is not an oversight: it points at another
// directory, and how far that directory reaches is decided by where the NEXT
// entry of this one starts, which a single entry cannot know.
func (e Entry) Covers(id uint64) bool {
	return !e.IsLeaf() && id >= e.TileID && id-e.TileID < uint64(e.RunLength)
}

// DecodeDirectory decodes a directory from its DECOMPRESSED bytes.
//
// The encoding is five runs of varints rather than five fields per entry: the
// count, then every tile ID, then every run length, then every length, then
// every offset. Tile IDs are deltas from the previous entry and offsets are
// stored as offset+1 so that zero can mean "immediately after the previous
// entry", which is the common case in a clustered archive and costs one byte
// instead of five.
//
// Everything this rejects is something that would otherwise decode into a
// plausible directory and serve the wrong bytes:
//
//   - a zero offset on the FIRST entry, which the spec's own decoder computes
//     as 0-1 and which wraps to an offset near 2^64;
//   - a tile ID delta of zero, which puts two entries at one ID and makes the
//     binary search below pick between them arbitrarily;
//   - a length of zero, which the spec forbids and which would read no bytes
//     and report a tile that is not there as present but empty.
func DecodeDirectory(b []byte) ([]Entry, error) {
	d := decoder{b: b}
	n, err := d.uvarint("entry count")
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("pmtiles: directory claims no entries, and a directory has at least one")
	}
	// Four varints of at least one byte each are needed per entry, so a count
	// larger than a quarter of what is left cannot be honest. Without this, a
	// corrupt varint claiming 2^60 entries is an allocation of 32 exabytes
	// before the first byte of it is read.
	if remaining := uint64(len(b) - d.i); n > remaining/4 {
		return nil, fmt.Errorf("pmtiles: directory claims %d entries but only %d bytes follow, too few to hold them", n, remaining)
	}

	entries := make([]Entry, n)
	var id uint64
	for i := range entries {
		delta, err := d.uvarint("tile ID delta")
		if err != nil {
			return nil, err
		}
		if i > 0 && delta == 0 {
			return nil, fmt.Errorf("pmtiles: directory entry %d repeats tile ID %d; entries must ascend", i, id)
		}
		if delta > math.MaxUint64-id {
			return nil, fmt.Errorf("pmtiles: directory entry %d has a tile ID delta of %d, which overflows past tile ID %d", i, delta, id)
		}
		id += delta
		entries[i].TileID = id
	}
	for i := range entries {
		v, err := d.uvarint("run length")
		if err != nil {
			return nil, err
		}
		if v > math.MaxUint32 {
			return nil, fmt.Errorf("pmtiles: directory entry %d has run length %d, beyond anything a zoom can hold", i, v)
		}
		entries[i].RunLength = uint32(v)
	}
	for i := range entries {
		v, err := d.uvarint("length")
		if err != nil {
			return nil, err
		}
		if v == 0 {
			return nil, fmt.Errorf("pmtiles: directory entry %d (tile ID %d) has length 0, and every entry must have some bytes", i, entries[i].TileID)
		}
		if v > math.MaxUint32 {
			return nil, fmt.Errorf("pmtiles: directory entry %d (tile ID %d) is %d bytes, larger than any tile or directory", i, entries[i].TileID, v)
		}
		entries[i].Length = uint32(v)
	}
	for i := range entries {
		v, err := d.uvarint("offset")
		if err != nil {
			return nil, err
		}
		if v == 0 {
			if i == 0 {
				return nil, fmt.Errorf("pmtiles: the first directory entry has offset 0, which encodes \"directly after the previous entry\" and there is no previous entry")
			}
			prev := entries[i-1]
			// The contiguous encoding is the previous entry's end, and that
			// addition can wrap for a directory carrying a huge offset. A
			// wrapped offset is a small number that addresses a real part of
			// the archive, so it is refused rather than computed.
			end := prev.Offset + uint64(prev.Length)
			if end < prev.Offset {
				return nil, fmt.Errorf("pmtiles: directory entry %d follows an entry of %d bytes at offset %d, and the two overflow", i, prev.Length, prev.Offset)
			}
			entries[i].Offset = end
			continue
		}
		entries[i].Offset = v - 1
	}
	return entries, nil
}

// find returns the entry that serves id, and whether there is one.
//
// The entries ascend by tile ID, so the candidate is the last entry at or
// before id: either it is a tile entry whose run reaches id, or it is a leaf
// whose range id falls in, or id is in a gap and the archive simply does not
// hold that tile.
func find(entries []Entry, id uint64) (Entry, bool) {
	i, exact := slices.BinarySearchFunc(entries, id, func(e Entry, id uint64) int {
		return cmp.Compare(e.TileID, id)
	})
	if !exact {
		// i is where id WOULD go, so the entry before it is the last one at
		// or before id. A zero i means id precedes every entry.
		if i == 0 {
			return Entry{}, false
		}
		i--
	}
	e := entries[i]
	if e.IsLeaf() || e.Covers(id) {
		return e, true
	}
	return Entry{}, false
}

// decoder reads the varint stream of a directory, keeping enough context to
// say which field ran off the end.
type decoder struct {
	b []byte
	i int
}

func (d *decoder) uvarint(what string) (uint64, error) {
	v, n := binary.Uvarint(d.b[d.i:])
	switch {
	case n == 0:
		return 0, fmt.Errorf("pmtiles: directory ends part way through a %s at byte %d of %d", what, d.i, len(d.b))
	case n < 0:
		return 0, fmt.Errorf("pmtiles: directory has a %s at byte %d that does not fit in 64 bits", what, d.i)
	}
	d.i += n
	return v, nil
}
