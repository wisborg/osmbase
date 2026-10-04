package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/fetch"
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
	t := p.Totals()
	fmt.Fprintf(w, "%-12s %s in %d range requests\n", "download", humanBytes(t.Transfer), t.Requests)
}
