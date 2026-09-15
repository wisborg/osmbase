package slice

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

// Archive is where a fill takes tiles from, and is the whole of what this
// package asks of one.
//
// One method, because there is only one question: have you got the tile at
// z/x/y, and what are its stored bytes. Everything else an archive knows --
// its directories, its header, whether it is a local file or a hundred
// gigabytes behind HTTP range requests -- is the archive's business and none
// of a store's.
//
// # Why the interface is declared here
//
// It is defined by the consumer rather than exported by an implementation, so
// the dependency points from the archive reader to nothing at all and this
// package imports no format. A store that knew about PMTiles would be a store
// that could only ever be filled from PMTiles, and the design is explicit that
// an archive the user already has, obtained however they like, is a
// first-class source rather than an escape hatch.
//
// *pmtiles.Reader satisfies this as it stands, with no adapter, exactly as it
// satisfies the renderer's TileSource. That is the same coincidence in both
// places and it is worth noticing rather than arranging: the shape came from
// the question.
//
// # The contract
//
// data is the tile's bytes AS THE ARCHIVE STORES THEM, still compressed. Not
// decompressed. The store writes them down unchanged, so that the cache stays
// small, so that decompression happens once per render instead of once per
// download, and -- the part that matters most -- so that a compression neither
// end can decode is a refusal at AddSource rather than a silent re-encoding.
//
// ok reports whether the archive holds that tile, and it is not the same
// question as err. An archive legitimately has no tile at most coordinates,
// and a fill that treated that as a failure could not fetch a coastal cell.
//
// There is no context on the method. Cancellation is checked by Fill between
// tiles, because that is where a slow archive can be interrupted without
// leaving half a file.
type Archive interface {
	RawTile(z uint8, x, y uint32) (data []byte, ok bool, err error)
}

// Fill is what one call to Fill or FillOverview did.
type Fill struct {
	// Written is tiles fetched and stored, Skipped tiles already on disk, and
	// Absent tiles the archive does not hold.
	//
	// Absent is not an error and not a gap. Most coordinates in most archives
	// have no tile -- ocean, or outside the build's bounds -- and a cell over
	// open water is a handful of kilobytes for exactly that reason.
	Written, Skipped, Absent int

	// Bytes is what this call wrote, which for a resumed fetch is less than
	// the cell occupies.
	Bytes int64
}

// Fill fetches one cell's sub-pyramid from an archive and writes it into the
// store, finishing with cell.json.
//
// # Resuming
//
// A tile already on disk is skipped rather than re-fetched, so running this
// again after an interrupted fetch completes the cell instead of repeating it.
// A tile the archive does not hold leaves no file and is asked for again on
// the next run; that is a few requests against a coastal cell, and the
// alternative -- a marker file per absent tile -- would store thousands of
// empty files to save them.
//
// # Why cell.json is last
//
// Every tile is written under a temporary name and renamed, so no tile file is
// ever half there. cell.json is written after all of them, so a fetch killed
// part way through leaves a directory of real tiles and no cell.json, and that
// is read back as an incomplete cell. Writing it first, or updating it as the
// fetch went, would leave a cell that claims to be finished and has holes in
// it -- and a hole in a cell is not visible until somebody renders that
// square of the world.
func (s *Source) Fill(ctx context.Context, a Archive, c Cell, z ZoomRange) (Fill, error) {
	if a == nil {
		return Fill{}, fmt.Errorf("slice: filling cell %s of source %s: no archive to read from", c, s.id)
	}
	if err := z.validate(); err != nil {
		return Fill{}, err
	}
	cellZoom := s.store.cellZoom
	if z.Max < cellZoom {
		return Fill{}, fmt.Errorf("slice: zoom range %s is entirely above this store's cell zoom %d, so none of it belongs to a cell; those tiles are the overview, and FillOverview writes them", z, cellZoom)
	}
	refs, err := PyramidTiles(c, cellZoom, z)
	if err != nil {
		return Fill{}, err
	}

	var f Fill
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return f, fmt.Errorf("slice: filling cell %s of source %s at zooms %s: %w", c, s.id, z, err)
		}
		n, got, err := s.store1(a, ref)
		if err != nil {
			return f, err
		}
		switch {
		case !got:
			f.Absent++
		case n < 0:
			f.Skipped++
		default:
			f.Written++
			f.Bytes += n
		}
	}

	// Measured rather than accumulated, so that a resumed cell records what is
	// in the directory and not what this run happened to add.
	bytes, err := dirBytes(s.cellDir(c))
	if err != nil {
		return f, err
	}

	// A cell refilled at a different depth keeps the deeper answer: a cell
	// taken to zoom 15 last week and topped up to 13 today still holds 15, and
	// recording only what this call covered would lose that. The previous
	// cell.json is read directly rather than through Cell, because Cell
	// answers "is there a complete cell here" and a file that is simply absent
	// would come back as a zero CellInfo whose zoom range reads as "zoom 0".
	held := z
	var prev CellInfo
	if err := readJSON(filepath.Join(s.cellDir(c), cellFileName), &prev); err == nil && !prev.Zoom.empty() {
		held = held.union(prev.Zoom)
	}
	now := time.Now().UTC()
	ci := CellInfo{
		Zoom:     held,
		Tiles:    f.Written + f.Skipped,
		Bytes:    bytes,
		Fetched:  now,
		LastUsed: now,
		Complete: true,
	}
	if err := writeJSON(filepath.Join(s.cellDir(c), cellFileName), ci); err != nil {
		return f, err
	}
	if err := s.widen(z); err != nil {
		return f, err
	}
	return f, nil
}

