package slice

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Source is one source's slice within a store: its manifest, its overview
// tiles and its cells.
//
// It satisfies render.TileSource with no adapter, which is the point of the
// method set being as narrow as it is. The renderer asks one question -- have
// you got the tile at z/x/y -- and everything else here is the store's
// business and none of a renderer's.
//
// It is safe for concurrent use.
type Source struct {
	store *Store
	id    string

	// compression is fixed for the life of a source directory: AddSource
	// refuses to change it, because tiles are stored exactly as fetched and a
	// directory holding two encodings would need a flag per file.
	compression Compression

	mu       sync.Mutex
	manifest Manifest
}

// ID is the source's directory name under the store root.
func (s *Source) ID() string { return s.id }

// Manifest returns a copy of what the store records about this source: where
// it came from, what schema it is, the credit it requires, and the zoom range
// it holds.
func (s *Source) Manifest() Manifest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manifest
}

// Tile returns the decompressed vector tile bytes at z/x/y, and whether the
// store holds it.
//
// This is render.TileSource. The boolean is not the same question as the
// error: a store legitimately has no tile at most coordinates -- outside the
// cells anybody fetched, outside the slice's zoom range, or over ocean the
// archive's writer chose to omit -- and that is an answer rather than a
// failure. A miss is where it ends. Nothing here fetches anything.
//
// The bytes come back DECOMPRESSED because that is what the renderer's
// contract says, and they are stored compressed because that is what keeps the
// store small. The asymmetry is deliberate: a tile is decompressed once per
// render and would otherwise be decompressed once per download and then stored
// three or four times larger forever.
//
// A tile inside an INCOMPLETE cell is served like any other. Incompleteness is
// a fact about coverage, reported by Coverage, and not a reason to withhold a
// file that is whole: the tiles a killed fetch did write are real tiles, and
// refusing them would turn an interrupted download into a blank map rather
// than a partial one.
func (s *Source) Tile(z uint8, x, y uint32) ([]byte, bool, error) {
	ref := TileRef{Z: z, X: x, Y: y}
	path := s.tilePath(ref)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("slice: reading tile %s from %s: %w", ref, path, err)
	}
	data, err := s.compression.decompress(raw, "tile "+ref.String())
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// Has reports whether the store holds a tile, without reading or
// decompressing it.
//
// It is what a fetch uses to resume: a cell whose tiles are half written is
// finished by skipping the ones already on disk. It is deliberately not how
// coverage is measured -- that is a question about cells, answered by
// Coverage.
func (s *Source) Has(t TileRef) bool {
	info, err := os.Stat(s.tilePath(t))
	return err == nil && info.Mode().IsRegular()
}

// Cell returns what the store records about one cell, and whether there is a
// complete one there at all.
//
// The false case covers both "nothing here" and "a fetch was interrupted": in
// either case there is no cell.json, because it is written last. A caller that
// needs to tell them apart asks Coverage, which reports partial cells
// separately from missing ones.
func (s *Source) Cell(c Cell) (CellInfo, bool, error) {
	var ci CellInfo
	if err := readJSON(filepath.Join(s.cellDir(c), cellFileName), &ci); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// An empty zoom range rather than the zero value, which would read
			// as "this cell holds zoom 0 to 0". A caller unioning what came
			// back with what it is about to fetch would then record a cell
			// reaching all the way up to the whole world.
			return CellInfo{Cell: c, Zoom: emptyZoom}, false, nil
		}
		return CellInfo{}, false, err
	}
	ci.Cell = c
	if !ci.Complete {
		return ci, false, nil
	}
	return ci, true, nil
}

// Cells lists every cell directory in the source, ordered by coordinate.
//
// Both complete and interrupted ones: an interrupted cell occupies disk and is
// something a report should mention, and pretending it is not there would make
// a store's byte count unexplainable.
func (s *Source) Cells() ([]Cell, error) {
	entries, err := os.ReadDir(s.cellsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("slice: listing the cells of source %s: %w", s.id, err)
	}
	var out []Cell
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if c, ok := parseCellDir(e.Name()); ok {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Y != out[j].Y {
			return out[i].Y < out[j].Y
		}
		return out[i].X < out[j].X
	})
	return out, nil
}

