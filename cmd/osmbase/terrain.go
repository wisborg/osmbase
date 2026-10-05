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

// terrainShortfall is what a terrain store lacks for one view, measured from
// the disk alone.
type terrainShortfall struct {
	root   string
	bounds slice.Bounds
	// zoom is the elevation zoom the view draws from: the map's less one,
	// no deeper than the source offers.
	zoom   uint8
	held   int
	wanted int
	empty  bool // no store, or a store with no elevation in it
}

// measureTerrain reports what the terrain store at root lacks for a view of
// b drawn at map zoom mapZoom, and whether it lacks anything at all. A store
// that cannot be read is not something an offer can fix; opening it for the
// render reports why.
func measureTerrain(root string, b slice.Bounds, mapZoom uint8) (terrainShortfall, bool) {
	s := terrainShortfall{root: root, bounds: b, zoom: max(mapZoom, 1) - 1}
	st, err := slice.Open(root)
	if errors.Is(err, fs.ErrNotExist) {
		s.empty = true
		return s, true
	}
	if err != nil {
		return s, false
	}
	sources, err := st.Sources()
	if err != nil {
		return s, false
	}
	for _, m := range sources {
		if m.TileType != "webp" && m.TileType != "png" {
			continue
		}
		src, err := st.Source(m.ID)
		if err != nil {
			return s, false
		}
		s.zoom = fetch.DrawnZoom(src, s.zoom)
		if s.held, s.wanted, err = src.HeldAt(b, s.zoom); err != nil {
			return s, false
		}
		return s, s.held < s.wanted
	}
	s.empty = true
	return s, true
}

// offerTerrain asks whether to fetch the terrain a view lacks, and fetches it
// if the answer is yes. It is offerToFill for the terrain store, for the
// same reasons and on the same terms: the question names whoever would be
// contacted before anything is requested -- the list of archives included --
// and a no, a pipe or a failed download is not an error.
func offerTerrain(ctx context.Context, w io.Writer, s terrainShortfall, source string, yes bool) {
	if s.empty {
		fmt.Fprintf(w, "osmbase: %s holds no terrain yet, so there is nothing to shade the map with.\n", s.root)
	} else {
		fmt.Fprintf(w, "osmbase: this view's terrain needs %d tiles at zoom %d and %s holds %d of them;\n", s.wanted, s.zoom, s.root, s.held)
		fmt.Fprintf(w, "osmbase:   the rest would be shaded from shallower tiles, more smoothly than the ground is.\n")
	}
	if strings.Contains(source, "://") {
		fmt.Fprintf(w, "osmbase: fetching it contacts %s, which learns which part\n", hostOf(source))
		fmt.Fprintf(w, "osmbase:   of the map you asked about. Afterwards, rendering it contacts nobody.\n")
	} else {
		fmt.Fprintf(w, "osmbase: it would be copied from %s, contacting nobody.\n", source)
	}

	later := fmt.Sprintf("osmbase fetch --store %s --bbox %s --max-zoom %d --terrain", strings.TrimSuffix(s.root, "-terrain"), bboxString(s.bounds), int(s.zoom)+1)
	switch (fetch.Consent{Yes: yes, Answerable: stdinAnswerable}).Ask(w, "Fetch the terrain now? [y/N] ") {
	case fetch.Unattended:
		fmt.Fprintf(w, "osmbase: nothing is attached to answer, so no terrain was fetched; pass --yes, or run\n")
		fmt.Fprintf(w, "osmbase:   %s\n", later)
		return
	case fetch.NoAnswer:
		fmt.Fprintf(w, "\nosmbase: no answer, so no terrain was fetched\n")
		return
	case fetch.Declined:
		fmt.Fprintf(w, "osmbase: not fetched; when you want it:\nosmbase:   %s\n", later)
		return
	}
	if err := fillTerrain(ctx, w, s, source); err != nil {
		fmt.Fprintf(w, "osmbase: %v\nosmbase: carrying on with what the terrain store holds\n", err)
	}
}

// fillTerrain carries out the fetch offerTerrain got consent for: the
// sequence "osmbase fetch --terrain" runs, for the view's area and depth.
func fillTerrain(ctx context.Context, w io.Writer, s terrainShortfall, source string) error {
	layout, err := terrainLayout(ctx, source, w)
	if err != nil {
		return err
	}
	st, err := slice.Create(s.root, slice.Config{})
	if err != nil {
		return fmt.Errorf("opening the terrain store at %s: %w", s.root, err)
	}
	// MaxZoom is the map's zoom: Prepare takes the terrain one shallower.
	p, err := terrain.Prepare(ctx, layout, st, acquire.Request{
		Bounds: s.bounds, MaxZoom: int(s.zoom) + 1, CellZoom: st.CellZoom(),
	}, terrainOpener(w))
	if err != nil {
		return err
	}
	defer p.Close()
	writeTerrainPlan(w, p, source, s.root)
	if p.Empty() {
		fmt.Fprintln(w, "osmbase: nothing to fetch after all: the archives hold no more of this view than the store does")
		return nil
	}
	p.Silence()
	res, err := p.Fetch(ctx, progressTo(w))
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%-12s %d terrain tiles in %d requests, %s\n", "fetched", res.Written, res.Requests, humanBytes(res.Transfer))
	return nil
}
