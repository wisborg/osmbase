package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/slice"
)

func fetchUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `osmbase fetch — copy an area of the map onto this machine, once.

usage:
  osmbase fetch [SOURCE] --lat L --lon L [--radius KM] [flags]
  osmbase fetch [SOURCE] --bbox W,S,E,N [flags]
  osmbase fetch [SOURCE] --world [flags]

Afterwards "osmbase render --store DIR" draws from the copy and contacts
nobody. That pairing is the point of this command: this is the one moment the
map data crosses the network, and it is a moment you chose.

What the host learns is the cells you asked for -- squares several kilometres
across -- once, not a route and not every time you draw. Pass a local .pmtiles
file as SOURCE and it learns nothing at all.

examples:
  osmbase fetch --lat -33.8568 --lon 151.2153 --radius 3
  osmbase fetch --lat 51.5081 --lon -0.0759 --radius 2 --dry-run
  osmbase fetch --world --max-zoom 5      # every tile on earth, shallow

`)
	printFlags(w, fs)
}

type fetchFlags struct {
	lat, lon float64
	radius   float64
	bbox     string
	world    bool
	maxZoom  int
	store    string
	dryRun   bool
	yes      bool
}

func fetchCommand(args []string, stdout, stderr io.Writer) error {
	var f fetchFlags
	fs := newFlagSet("fetch", fetchUsage)
	fs.Float64Var(&f.lat, "lat", 0, "latitude at the centre of the area")
	fs.Float64Var(&f.lon, "lon", 0, "longitude at the centre of the area")
	fs.Float64Var(&f.radius, "radius", 3, "half-width of the area in kilometres")
	fs.StringVar(&f.bbox, "bbox", "", "the area as west,south,east,north in degrees, instead of a centre and radius")
	fs.BoolVar(&f.world, "world", false, "every tile on earth at shallow zooms, for a track that crosses oceans")
	fs.IntVar(&f.maxZoom, "max-zoom", acquire.AutoZoom, "deepest zoom to take; the default chooses one from the size of the area")
	fs.StringVar(&f.store, "store", "", "directory to keep the copy in (default: an osmbase folder under your user cache directory)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "say exactly what would be downloaded, then stop")
	fs.BoolVar(&f.yes, "yes", false, "do not ask before downloading")

	source, err := parseArgs(fs, args, stdout)
	if err != nil || fs.Parsed() && isHelpRequest(args) {
		return err
	}

	bounds, err := f.bounds(fs)
	if err != nil {
		return err
	}

	root := f.store
	if root == "" {
		d, err := slice.DefaultRoot()
		if err != nil {
			return usageErrorf("there is no user cache directory on this machine, so --store must say where to keep the copy: %v", err)
		}
		root = d
	}

	a, err := openArchive(source, stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	if err := a.requireVectorTiles(); err != nil {
		return err
	}

	// The store is created rather than opened, because fetching into one that
	// does not exist yet is the ordinary first run. Opening an existing store
	// keeps its cell zoom: it is recorded in the manifest precisely so that a
	// later change of default does not orphan everything already on disk.
	st, err := slice.Create(root, slice.Config{})
	if err != nil {
		return fmt.Errorf("opening the store at %s: %w", root, err)
	}
	src, err := a.AddTo(st, attributionOf(a, stderr))
	if err != nil {
		return err
	}

	// SourceZoom is not passed: the archive fills it in from its own header,
	// which is where that fact lives. See fetch.Archive.Plan.
	plan, err := a.Plan(context.Background(), src, acquire.Request{
		Bounds: bounds, World: f.world, MaxZoom: f.maxZoom,
		CellZoom: st.CellZoom(),
	})
	if err != nil {
		return err
	}

	writePlan(stdout, plan, root)
	if plan.Empty() {
		fmt.Fprintln(stdout, "\nnothing to fetch: the store already holds this area.")
		return nil
	}
	if f.dryRun {
		fmt.Fprintln(stdout, "\ndry run: nothing was downloaded and nothing was written.")
		return nil
	}
	if !f.yes && a.Remote() {
		ok, err := confirm(stdout, plan)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(stdout, "stopped; nothing was downloaded.")
			return nil
		}
	}

	// The per-request trace and the progress line cannot share a stream; see
	// fetch.Options.Trace. Planning keeps its trace, because planning happens
	// before there is a progress line to fight with and is the slowest silent
	// part.
	a.Silence()
	res, err := a.Fetch(context.Background(), plan, src, progressTo(stderr))
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "\n")
	writeFetchResult(stdout, res, plan, root, st)
	return nil
}

func (f fetchFlags) bounds(fs *flag.FlagSet) (slice.Bounds, error) {
	if f.world {
		return acquire.WorldBounds(), nil
	}
	if f.bbox != "" {
		return parseBBox(f.bbox)
	}
	if !flagGiven(fs, "lat") || !flagGiven(fs, "lon") {
		return slice.Bounds{}, usageErrorf("fetch needs --lat and --lon, or --bbox, or --world")
	}
	if f.radius <= 0 {
		return slice.Bounds{}, usageErrorf("--radius %g is not a distance", f.radius)
	}
	return acquire.BoundsAround(f.lat, f.lon, f.radius)
}

func flagGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(fl *flag.Flag) {
		if fl.Name == name {
			given = true
		}
	})
	return given
}