// Bytes is how much disk this source occupies, measured by walking it. See
// Store.Bytes for why it is measured rather than remembered.
func (s *Source) Bytes() (int64, error) { return dirBytes(s.sourceDir()) }

// Coverage is the honest account of what a source holds over an area, counted
// in cells and in overview tiles and never in ink.
//
// Every number here comes from which files exist. None of it is measured from
// a rendered image, and it must stay that way: an ocean tile draws almost
// nothing and is fully covered, a tile of empty desert draws nothing at all,
// and a coverage figure derived from pixels would report both as missing data.
// See docs/architecture.md, trap T9.
type Coverage struct {
	// CellZoom is the store's, and Zoom is the range the manifest says this
	// slice holds. Both are here because a coverage report with no depth on it
	// cannot be read: zero cells over an area is complete coverage for a slice
	// that holds the world at zoom 0 to 5, and a hole for one that holds a
	// city at 12 to 15.
	CellZoom uint8
	Zoom     ZoomRange

	// Cells is how many cells the area touches and Complete how many of those
	// hold a finished fetch. They are counted from the directories that exist
	// and never from the manifest, so a slice held entirely above the cell
	// zoom reports every cell as missing -- which is true, and is read
	// together with Zoom and OverviewHeld.
	Cells    int
	Complete int

	// Partial are the cells with tiles on disk and no cell.json, which is what
	// an interrupted fetch leaves. They are listed apart from Missing because
	// the remedy is different: a partial cell is resumed, a missing one is
	// fetched.
	Partial []Cell
	// Missing are the cells with nothing at all.
	Missing []Cell

	// OverviewWanted is how many shallow tiles the slice's zoom range calls
	// for above these cells, and OverviewHeld how many are there. For a slice
	// held entirely above the cell zoom -- the whole world at zoom 0 to 5 --
	// these two are the whole answer and the cell counts are all zero.
	OverviewWanted, OverviewHeld int
}

// Fraction is the share of the area's cells that hold a finished fetch, from 0
// to 1. It is 0 when the area touches no cells, which for a shallow slice
// means the cell grid is the wrong question and OverviewHeld is the right one.
func (c Coverage) Fraction() float64 {
	if c.Cells == 0 {
		return 0
	}
	return float64(c.Complete) / float64(c.Cells)
}

// Coverage reports what this source holds over an area.
//
// It is answered by a stat per cell rather than by a geometric search, which
// is the reason the store is keyed on a cell grid at all: a cache keyed on
// per-activity bounding boxes cannot answer "do I have coverage here" without
// walking every entry it has ever stored, and two runs from the same front
// door produce two boxes that never share a byte.
func (s *Source) Coverage(b Bounds) (Coverage, error) {
	m := s.Manifest()
	cov := Coverage{CellZoom: s.store.cellZoom, Zoom: m.Zoom}

	cells, err := cellsFor(b, s.store.cellZoom)
	if err != nil {
		return Coverage{}, err
	}
	// The cells are counted from what is on disk and never from the manifest's
	// zoom range. A slice that holds nothing at or below the cell zoom -- the
	// whole world at zoom 0 to 5 -- reports every cell of the area as missing,
	// and that is the right answer to the question the cell grid asks: there
	// is no street-level data here. Zoom and OverviewHeld are beside it to say
	// what there IS, and suppressing the cell counts instead would hide a
	// half-fetched cell from a store whose manifest had not been widened yet.
	cov.Cells = len(cells)
	for _, c := range cells {
		_, complete, err := s.Cell(c)
		if err != nil {
			return Coverage{}, err
		}
		switch {
		case complete:
			cov.Complete++
		case s.cellHasFiles(c):
			cov.Partial = append(cov.Partial, c)
		default:
			cov.Missing = append(cov.Missing, c)
		}
	}

	// The overview half. The ancestors of the area's cells, deduplicated,
	// because two neighbouring cells share every ancestor above a certain
	// zoom and counting them twice would make a fully covered area look
	// half covered.
	if !m.Zoom.empty() {
		seen := make(map[TileRef]bool)
		for _, c := range cells {
			refs, err := AncestorTiles(c, s.store.cellZoom, m.Zoom)
			if err != nil {
				return Coverage{}, err
			}
			for _, r := range refs {
				if seen[r] {
					continue
				}
				seen[r] = true
				cov.OverviewWanted++
				if s.Has(r) {
					cov.OverviewHeld++
				}
			}
		}
	}
	return cov, nil
}