// FillOverview writes shallow tiles: the ones above the cell zoom, stored once
// per source and shared between every cell beneath them.
//
// These are the twelve files a default store keeps per source, and they are
// never evicted. They are what makes a partially evicted area degrade into a
// coarser map instead of vanishing, and for a slice held entirely above the
// cell zoom -- the whole world at zoom 0 to 5, 1,365 tiles and 19.5 MB -- they
// are the whole slice.
//
// There is no cell.json for them and so no notion of a complete overview. The
// set a caller wants is the set it asks for: AncestorTiles for the shallow
// chain above a cell, WorldTiles for a global one.
func (s *Source) FillOverview(ctx context.Context, a Archive, refs []TileRef) (Fill, error) {
	if a == nil {
		return Fill{}, fmt.Errorf("slice: filling the overview of source %s: no archive to read from", s.id)
	}
	cellZoom := s.store.cellZoom
	held := emptyZoom
	var f Fill
	for _, ref := range refs {
		if ref.Z >= cellZoom {
			return f, fmt.Errorf("slice: tile %s is at or below this store's cell zoom %d, so it belongs to a cell rather than to the shared overview; Fill writes it", ref, cellZoom)
		}
		if err := ctx.Err(); err != nil {
			return f, fmt.Errorf("slice: filling the overview of source %s: %w", s.id, err)
		}
		n, got, err := s.store1(a, ref)
		if err != nil {
			return f, err
		}
		switch {
		case !got:
			f.Absent++
		case n < 0:
			f.Skipped++
		default:
			f.Written++
			f.Bytes += n
		}
		held = held.union(ZoomRange{Min: ref.Z, Max: ref.Z})
	}
	if held.empty() {
		return f, nil
	}
	if err := s.widen(held); err != nil {
		return f, err
	}
	return f, nil
}

// store1 writes one tile, skipping it if it is already there.
//
// The returned count is the bytes written, -1 for a tile that was already on
// disk, and the boolean is whether the archive holds it at all.
func (s *Source) store1(a Archive, ref TileRef) (int64, bool, error) {
	path := s.tilePath(ref)
	if s.Has(ref) {
		return -1, true, nil
	}
	data, ok, err := a.RawTile(ref.Z, ref.X, ref.Y)
	if err != nil {
		return 0, false, fmt.Errorf("slice: reading tile %s from the archive for source %s: %w", ref, s.id, err)
	}
	if !ok {
		return 0, false, nil
	}
	if err := writeFileAtomic(path, data); err != nil {
		return 0, false, err
	}
	return int64(len(data)), true, nil
}

// widen records that the slice now reaches a zoom range it did not before.
//
// It only ever grows. A slice that held the world at 0 to 5 and then took a
// city at 12 to 15 holds 0 to 15, and the range is a statement about the ends
// rather than a promise about every zoom between them -- which is why coverage
// is a separate question, asked cell by cell.
func (s *Source) widen(z ZoomRange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.manifest.Zoom.union(z)
	if next == s.manifest.Zoom {
		return nil
	}
	m := s.manifest
	m.Zoom = next
	m.Updated = time.Now().UTC()
	if err := writeJSON(filepath.Join(s.store.root, s.id, manifestFileName), m); err != nil {
		return err
	}
	s.manifest = m
	return nil
}