func isHelpRequest(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
	}
	return false
}

// writePlan states the cost before anything is downloaded.
//
// The byte figure is EXACT rather than estimated, which is a property of the
// format and the reason this is worth printing at all: the planner reads the
// archive's own directories, so the number below is what the download will
// weigh rather than a guess that could be out by a factor of two. A prompt that
// can say the true cost is a different thing from one that hedges.
func writePlan(w io.Writer, p *acquire.Plan, root string) {
	fmt.Fprintf(w, "%-12s %s\n", "source", p.Archive)
	fmt.Fprintf(w, "%-12s %s\n", "store", root)
	if p.Depth.World {
		fmt.Fprintf(w, "%-12s every tile on earth, zooms %d to %d\n", "area", p.Zoom.Min, p.Zoom.Max)
	} else {
		fmt.Fprintf(w, "%-12s west %.4f, south %.4f, east %.4f, north %.4f\n",
			"area", p.Bounds.West, p.Bounds.South, p.Bounds.East, p.Bounds.North)
		if p.Zoom.Empty() {
			// Shallow: every tile is above the cell grid, and the cells
			// beneath are not being filled -- which a count of "0 of N
			// cells" would say as though something had gone wrong.
			fmt.Fprintf(w, "%-12s zooms %d to %d, above the store's cell grid", "depth", p.Overview.Min, p.Overview.Max)
			if len(p.Cells) > 0 {
				fmt.Fprintf(w, "; the %d cells beneath are not filled", len(p.Cells))
			}
			fmt.Fprintln(w)
		} else {
			fmt.Fprintf(w, "%-12s %d of %d cells, zooms %d to %d\n",
				"cells", p.CellsToFetch, len(p.Cells), p.Zoom.Min, p.Zoom.Max)
		}
	}
	fetch := p.Tiles - p.Held - p.Absent
	fmt.Fprintf(w, "%-12s %d to fetch", "tiles", fetch)
	if p.Held > 0 {
		fmt.Fprintf(w, ", %d already held", p.Held)
	}
	if p.Absent > 0 {
		fmt.Fprintf(w, ", %d not in the archive", p.Absent)
	}
	fmt.Fprintln(w)

	requests := 0
	for _, g := range p.Groups {
		requests += len(g.Ranges)
	}
	fmt.Fprintf(w, "%-12s %s in %d range requests", "download", humanBytes(p.Transfer), requests)
	if p.Transfer > p.Bytes {
		fmt.Fprintf(w, " (%s of it is gaps joined to save requests)", humanBytes(p.Transfer-p.Bytes))
	}
	fmt.Fprintln(w)
}

func confirm(w io.Writer, p *acquire.Plan) (bool, error) {
	fmt.Fprintf(w, "\nThis contacts %s and downloads %s.\n", hostOf(p.Archive), humanBytes(p.Transfer))
	fmt.Fprintf(w, "It tells that host which cells you asked for, once. Rendering afterwards contacts nobody.\n")
	fmt.Fprintf(w, "Continue? [y/N] ")

	var answer string
	if _, err := fmt.Fscanln(os.Stdin, &answer); err != nil {
		// A closed or empty stdin is a no, not a crash. A pipeline that
		// reaches this prompt did not mean to download anything.
		return false, nil
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func hostOf(archive string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(archive, "https://"), "http://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return archive
	}
	return s
}

// progressTo reports each group as it lands.
//
// Per group rather than per tile, because a group is one range request and a
// tile is not an event the network knows about: a hundred and eighty tiles
// arriving in twenty requests would print a hundred and sixty lines saying
// nothing happened. The percentage is of BYTES rather than of groups, since
// groups differ in size by two orders of magnitude.
func progressTo(w io.Writer) func(acquire.Progress) {
	return func(pr acquire.Progress) {
		pct := 0.0
		if pr.PlanTransfer > 0 {
			pct = 100 * float64(pr.DoneTransfer) / float64(pr.PlanTransfer)
		}
		fmt.Fprintf(w, "\rosmbase: %s  %3.0f%%  %s of %s  ",
			pr.Label, pct, humanBytes(pr.DoneTransfer), humanBytes(pr.PlanTransfer))
	}
}

func writeFetchResult(w io.Writer, res acquire.Result, p *acquire.Plan, root string, st *slice.Store) {
	fmt.Fprintf(w, "%-12s %d tiles in %d requests, %s in %s\n",
		"fetched", res.Written, res.Requests, humanBytes(res.Transfer), res.Elapsed.Round(time.Millisecond))
	if res.Transfer != p.Transfer {
		// Worth saying out loud rather than hiding: the plan's figure is the
		// one the prompt was answered against.
		fmt.Fprintf(w, "%-12s the plan said %s\n", "", humanBytes(p.Transfer))
	}
	if n, err := st.Bytes(); err == nil {
		fmt.Fprintf(w, "%-12s %s at %s\n", "store", humanBytes(n), root)
	}
	fmt.Fprintf(w, "\nNow: osmbase render --store %s --lat %.4f --lon %.4f --out map.png\n",
		root, (p.Bounds.North+p.Bounds.South)/2, (p.Bounds.West+p.Bounds.East)/2)
	fmt.Fprintf(w, "Nothing about that reaches the network.\n")
}
