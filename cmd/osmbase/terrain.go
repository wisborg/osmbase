package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/wisborg/osmbase/acquire"
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

// terrainLayout reads where source's terrain archives are: a host's index,
// or a directory of archives. A host's index is announced before it is
// requested, as the map's default is: a notice after it tells the user about
// a request that has already gone.
func terrainLayout(ctx context.Context, source string, stderr io.Writer) (terrain.Layout, error) {
	return terrain.Locate(ctx, source, func(index string) {
		fmt.Fprintf(stderr, "osmbase: reading the list of terrain archives at %s\n", index)
	})
}

// terrainOpener opens a terrain archive the way the map's is opened: with the
// per-request trace on stderr, for the silent part of planning, and a fetch
// from it keeping at most requests in flight (zero: the library's default).
func terrainOpener(stderr io.Writer, requests int) terrain.Opener {
	return func(source string) (*fetch.Archive, error) {
		return fetch.Open(source, fetch.Options{Requests: requests, Trace: func(line string) {
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

// openTerrain opens the terrain store at root for drawing, refusing an
// empty one with the command that fills it.
func openTerrain(root string) (*terrain.Store, error) {
	ts, err := terrain.Open(root)
	if errors.Is(err, terrain.ErrNoTerrain) {
		return nil, fmt.Errorf("there is no terrain at %s; fetch it with \"osmbase fetch --terrain\" over the same area first", root)
	}
	return ts, err
}

// terrainInto sets a render's terrain options from the store, for a view:
// nothing when there is no store.
func terrainInto(ts *terrain.Store, o *render.Options, view render.View) error {
	if ts == nil {
		return nil
	}
	z, _, err := view.Zoom()
	if err != nil {
		return err
	}
	short, full, err := ts.Credit(viewBounds(view), z)
	if err != nil {
		return err
	}
	o.Terrain = ts.Heights()
	o.TerrainAttribution = short
	o.TerrainNotice = full
	return nil
}

func viewBounds(v render.View) slice.Bounds {
	return slice.Bounds{West: v.Bounds.West, South: v.Bounds.South, East: v.Bounds.East, North: v.Bounds.North}
}

// offerTerrain asks whether to fetch the terrain a view lacks, and fetches it
// if the answer is yes. It is offerToFill for the terrain store, for the
// same reasons and on the same terms: the question names whoever would be
// contacted before anything is requested -- the list of archives included,
// which is why terrain.Measure comes first and terrain.Locate only after a
// yes -- and a no, a pipe or a failed download is not an error.
func offerTerrain(ctx context.Context, w io.Writer, s terrain.Shortfall, source string, yes bool) {
	if s.Empty {
		fmt.Fprintf(w, "osmbase: %s holds no terrain yet, so there is nothing to shade the map with.\n", s.Root)
	} else {
		fmt.Fprintf(w, "osmbase: this view's terrain needs %d tiles at zoom %d and %s holds %d of them;\n", s.Wanted, s.Zoom, s.Root, s.Held)
		fmt.Fprintf(w, "osmbase:   the rest would be shaded from shallower tiles, more smoothly than the ground is.\n")
	}
	if strings.Contains(source, "://") {
		fmt.Fprintf(w, "osmbase: fetching it contacts %s, which learns which part\n", hostOf(source))
		fmt.Fprintf(w, "osmbase:   of the map you asked about. Afterwards, rendering it contacts nobody.\n")
	} else {
		fmt.Fprintf(w, "osmbase: it would be copied from %s, contacting nobody.\n", source)
	}

	later := fmt.Sprintf("osmbase fetch --store %s --bbox %s --max-zoom %d --terrain", strings.TrimSuffix(s.Root, "-terrain"), bboxString(s.Bounds), s.MapZoom())
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

// fillTerrain carries out the fetch offerTerrain got consent for.
func fillTerrain(ctx context.Context, w io.Writer, s terrain.Shortfall, source string) error {
	layout, err := terrainLayout(ctx, source, w)
	if err != nil {
		return err
	}
	var bar *fetchBar
	res, err := terrain.Fill(ctx, layout, s.Root, s.Bounds, s.MapZoom(), terrainOpener(w, 0), func(p *terrain.Plan) {
		writeTerrainPlan(w, p, source, s.Root)
		if p.Empty() {
			fmt.Fprintln(w, "osmbase: nothing to fetch after all: the archives hold no more of this view than the store does")
			return
		}
		bar = startFetchBar(w, "terrain", p.Totals().Transfer)
	}, func(pr acquire.Progress) { bar.update(pr) })
	bar.stop()
	if err != nil {
		return err
	}
	if res.Written > 0 {
		fmt.Fprintf(w, "%-12s %d terrain tiles in %d requests, %s\n", "fetched", res.Written, res.Requests, humanBytes(res.Transfer))
	}
	return nil
}
