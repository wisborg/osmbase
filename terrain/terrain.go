// Package terrain copies elevation onto this machine, and opens it for
// drawing: the shape of the ground, for a map that shows hills as well as
// streets.
//
// A program that draws maps uses four things from it, in this order: Root,
// where the terrain for a map store is kept; Measure, what a view lacks,
// read from the disk before anything is asked of anyone; Locate and Fill,
// after a yes, to fetch it; and Open, for the heights and the credit a
// render needs. osmbase's own command is built on the same four, so every
// program sharing a store keeps its terrain in one place and credits it one
// way.
//
// OpenStreetMap has no elevation surface, so terrain comes from a digital
// elevation model of its own, distributed as tiles of elevation encoded in
// the colours of images. The layout this package reads is Mapterhorn's (see
// docs/elevation.md for why that source): a global archive of the shallow
// zooms, regional archives of the deep ones where finer data exists, each
// covering one tile at a coarse zoom, and an archive of vector polygons
// saying which source covers where, for the credit a map owes.
//
// # A store of its own
//
// Terrain is kept in a store beside the map's, never inside it. Every
// command that draws a map picks the store's one source, and refuses when
// there are several without being told which; and the size of a store is
// everything under its root, which eviction holds to a budget. Terrain in
// the map's store would make every render ambiguous and push map cells out
// of the map's budget -- in every program built on this library, released
// ones included, which cannot be taught otherwise. A store of its own is an
// ordinary store to all of them: a directory they never look in.
//
// Within that store the elevation is one source however many archives it
// came from, since a cell's shallow zooms come from the global archive and
// its deep ones from its region's; and the coverage is a second, vector,
// source.
//
// # No default host
//
// As for the map, the library names no host: a Layout says where the
// archives are, and the application that builds one is where a default
// belongs. A Layout can as well be a directory of archives already on disk,
// from which nothing is requested from anyone.
package terrain

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wisborg/osmbase/acquire"
	"github.com/wisborg/osmbase/fetch"
	"github.com/wisborg/osmbase/slice"
)

// Layout is where a terrain source's archives are.
type Layout struct {
	// Name is what the terrain store calls the elevation source: the base
	// the archives were found at, so that two hosts of the same data are
	// two sources and one host fetched twice is one.
	Name string

	// Global is the archive of the shallow zooms everywhere, a URL or a
	// path.
	Global string

	// Coverage is the archive of polygons naming each area's source, or
	// empty for a layout without one, whose maps can credit only the
	// layout as a whole.
	Coverage string

	// Sources is the list of who made each source the coverage names, and
	// under what licence -- Mapterhorn's attribution.json -- or empty for a
	// layout without one. A fetch keeps a copy in the terrain store, so a
	// map drawn from it offline can say whom it owes.
	Sources string

	// Regions are the archives of the deep zooms, each covering one tile.
	Regions []Region
}

// Region is an archive of the deep zooms over one tile's area.
type Region struct {
	// Tile is the area, one tile at a coarse zoom: Mapterhorn's are at
	// zoom 6, a few hundred kilometres across.
	Tile slice.TileRef
	// Source is the archive, a URL or a path.
	Source string
}

// regionFor is the region whose area holds the cell c of a store keyed at
// cellZoom, if the layout has one.
//
// A cell is always inside exactly one tile at any shallower zoom, so a cell
// is never split between two regions, and its deep zooms come from one
// archive.
func (l Layout) regionFor(c slice.Cell, cellZoom uint8) (Region, bool) {
	for _, r := range l.Regions {
		if r.Tile.Z > cellZoom {
			continue
		}
		shift := cellZoom - r.Tile.Z
		if c.X>>shift == r.Tile.X && c.Y>>shift == r.Tile.Y {
			return r, true
		}
	}
	return Region{}, false
}

// regionName is how Mapterhorn names a regional archive: its tile, as
// z-x-y.pmtiles.
var regionName = regexp.MustCompile(`^(\d+)-(\d+)-(\d+)\.pmtiles$`)

