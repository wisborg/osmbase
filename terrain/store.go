package terrain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/dem"
	"github.com/wisborg/osmbase/fetch"
	"github.com/wisborg/osmbase/slice"
)

// This file is the terrain as a program drawing maps uses it: where it is
// kept, opening it to draw from, what a view of it owes, what a view lacks,
// and filling that. osmbase's own command is built on it, and so is every
// other program that shades a map, so that the store's place, its credit and
// the order of the questions asked before a fetch are decided once.

// Root is where the terrain for the map store at mapRoot is kept: beside it,
// never inside it, its name the map store's with "-terrain" after it. See
// the package comment for why it cannot be inside.
//
// Every program sharing a map store has to find its terrain in the same
// place, or the same ground is fetched twice; that is why the name is
// decided here and not by each of them.
func Root(mapRoot string) string { return filepath.Clean(mapRoot) + "-terrain" }

// ErrNoTerrain is wrapped by Open's error when there is no terrain at all to
// draw from -- no store, or a store with no elevation in it -- which is the
// case Measure and Fill can remedy, as against a store that cannot be read.
var ErrNoTerrain = errors.New("no terrain has been fetched")

// Store is a terrain store opened for drawing: its elevation, decoded on
// demand, and what it needs to say whom a view owes.
type Store struct {
	root      string
	elevation *slice.Source
	heights   *dem.Source
	coverage  *slice.Source // nil when the store holds none
	sources   []dem.Attribution
}