// cellHasFiles reports whether a cell directory exists with anything in it,
// which together with a missing cell.json is what an interrupted fetch looks
// like.
func (s *Source) cellHasFiles(c Cell) bool {
	entries, err := os.ReadDir(s.cellDir(c))
	return err == nil && len(entries) > 0
}

// Hold marks cells as in use by a render, and records that they were used.
//
// It does both jobs in one call on purpose. They are the same moment: a render
// is about to read these cells, so they are the most recently used cells in
// the store and they are the cells nothing may remove until it finishes.
// Splitting them into two calls would mean a caller that made only one, and
// either half alone is a quiet failure -- a forgotten Touch turns least-
// recently-used into least-recently-FETCHED, and a forgotten hold puts a hole
// in the middle of a video.
//
// Evicting a cell a running render is reading produces a hole in the map that
// appears on some frames and not others, which is the worst shape a bug can
// take here. Eviction belongs at the end of an acquisition and at the start of
// a render, never during one; this is what makes that structural for renders
// that share this Store value. It is not a lock on disk: another process, or
// another Store opened over the same root, will not see it. Two programs
// sharing one slice is the design's intent, so this is worth saying plainly
// rather than implying otherwise with the word "lock".
//
// Release is idempotent. Holding the same cell twice holds it until both are
// released.
func (s *Source) Hold(cells []Cell) *Hold {
	h := &Hold{store: s.store, source: s.id}
	seen := make(map[Cell]bool, len(cells))
	for _, c := range cells {
		if seen[c] {
			continue
		}
		seen[c] = true
		h.cells = append(h.cells, c)
	}

	s.store.mu.Lock()
	for _, c := range h.cells {
		s.store.held[heldKey{source: s.id, cell: c}]++
	}
	s.store.mu.Unlock()

	h.touchErr = s.touch(h.cells)
	return h
}

// touch moves the eviction clock on every complete cell in the list.
//
// A cell with no cell.json -- one an interrupted fetch left behind -- is
// skipped, because there is no file to record a timestamp in. Its clock is the
// directory's modification time, which is when the fetch last wrote to it, and
// that is the right answer for it: leftovers from a download that died five
// minutes ago are newer than a cell rendered from last week, and evicting them
// first would throw away the one thing that makes a fetch resumable.
func (s *Source) touch(cells []Cell) error {
	now := time.Now().UTC()
	var firstErr error
	for _, c := range cells {
		ci, complete, err := s.Cell(c)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !complete {
			continue
		}
		ci.LastUsed = now
		if err := writeJSON(filepath.Join(s.cellDir(c), cellFileName), ci); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Hold is a render's claim on a set of cells. Release it when the render is
// finished.
type Hold struct {
	store  *Store
	source string
	cells  []Cell

	mu       sync.Mutex
	released bool
	touchErr error
}

// Release gives the cells back. It is safe to call more than once, so the
// ordinary shape is a deferred call next to the Hold.
func (h *Hold) Release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return
	}
	h.released = true
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	for _, c := range h.cells {
		k := heldKey{source: h.source, cell: c}
		if n := h.store.held[k]; n > 1 {
			h.store.held[k] = n - 1
		} else {
			delete(h.store.held, k)
		}
	}
}

// TouchError reports whether the eviction clock could be moved, and it is
// returned this way rather than from Hold for a reason.
//
// A render must not fail because a timestamp could not be written. The cost of
// an unwritten one is that a cell may be evicted earlier than it deserved,
// which is a slower render later and never a wrong picture. But it is also a
// filesystem that is not behaving, so it is not swallowed either: a command
// doing the acquiring can print it, and a render can ignore it.
func (h *Hold) TouchError() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.touchErr
}

// isHeld reports whether a cell is claimed by any hold in this process. The
// caller holds st.mu.
func (st *Store) isHeld(source string, c Cell) bool {
	return st.held[heldKey{source: source, cell: c}] > 0
}