// region reads a regional archive's tile from its file name.
func region(name, source string) (Region, bool) {
	m := regionName.FindStringSubmatch(name)
	if m == nil {
		return Region{}, false
	}
	z, err1 := strconv.ParseUint(m[1], 10, 8)
	x, err2 := strconv.ParseUint(m[2], 10, 32)
	y, err3 := strconv.ParseUint(m[3], 10, 32)
	if err1 != nil || err2 != nil || err3 != nil || x >= 1<<z || y >= 1<<z {
		return Region{}, false
	}
	return Region{Tile: slice.TileRef{Z: uint8(z), X: uint32(x), Y: uint32(y)}, Source: source}, true
}

// The names of the global and the coverage archives, and of the index of
// every archive, in a Mapterhorn layout.
const (
	globalName   = "planet.pmtiles"
	coverageName = "coverage.pmtiles"
	// SourcesName is the list of sources, on the host, in a directory of
	// archives, and in the terrain store.
	SourcesName = "attribution.json"
	// IndexName is the file listing a hosted layout's archives.
	IndexName = "download_urls.json"
)

// ReadIndex reads a hosted layout from its index -- the list of archives a
// Mapterhorn host publishes as download_urls.json -- found at base. The
// global archive and the regions come from the list; the coverage archive,
// which it does not list, is taken to sit beside it.
func ReadIndex(base string, r io.Reader) (Layout, error) {
	var idx struct {
		Items []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"items"`
	}
	if err := json.NewDecoder(r).Decode(&idx); err != nil {
		return Layout{}, fmt.Errorf("terrain: reading the index of %s: %w", base, err)
	}
	l := Layout{Name: base, Coverage: join(base, coverageName), Sources: join(base, SourcesName)}
	for _, it := range idx.Items {
		url := it.URL
		if url == "" {
			url = join(base, it.Name)
		}
		if it.Name == globalName {
			l.Global = url
			continue
		}
		if r, ok := region(it.Name, url); ok {
			l.Regions = append(l.Regions, r)
		}
	}
	if l.Global == "" {
		return Layout{}, fmt.Errorf("terrain: the index of %s lists no %s, the archive every terrain fetch starts from", base, globalName)
	}
	sortRegions(l.Regions)
	return l, nil
}

// ReadDir reads a layout from a directory holding its archives: a global
// archive, the regions there are files for, and a coverage archive if there
// is one. This is the way to use terrain obtained some other way -- copied
// from a mirror, extracted with the pmtiles tool -- and nothing is requested
// from anyone.
func ReadDir(dir string) (Layout, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Layout{}, fmt.Errorf("terrain: reading %s: %w", dir, err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Layout{}, err
	}
	l := Layout{Name: abs}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(abs, e.Name())
		switch e.Name() {
		case globalName:
			l.Global = path
		case coverageName:
			l.Coverage = path
		case SourcesName:
			l.Sources = path
		default:
			if r, ok := region(e.Name(), path); ok {
				l.Regions = append(l.Regions, r)
			}
		}
	}
	if l.Global == "" {
		return Layout{}, fmt.Errorf("terrain: %s holds no %s, the archive every terrain fetch starts from", dir, globalName)
	}
	sortRegions(l.Regions)
	return l, nil
}

func sortRegions(rs []Region) {
	slices.SortFunc(rs, func(a, b Region) int {
		return cmp.Or(cmp.Compare(a.Tile.Z, b.Tile.Z), cmp.Compare(a.Tile.X, b.Tile.X), cmp.Compare(a.Tile.Y, b.Tile.Y))
	})
}

// join is base and name, a URL or a path.
func join(base, name string) string {
	if strings.Contains(base, "://") {
		return strings.TrimSuffix(base, "/") + "/" + name
	}
	return filepath.Join(base, name)
}

// Opener opens an archive by URL or path; fetch.Open, with the options the
// application wants, in practice.
type Opener func(source string) (*fetch.Archive, error)

// Plan is a terrain fetch, worked out and not yet carried out: what each
// archive would supply and what it would cost.
type Plan struct {
	// Global is what the global archive supplies: the overview, and the
	// cells' shallow zooms.
	Global *acquire.Plan
	// Regions are what the regional archives supply, one plan each, for the
	// cells inside them.
	Regions []RegionPlan
	// Coverage is what the coverage archive supplies, or nil for a layout
	// without one.
	Coverage *acquire.Plan

	// Sources is where the list of sources is copied from, when the store
	// has none yet; see Layout.Sources. Empty when there is nothing to copy.
	Sources string

	elevation, coverage *slice.Source
	global, cover       *fetch.Archive
	archives            []*fetch.Archive
	sourcesTo           string
}

// RegionPlan is one regional archive's part of a terrain fetch.
type RegionPlan struct {
	Region Region
	Plan   *acquire.Plan

	archive *fetch.Archive
}

// Totals are a plan's figures, every archive's together: as for an
// acquire.Plan, Tiles is what will be written, Held what is on disk already
// and Absent what the archives do not hold.
type Totals struct {
	Tiles, Held, Absent int
	Bytes, Transfer     int64
	Requests            int
}

// Totals adds up every archive's plan. The list of sources, when there is
// one to copy, is not in it: it is one small file read whole rather than
// tiles read by range, and its size is not known until it is read.
func (p *Plan) Totals() Totals {
	var t Totals
	for _, ap := range p.plans() {
		t.Tiles += ap.Tiles
		t.Held += ap.Held
		t.Absent += ap.Absent
		t.Bytes += ap.Bytes
		t.Transfer += ap.Transfer
		t.Requests += ap.Requests
	}
	return t
}

// Empty reports whether there is nothing to fetch.
func (p *Plan) Empty() bool { return p.Totals().Tiles == 0 && p.Sources == "" }

// Remote reports whether the plan reads from any host at all, as against
// files on disk.
func (p *Plan) Remote() bool {
	for _, a := range p.archives {
		if a.Remote() {
			return true
		}
	}
	return false
}

func (p *Plan) plans() []*acquire.Plan {
	out := []*acquire.Plan{p.Global}
	for _, r := range p.Regions {
		out = append(out, r.Plan)
	}
	if p.Coverage != nil {
		out = append(out, p.Coverage)
	}
	return out
}

// Close closes every archive the plan opened.
func (p *Plan) Close() error {
	var errs []error
	for _, a := range p.archives {
		errs = append(errs, a.Close())
	}
	return errors.Join(errs...)
}

// Silence turns off every archive's per-request trace, before a progress
// line is drawn; see fetch.Options.Trace.
func (p *Plan) Silence() {
	for _, a := range p.archives {
		a.Silence()
	}
}

// Prepare works out a terrain fetch of req's area from the archives l names,
// into the terrain store st, registering the elevation and coverage sources
// there and reading the archives' directories but no tile data.
//
// The depth is a zoom shallower than a map of the same area would fetch --
// the one req asks for, or would choose: terrain tiles are 512 pixels
// across, so a tile at zoom z is as fine as a 256-pixel map tile at z+1. Each archive is then
// asked for no deeper than it goes, and a region is not opened at all unless
// the depth reaches below the global archive -- nor unless a cell of the
// area is inside it.
func Prepare(ctx context.Context, l Layout, st *slice.Store, req acquire.Request, open Opener) (_ *Plan, err error) {
	p := &Plan{}
	defer func() {
		if err != nil {
			p.Close()
		}
	}()
	if p.global, err = p.open(open, l.Global); err != nil {
		return nil, err
	}
	gh := p.global.Reader().Header()
	if t := gh.TileType.String(); t != "webp" && t != "png" {
		return nil, fmt.Errorf("terrain: the global archive %s holds %s tiles, and terrain is images of elevation (webp or png)", l.Global, t)
	}

	want := req.MaxZoom
	switch {
	case want != acquire.AutoZoom:
		want = max(0, want-1)
	case req.World:
		want = acquire.WorldMaxZoom - 1
	default:
		d, err := acquire.DepthFor(req.Bounds, slice.MaxCellZoom)
		if err != nil {
			return nil, err
		}
		want = max(0, int(d.Max)-1)
	}

	// Which regions the area's cells are in, before anything is planned,
	// so the elevation source's zoom range can say how deep it goes.
	type need struct {
		region Region
		cells  []slice.Cell
	}
	var needs []need
	if !req.World && want > int(gh.MaxZoom) {
		cells, err := slice.CellsForZoom(req.Bounds, req.CellZoom)
		if err != nil {
			return nil, err
		}
		byRegion := map[slice.TileRef]int{}
		for _, c := range cells {
			r, ok := l.regionFor(c, req.CellZoom)
			if !ok {
				continue
			}
			i, seen := byRegion[r.Tile]
			if !seen {
				i = len(needs)
				byRegion[r.Tile] = i
				needs = append(needs, need{region: r})
			}
			needs[i].cells = append(needs[i].cells, c)
		}
	}
	zoom := slice.ZoomRange{Min: gh.MinZoom, Max: gh.MaxZoom}
	for _, n := range needs {
		a, err := p.open(open, n.region.Source)
		if err != nil {
			return nil, err
		}
		h := a.Reader().Header()
		if h.TileType != gh.TileType || h.TileCompression != gh.TileCompression {
			return nil, fmt.Errorf("terrain: the regional archive %s holds %s tiles compressed %s, where the global archive's are %s compressed %s; one source cannot hold both",
				n.region.Source, h.TileType, h.TileCompression, gh.TileType, gh.TileCompression)
		}
		zoom.Max = max(zoom.Max, h.MaxZoom)
		p.Regions = append(p.Regions, RegionPlan{Region: n.region, archive: a})
	}

	if p.elevation, err = elevationSource(st, l, p.global, zoom); err != nil {
		return nil, err
	}

	g := req
	g.MaxZoom = min(want, int(gh.MaxZoom))
	if p.Global, err = p.global.Plan(ctx, p.elevation, g); err != nil {
		return nil, err
	}

	for i := range p.Regions {
		rp := &p.Regions[i]
		h := rp.archive.Reader().Header()
		r := req
		r.Bounds = intersect(req.Bounds, tileBounds(rp.Region.Tile))
		r.MaxZoom = min(want, int(h.MaxZoom))
		if r.MaxZoom < int(h.MinZoom) {
			continue
		}
		if rp.Plan, err = rp.archive.Plan(ctx, p.elevation, r); err != nil {
			return nil, err
		}
	}
	p.Regions = slices.DeleteFunc(p.Regions, func(rp RegionPlan) bool { return rp.Plan == nil })

	if l.Coverage != "" {
		if p.cover, err = p.open(open, l.Coverage); err != nil {
			return nil, err
		}
		if p.coverage, err = p.cover.AddTo(st, ""); err != nil {
			return nil, err
		}
		c := req
		c.MaxZoom = min(want, int(p.cover.Reader().Header().MaxZoom))
		if p.Coverage, err = p.cover.Plan(ctx, p.coverage, c); err != nil {
			return nil, err
		}
	}
	// The list of sources is copied once, into the store's root, where no
	// command listing the store's sources looks: they are directories.
	p.sourcesTo = filepath.Join(st.Root(), SourcesName)
	if l.Sources != "" {
		if _, err := os.Stat(p.sourcesTo); errors.Is(err, os.ErrNotExist) {
			p.Sources = l.Sources
		}
	}
	return p, nil
}

func (p *Plan) open(open Opener, source string) (*fetch.Archive, error) {
	a, err := open(source)
	if err != nil {
		return nil, fmt.Errorf("terrain: opening %s: %w", source, err)
	}
	p.archives = append(p.archives, a)
	return a, nil
}

// elevationSource registers the elevation as one source of st, named for the
// layout rather than for any one archive, with the zoom range it has reached:
// what it held already, widened by zoom.
func elevationSource(st *slice.Store, l Layout, global *fetch.Archive, zoom slice.ZoomRange) (*slice.Source, error) {
	id := slice.SourceID(l.Name, "")
	if sources, err := st.Sources(); err == nil {
		for _, m := range sources {
			if m.ID == id && !m.SourceZoom.Empty() {
				zoom = slice.ZoomRange{Min: min(zoom.Min, m.SourceZoom.Min), Max: max(zoom.Max, m.SourceZoom.Max)}
			}
		}
	}
	h := global.Reader().Header()
	attribution, _ := global.Attribution()
	src, err := st.AddSource(slice.SourceDesc{
		Source:          l.Name,
		Attribution:     attribution,
		TileType:        h.TileType.String(),
		TileCompression: slice.Compression(h.TileCompression.String()),
		SourceZoom:      zoom,
	})
	if err != nil {
		return nil, fmt.Errorf("terrain: recording %s as the terrain store's elevation: %w", l.Name, err)
	}
	return src, nil
}

// Result is what a terrain fetch did, every archive's together.
type Result struct {
	acquire.Result
}

// Fetch carries out the plan: the global archive first, so that every cell
// has its shallow zooms before its region adds the deep ones, then each
// region, then the coverage. progress may be nil; see fetch.Archive.Fetch.
// Its DoneTransfer and PlanTransfer are the whole plan's, every archive's
// together, so a percentage climbs once from nothing to all of it rather
// than starting again at each archive.
func (p *Plan) Fetch(ctx context.Context, progress func(acquire.Progress)) (Result, error) {
	var res Result
	if progress != nil {
		total, inner := p.Totals().Transfer, progress
		progress = func(pr acquire.Progress) {
			pr.DoneTransfer += res.Transfer
			pr.PlanTransfer = total
			inner(pr)
		}
	}
	add := func(r acquire.Result) {
		res.Written += r.Written
		res.Held += r.Held
		res.Absent += r.Absent
		res.Bytes += r.Bytes
		res.Requests += r.Requests
		res.Transfer += r.Transfer
		res.Elapsed += r.Elapsed
	}
	r, err := p.global.Fetch(ctx, p.Global, p.elevation, progress)
	if err != nil {
		return res, err
	}
	add(r)
	for _, rp := range p.Regions {
		r, err := rp.archive.Fetch(ctx, rp.Plan, p.elevation, progress)
		if err != nil {
			return res, err
		}
		add(r)
	}
	if p.Coverage != nil {
		r, err := p.cover.Fetch(ctx, p.Coverage, p.coverage, progress)
		if err != nil {
			return res, err
		}
		add(r)
	}
	if p.Sources != "" {
		n, err := p.copySources(ctx)
		if err != nil {
			return res, err
		}
		res.Bytes += n
		if strings.Contains(p.Sources, "://") {
			res.Requests++
			res.Transfer += n
		}
	}
	return res, nil
}

// sourcesLimit bounds the list of sources this reads: Mapterhorn's names a
// hundred and fifty sources in under a hundred kilobytes.
const sourcesLimit = 4 << 20

// copySources copies the list of sources into the store, checking first
// that it is a list: a page of HTML saved under its name would leave every
// later map uncredited without a word.
func (p *Plan) copySources(ctx context.Context) (int64, error) {
	var b bytes.Buffer
	if strings.Contains(p.Sources, "://") {
		if _, err := acquire.DownloadContext(ctx, p.Sources, &b, sourcesLimit, time.Minute); err != nil {
			return 0, fmt.Errorf("terrain: reading the list of sources: %w", err)
		}
	} else {
		data, err := os.ReadFile(p.Sources)
		if err != nil {
			return 0, fmt.Errorf("terrain: reading the list of sources: %w", err)
		}
		b.Write(data)
	}
	var list []struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(b.Bytes(), &list); err != nil || len(list) == 0 || list[0].Source == "" {
		return 0, fmt.Errorf("terrain: %s is not a list of elevation sources", p.Sources)
	}
	tmp := p.sourcesTo + ".partial"
	if err := os.WriteFile(tmp, b.Bytes(), 0o644); err != nil {
		return 0, fmt.Errorf("terrain: writing the list of sources: %w", err)
	}
	if err := os.Rename(tmp, p.sourcesTo); err != nil {
		os.Remove(tmp)
		return 0, fmt.Errorf("terrain: writing the list of sources: %w", err)
	}
	return int64(b.Len()), nil
}

// tileBounds is the area of a tile, in degrees.
func tileBounds(t slice.TileRef) slice.Bounds {
	n := math.Exp2(float64(t.Z))
	lon := func(x float64) float64 { return x/n*360 - 180 }
	lat := func(y float64) float64 { return 180 / math.Pi * math.Atan(math.Sinh(math.Pi*(1-2*y/n))) }
	return slice.Bounds{West: lon(float64(t.X)), East: lon(float64(t.X + 1)), North: lat(float64(t.Y)), South: lat(float64(t.Y + 1))}
}

// intersect is the overlap of two areas, which a caller has made sure is not
// empty.
func intersect(a, b slice.Bounds) slice.Bounds {
	return slice.Bounds{West: max(a.West, b.West), South: max(a.South, b.South), East: min(a.East, b.East), North: min(a.North, b.North)}
}
