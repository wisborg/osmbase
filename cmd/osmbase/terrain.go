package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/dem"
	"github.com/wisborg/osmbase/fetch"
	"github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
	"github.com/wisborg/osmbase/terrain"
)

// defaultTerrainSource is where terrain is fetched from when --terrain-source
// does not say: Mapterhorn's download host, chosen in docs/elevation.md.
//
// A default here and not in the library, for the reason the map's has one
// here: the library names no host, and the command is where a default can be
// named in the help and changed by a flag. Pointing --terrain-source at a
// directory of archives already on disk requests nothing from anyone.
const defaultTerrainSource = "https://download.mapterhorn.com"

// terrainIndexLimit bounds the terrain index this reads: Mapterhorn's lists
// four hundred and fifty archives in under a hundred kilobytes, so anything
// past a few megabytes is not an index.
const terrainIndexLimit = 4 << 20

// terrainRoot is where terrain is kept when --terrain-store does not say:
// beside the map's store, never inside it. Inside, it would count against the
// map's budget and make every render's choice of source ambiguous -- in every
// program sharing the store, released ones included. See the terrain
// package.
func terrainRoot(mapRoot string) string { return filepath.Clean(mapRoot) + "-terrain" }

// terrainLayout reads where source's terrain archives are: a host's index,
// or a directory of archives.
func terrainLayout(ctx context.Context, source string, stderr io.Writer) (terrain.Layout, error) {
	if !strings.Contains(source, "://") {
		return terrain.ReadDir(source)
	}
	index := strings.TrimSuffix(source, "/") + "/" + terrain.IndexName
	// Announced before the request, as the map's default is: a notice after
	// it tells the user about a request that has already gone.
	fmt.Fprintf(stderr, "osmbase: reading the list of terrain archives at %s\n", index)
	var b bytes.Buffer
	if _, err := acquire.DownloadContext(ctx, index, &b, terrainIndexLimit, time.Minute); err != nil {
		return terrain.Layout{}, err
	}
	return terrain.ReadIndex(source, &b)
}

// terrainOpener opens a terrain archive the way the map's is opened: with the
// per-request trace on stderr, for the silent part of planning.
func terrainOpener(stderr io.Writer) terrain.Opener {
	return func(source string) (*fetch.Archive, error) {
		return fetch.Open(source, fetch.Options{Trace: func(line string) {
			fmt.Fprintf(stderr, "osmbase: %s\n", line)
		}})
	}
}

// writeTerrainPlan says what a terrain fetch would take, archive by archive.
func writeTerrainPlan(w io.Writer, p *terrain.Plan, source, root string) {
	fmt.Fprintf(w, "\n%-12s %s\n", "terrain", source)
	fmt.Fprintf(w, "%-12s %s\n", "store", root)
	line := func(what string, tiles, held, absent int, transfer int64) {
		fmt.Fprintf(w, "%-12s %d to fetch", what, tiles)
		if held > 0 {
			fmt.Fprintf(w, ", %d already held", held)
		}
		if absent > 0 {
			fmt.Fprintf(w, ", %d not in the archive", absent)
		}
		fmt.Fprintf(w, ", %s\n", humanBytes(transfer))
	}
	g := p.Global
	line(fmt.Sprintf("zooms %d-%d", g.Overview.Min, max(g.Zoom.Max, g.Overview.Max)), g.Tiles, g.Held, g.Absent, g.Transfer)
	for _, r := range p.Regions {
		line(fmt.Sprintf("to zoom %d", r.Plan.Zoom.Max), r.Plan.Tiles, r.Plan.Held, r.Plan.Absent, r.Plan.Transfer)
	}
	if c := p.Coverage; c != nil {
		line("coverage", c.Tiles, c.Held, c.Absent, c.Transfer)
	}
	if p.Sources != "" {
		what := "copied from " + p.Sources
		if strings.Contains(p.Sources, "://") {
			what = "one more request, for " + p.Sources
		}
		fmt.Fprintf(w, "%-12s who made each source, for the credit: %s\n", "sources", what)
	}
	t := p.Totals()
	fmt.Fprintf(w, "%-12s %s in %d range requests\n", "download", humanBytes(t.Transfer), t.Requests)
}

// terrainStore is a terrain store opened for drawing: its elevation, and its
// coverage for the credit.
type terrainStore struct {
	root      string
	elevation *slice.Source
	heights   *dem.Source
	coverage  *slice.Source // nil when the store holds none
	sources   []dem.Attribution
}

// openTerrainStore opens the terrain store at root for drawing. It reads the
// disk and nothing else: a render never fetches terrain, as it never fetches
// a map without asking, and an empty store is refused with the command that
// fills it.
func openTerrainStore(root string) (*terrainStore, error) {
	refuse := fmt.Errorf("there is no terrain at %s; fetch it with \"osmbase fetch --terrain\" over the same area first", root)
	st, err := slice.Open(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, refuse
		}
		return nil, fmt.Errorf("opening the terrain store at %s: %w", root, err)
	}
	sources, err := st.Sources()
	if err != nil {
		return nil, err
	}
	t := &terrainStore{root: root}
	for _, m := range sources {
		switch m.TileType {
		case "webp", "png":
			if t.elevation != nil {
				return nil, fmt.Errorf("the terrain store at %s holds more than one elevation source; give each its own --terrain-store", root)
			}
			if t.elevation, err = st.Source(m.ID); err != nil {
				return nil, err
			}
		case "mvt":
			if t.coverage, err = st.Source(m.ID); err != nil {
				return nil, err
			}
		}
	}
	if t.elevation == nil {
		return nil, refuse
	}
	t.heights = dem.NewSource(t.elevation)
	// The list of who made each source, which a fetch keeps. A store
	// without one still credits each source, by its id.
	if f, err := os.Open(filepath.Join(root, terrain.SourcesName)); err == nil {
		t.sources, err = dem.ReadAttributions(f)
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	return t, nil
}

// into sets a render's terrain options from the store, for a view: nothing
// when there is no store.
func (t *terrainStore) into(o *render.Options, view render.View) error {
	if t == nil {
		return nil
	}
	short, full, err := t.credit(view)
	if err != nil {
		return err
	}
	o.Terrain = t.heights
	o.TerrainAttribution = short
	o.TerrainNotice = full
	return nil
}

// credit is what the terrain under view owes: a short credit for the image,
// pointing to where every source is listed, and the full notice --
// Mapterhorn, each source the coverage puts under the view, Copernicus's
// sentence -- for whoever publishes it. A store with no coverage can say
// only where the terrain came from, which is still owed, and is short
// enough to be both.
func (t *terrainStore) credit(view render.View) (short, full string, err error) {
	if t.coverage == nil {
		s := "Elevation: " + t.elevation.Manifest().Source
		return s, s, nil
	}
	z, _, err := view.Zoom()
	if err != nil {
		return "", "", err
	}
	b := view.Bounds
	ids, err := dem.SourcesIn(t.coverage, max(z, 1)-1, b.West, b.South, b.East, b.North)
	if err != nil {
		return "", "", err
	}
	return dem.ShortCredit(ids), dem.Credit(ids, t.sources), nil
}
