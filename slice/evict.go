package slice

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Evicted is one cell an eviction removed.
type Evicted struct {
	Source   string
	Cell     Cell
	Bytes    int64
	LastUsed time.Time
	// Complete says whether the cell held a finished fetch. An incomplete one
	// is the leftovers of a download that died.
	Complete bool
}

// Eviction is what one call to Evict did.
type Eviction struct {
	Budget        int64
	Before, After int64
	Cells         []Evicted

	// Held is how many candidate cells were passed over because a render in
	// this process had them. A store that cannot reach its budget because
	// everything in it is being read is a fact to report, not a failure: the
	// alternative is removing ground out from under a render that is drawing
	// it.
	Held int
}

// Bytes is what the eviction freed.
func (e Eviction) Bytes() int64 { return e.Before - e.After }

// Evict removes whole cells, least recently used first, until the store fits
// in budget.
//
// # When to call it
//
// At the end of an acquisition and at the start of a render. NEVER during one.
// Evicting a cell a running render is reading produces a hole in the map that
// appears on some frames and not others, which is the worst shape a bug can
// take here: it is not reproducible from the activity, it moves when the
// budget moves, and it looks like a rasterizer fault. Cells held by a render
// in this process are skipped and counted, which makes that safe for the
// common case of one program; it is not a lock on disk, and a second program
// evicting from a shared store cannot see this one's holds.
//
// # What is never removed
//
// The unit is a cell directory, so a manifest, a store file and the overview
// tiles are not candidates -- there is no code path here that can reach them.
// The overview is twelve files per source and it is what makes a partially
// evicted area degrade into a coarser map instead of vanishing; evicting it to
// save twelve files would turn every eviction into a hole. A global slice is
// held entirely in the overview and is therefore never evicted at all, which
// is correct: at 19.5 MB for every tile on earth at flight detail, it is the
// floor the whole store falls back to.
//
// # Least recently used, by cell
//
// The clock is cell.json's LastUsed, which a render moves once per render
// rather than once per tile. A cell an interrupted fetch left behind has no
// cell.json and uses its directory's modification time instead -- which is
// when that fetch last wrote to it, so the leftovers of a download that died a
// minute ago sort as newly used and survive, and the fetch stays resumable.
//
// Reaching the budget is not guaranteed. If everything left is held or is
// overview, After comes back above Budget and Held says why. That is an
// honest report rather than an error: nothing has gone wrong, there is simply
// nothing this call is allowed to remove.
func (s *Store) Evict(budget int64) (Eviction, error) {
	if budget < 0 {
		return Eviction{}, fmt.Errorf("slice: an eviction budget of %d bytes is not a size", budget)
	}
	total, err := s.Bytes()
	if err != nil {
		return Eviction{}, err
	}
	ev := Eviction{Budget: budget, Before: total, After: total}
	if total <= budget {
		return ev, nil
	}

	candidates, err := s.candidates()
	if err != nil {
		return Eviction{}, err
	}
	// Oldest first, with the coordinates as the tiebreak so that two cells
	// last used in the same clock tick are removed in the same order every
	// time. An eviction that picked its victims from a map would be an
	// eviction nobody could write a test for.
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if !a.LastUsed.Equal(b.LastUsed) {
			return a.LastUsed.Before(b.LastUsed)
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Cell.Y != b.Cell.Y {
			return a.Cell.Y < b.Cell.Y
		}
		return a.Cell.X < b.Cell.X
	})

	// The lock is held across the removals rather than taken per cell, so a
	// Hold cannot be granted for a cell this loop has already chosen. It is
	// the one place in this package that does I/O under the lock, and it is
	// worth it: the window it closes is exactly "a render started while the
	// eviction was running", which is the case the whole hold mechanism is
	// for.
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range candidates {
		if ev.After <= budget {
			break
		}
		if s.isHeld(c.Source, c.Cell) {
			ev.Held++
			continue
		}
		dir := filepath.Join(s.root, c.Source, "cells", c.Cell.String())
		if err := os.RemoveAll(dir); err != nil {
			return ev, fmt.Errorf("slice: evicting cell %s of source %s: %w", c.Cell, c.Source, err)
		}
		ev.Cells = append(ev.Cells, c)
		ev.After -= c.Bytes
	}
	return ev, nil
}

// candidates lists every cell in the store with what eviction needs to know
// about it.
//
// It reads the directories rather than the Source objects on purpose: a store
// is shared, and cells written by another process since this one opened are
// just as evictable as the ones it wrote itself.
func (s *Store) candidates() ([]Evicted, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("slice: listing the store at %s: %w", s.root, err)
	}
	var out []Evicted
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		cellsDir := filepath.Join(s.root, id, "cells")
		cellDirs, err := os.ReadDir(cellsDir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("slice: listing the cells of source %s: %w", id, err)
		}
		for _, ce := range cellDirs {
			if !ce.IsDir() {
				continue
			}
			cell, ok := parseCellDir(ce.Name())
			if !ok {
				continue
			}
			dir := filepath.Join(cellsDir, ce.Name())
			bytes, err := dirBytes(dir)
			if err != nil {
				return nil, err
			}
			cand := Evicted{Source: id, Cell: cell, Bytes: bytes}

			var ci CellInfo
			switch err := readJSON(filepath.Join(dir, cellFileName), &ci); {
			case err == nil && ci.Complete:
				cand.Complete = true
				cand.LastUsed = ci.LastUsed
			case err == nil, errors.Is(err, fs.ErrNotExist):
				// An interrupted fetch, or a cell.json that says it is not
				// finished. Its clock is the directory's own.
				if info, err := ce.Info(); err == nil {
					cand.LastUsed = info.ModTime()
				}
			default:
				return nil, err
			}
			out = append(out, cand)
		}
	}
	return out, nil
}
