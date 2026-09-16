package acquire

import (
	"fmt"
	"slices"

	"github.com/wisborg/osmbase/pmtiles"
)

// Coalescing is what turns a cell of eighty-five tiles into a handful of
// requests, and it is possible because of how the archive is ordered rather
// than because of anything clever here.
//
// PMTiles addresses tiles by Hilbert index, and the Hilbert curve is
// hierarchical: the 4^k descendants of one tile occupy a CONTIGUOUS run of
// indices k zooms deeper. So a cell's sub-pyramid is one unbroken run of tile
// IDs per zoom -- four runs for zoom 12 to 15 -- and in a clustered archive
// those runs are unbroken stretches of the file. What is left to do is to spot
// them and to decide when a gap between two of them is small enough to be
// worth swallowing.
//
// The alternative, one request per tile, is not merely slower. Eighty-five
// round trips per cell against a host this project has no agreement with is
// the traffic pattern most likely to look like abuse, and the architecture
// says so.

// Range is a contiguous stretch of the archive that one request will fetch.
type Range struct {
	Offset, Length int64
}

// End is the offset one byte past this range.
func (r Range) End() int64 { return r.Offset + r.Length }

// contains reports whether a location lies wholly inside this range.
func (r Range) contains(l pmtiles.Location) bool {
	return l.Offset >= r.Offset && l.End() <= r.End()
}

// Limits bound what coalescing may do.
//
// Both are ceilings on a single decision rather than on a whole fetch, because
// that is what makes the waste calculable in advance: the bytes nobody wants
// are at most MaxGap per join, and a plan reports the exact figure anyway.
type Limits struct {
	// MaxGap is how many unwanted bytes one join may swallow.
	//
	// Zero means DefaultMaxGap; a negative value means no joining at all,
	// which is how a test asks for the uncoalesced count.
	MaxGap int64

	// MaxRequest is the largest single request, and so the largest buffer a
	// fetch holds at once. Zero means DefaultMaxRequest.
	MaxRequest int64
}

// DefaultMaxGap is how much unwanted data one join will fetch.
//
// The trade is bytes against round trips. A range request to an archive on the
// other side of the world costs 100 to 300 ms of latency before a byte
// arrives; at a domestic 10 Mbit/s, 64 KiB is about 50 ms of transfer. So
// swallowing up to 64 KiB to avoid a request is a clear win on time, and it is
// small in absolute terms too: a dense vector tile is 10 to 200 KB, so the
// worst a join wastes is under one tile's worth.
//
// It is also the figure that makes the waste bound statable. The bytes fetched
// that nobody asked for are at most MaxGap times the number of joins, and a
// plan reports the exact total before anything is downloaded.
const DefaultMaxGap int64 = 64 << 10

// DefaultMaxRequest is the largest single range request.
//
// A coalesced range is read into memory whole, so this is the peak buffer a
// fetch holds. Eight mebibytes is comfortably above one zoom level of the
// densest measured cell -- the City of London is 8.9 MB across all four zooms
// of its pyramid -- so in practice nothing is split by it, and it is what
// stops a pathological archive turning one join into a gigabyte allocation.
//
// A single tile larger than this is still fetched in one request. Splitting a
// tile across two would mean reassembling it, and a tile that large is a fact
// about the archive rather than something to work around.
const DefaultMaxRequest int64 = 8 << 20

func (l Limits) maxGap() int64 {
	if l.MaxGap == 0 {
		return DefaultMaxGap
	}
	if l.MaxGap < 0 {
		return 0
	}
	return l.MaxGap
}

func (l Limits) maxRequest() int64 {
	if l.MaxRequest <= 0 {
		return DefaultMaxRequest
	}
	return l.MaxRequest
}

// coalesce turns the locations of the tiles a fetch wants into the ranges it
// will request.
//
// Locations are deduplicated first, and that is not an optimisation. The
// format serves a run of identical tiles -- eighty-five tiles of open water,
// say -- from ONE stored blob, so the same location arrives many times over,
// and a planner that did not fold them together would report a download many
// times larger than the one that is about to happen. The whole confirmation
// prompt rests on that number being right.
//
// The result is ascending by offset, non-overlapping, and deterministic for a
// given input: the sort is total and nothing here iterates a map.
func coalesce(locs []pmtiles.Location, lim Limits) ([]Range, error) {
	if len(locs) == 0 {
		return nil, nil
	}
	sorted := make([]pmtiles.Location, 0, len(locs))
	seen := make(map[pmtiles.Location]bool, len(locs))
	for _, l := range locs {
		if l.Length <= 0 || l.Offset < 0 || l.End() < l.Offset {
			return nil, fmt.Errorf("acquire: a tile at offset %d is %d bytes, which is not a stretch of an archive", l.Offset, l.Length)
		}
		if seen[l] {
			continue
		}
		seen[l] = true
		sorted = append(sorted, l)
	}
	slices.SortFunc(sorted, func(a, b pmtiles.Location) int {
		if a.Offset != b.Offset {
			return cmpInt64(a.Offset, b.Offset)
		}
		return cmpInt64(a.Length, b.Length)
	})

	gap, maxReq := lim.maxGap(), lim.maxRequest()
	out := []Range{{Offset: sorted[0].Offset, Length: sorted[0].Length}}
	for _, l := range sorted[1:] {
		cur := &out[len(out)-1]
		// A true OVERLAP is not a gap and is absorbed whatever the size. The
		// two share bytes, so they are one stretch of the file and there is no
		// version of this where they are separate requests -- and a range has
		// to hold every tile it serves whole, so splitting here would leave a
		// tile nothing could reassemble. Abutting ranges, where the gap is
		// exactly zero, are the common case in a clustered archive and go
		// through the size check below instead; absorbing those unconditionally
		// would turn a contiguous run of half a gigabyte into one request.
		if l.Offset < cur.End() {
			if end := l.End(); end > cur.End() {
				cur.Length = end - cur.Offset
			}
			continue
		}
		waste := l.Offset - cur.End()
		grown := l.End() - cur.Offset
		if waste <= gap && grown <= maxReq {
			cur.Length = grown
			continue
		}
		out = append(out, Range{Offset: l.Offset, Length: l.Length})
	}
	return out, nil
}

// usefulBytes is how much of a set of ranges is tiles the fetch actually
// wants, counting each stored blob once.
//
// It is the figure the waste bound is measured against: what comes over the
// wire minus this is exactly the data nobody asked for.
func usefulBytes(locs []pmtiles.Location) int64 {
	seen := make(map[pmtiles.Location]bool, len(locs))
	var total int64
	for _, l := range locs {
		if seen[l] {
			continue
		}
		seen[l] = true
		total += l.Length
	}
	return total
}

func totalBytes(ranges []Range) int64 {
	var total int64
	for _, r := range ranges {
		total += r.Length
	}
	return total
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