// Open opens the terrain store at root for drawing. It reads the disk and
// nothing else.
func Open(root string) (*Store, error) {
	none := fmt.Errorf("terrain: %s: %w", root, ErrNoTerrain)
	st, err := slice.Open(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, none
		}
		return nil, fmt.Errorf("terrain: opening the store at %s: %w", root, err)
	}
	ms, err := st.Sources()
	if err != nil {
		return nil, err
	}
	s := &Store{root: root}
	for _, m := range ms {
		switch m.TileType {
		case "webp", "png":
			if s.elevation != nil {
				return nil, fmt.Errorf("terrain: the store at %s holds more than one elevation source; keep each in a store of its own", root)
			}
			if s.elevation, err = st.Source(m.ID); err != nil {
				return nil, err
			}
		case "mvt":
			if s.coverage, err = st.Source(m.ID); err != nil {
				return nil, err
			}
		}
	}
	if s.elevation == nil {
		return nil, none
	}
	s.heights = dem.NewSource(s.elevation)
	// The list of who made each source, which a fetch keeps. A store
	// without one still credits each source, by its id.
	if f, err := os.Open(filepath.Join(root, SourcesName)); err == nil {
		s.sources, err = dem.ReadAttributions(f)
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Root is where the store is.
func (s *Store) Root() string { return s.root }

// Heights is the store's elevation, for render.Options.Terrain.
func (s *Store) Heights() *dem.Source { return s.heights }

// Credit is what a view of b drawn at map zoom mapZoom owes the elevation: a
// short credit for the image itself, pointing to where every source is
// listed, and the full notice -- Mapterhorn, each source the coverage puts
// under the view, Copernicus's dictated sentence -- for whoever publishes
// it. They are render.Options' TerrainAttribution and TerrainNotice; see
// dem.ShortCredit for why there are two.
//
// A store with no coverage can say only where the terrain came from, which
// is still owed, and short enough to be both.
func (s *Store) Credit(b slice.Bounds, mapZoom uint8) (short, full string, err error) {
	if s.coverage == nil {
		c := "Elevation: " + s.elevation.Manifest().Source
		return c, c, nil
	}
	ids, err := dem.SourcesIn(s.coverage, max(mapZoom, 1)-1, b.West, b.South, b.East, b.North)
	if err != nil {
		return "", "", err
	}
	return dem.ShortCredit(ids), dem.Credit(ids, s.sources), nil
}

// Shortfall is what a terrain store lacks for one view, measured from the
// disk alone.
type Shortfall struct {
	Root   string
	Bounds slice.Bounds
	// Zoom is the elevation zoom the view draws from: the map's less one,
	// no deeper than the source offers.
	Zoom uint8
	// Held and Wanted are the elevation tiles of the view at Zoom the store
	// has, and needs.
	Held, Wanted int
	// Empty is no store, or a store with no elevation in it.
	Empty bool
}

// MapZoom is the map zoom a fetch for the shortfall asks for: the terrain
// is fetched a zoom shallower than the map.
func (s Shortfall) MapZoom() uint8 { return s.Zoom + 1 }

// Measure reports what the terrain store at root lacks for a view of b drawn
// at map zoom mapZoom, and whether it lacks anything at all.
//
// It reads the disk and nothing else, which is the point of it. Whether to
// offer a fetch has to be settled before anything reaches a host -- reading
// even the list of archives tells the host something -- so a program asks
// this first, and only after a yes calls Locate and Fill.
//
// A store that cannot be read reports nothing lacking: a fetch would not
// mend it, and Open says why it cannot be drawn from.
func Measure(root string, b slice.Bounds, mapZoom uint8) (Shortfall, bool) {
	s := Shortfall{Root: root, Bounds: b, Zoom: max(mapZoom, 1) - 1}
	st, err := slice.Open(root)
	if errors.Is(err, fs.ErrNotExist) {
		s.Empty = true
		return s, true
	}
	if err != nil {
		return s, false
	}
	ms, err := st.Sources()
	if err != nil {
		return s, false
	}
	for _, m := range ms {
		if m.TileType != "webp" && m.TileType != "png" {
			continue
		}
		src, err := st.Source(m.ID)
		if err != nil {
			return s, false
		}
		s.Zoom = fetch.DrawnZoom(src, s.Zoom)
		if s.Held, s.Wanted, err = src.HeldAt(b, s.Zoom); err != nil {
			return s, false
		}
		return s, s.Held < s.Wanted
	}
	s.Empty = true
	return s, true
}

// IndexLimit bounds the index of archives Locate reads: Mapterhorn's lists
// four hundred and fifty archives in under a hundred kilobytes, so anything
// past a few megabytes is not an index.
const IndexLimit = 4 << 20

// Locate finds the archives of the terrain at source: a host's address,
// whose index it downloads, or a directory of archives, which it lists and
// which contacts nobody. announce, when not nil, is told the index's address
// before it is requested, so a program can say what is about to be asked of
// whom while it still means something.
func Locate(ctx context.Context, source string, announce func(index string)) (Layout, error) {
	if !strings.Contains(source, "://") {
		return ReadDir(source)
	}
	index := strings.TrimSuffix(source, "/") + "/" + IndexName
	if announce != nil {
		announce(index)
	}
	var b bytes.Buffer
	if _, err := acquire.DownloadContext(ctx, index, &b, IndexLimit, time.Minute); err != nil {
		return Layout{}, err
	}
	return ReadIndex(source, &b)
}

// Fill fetches what a view of b drawn at map zoom mapZoom needs from the
// archives l names into the terrain store at root, creating the store if
// need be: the sequence "osmbase fetch --terrain" runs, for a program that
// has already asked and been told yes.
//
// report, when not nil, is given the plan before anything is fetched, so a
// program can say what is about to cross the network; progress is passed
// to Plan.Fetch. A plan with nothing in it fetches nothing and is not an
// error.
func Fill(ctx context.Context, l Layout, root string, b slice.Bounds, mapZoom uint8, open Opener,
	report func(*Plan), progress func(acquire.Progress)) (Result, error) {
	st, err := slice.Create(root, slice.Config{})
	if err != nil {
		return Result{}, fmt.Errorf("terrain: opening the store at %s: %w", root, err)
	}
	p, err := Prepare(ctx, l, st, acquire.Request{Bounds: b, MaxZoom: int(mapZoom), CellZoom: st.CellZoom()}, open)
	if err != nil {
		return Result{}, err
	}
	defer p.Close()
	if report != nil {
		report(p)
	}
	if p.Empty() {
		return Result{}, p.Settle()
	}
	p.Silence()
	return p.Fetch(ctx, progress)
}
