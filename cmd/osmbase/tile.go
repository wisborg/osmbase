package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
)

func tileUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `usage: osmbase tile [SOURCE] --lat LAT --lon LON [--zoom N] [--tags]

Decode the tile covering a coordinate and report what is in it: its z/x/y, its
size stored and decoded, and for each layer the feature count, the extent and
a breakdown by geometry type.

With --tags, also list each layer's attribute keys with a sample of their
values, which is how to find out what the tile schema actually offers before
writing anything against it.

examples:
  osmbase tile --lat -33.8568 --lon 151.2153
      the Sydney Opera House at the default zoom

  osmbase tile --lat 51.5081 --lon -0.0759 --zoom 15 --tags
      the Tower of London at the deepest zoom the public builds hold

  osmbase tile ./sydney.pmtiles --lat -33.8568 --lon 151.2153
      the same tile from a local archive, contacting nobody

`)
	printFlags(w, fs)
	fmt.Fprint(w, "\n"+sourceHelp)
}

func tileCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var (
		coords coordFlags
		tags   bool
	)
	fs := newFlagSet("tile", tileUsage)
	coords.bind(fs)
	fs.BoolVar(&tags, "tags", false, "list each layer's tag keys and a sample of their values")

	source, err := parseArgs(fs, args, stdout)
	if err != nil {
		return err
	}
	if err := coords.check(fs, "tile"); err != nil {
		return err
	}

	a, err := openArchive(source, stderr)
	if err != nil {
		return err
	}
	defer a.Close()
	defer a.reportTraffic(stderr)
	if err := a.requireVectorTiles(); err != nil {
		return err
	}

	loc, err := locate(a, coords)
	if err != nil {
		return err
	}

	// Two reads of the same tile, one stored and one decoded, which over a
	// remote archive is two range requests. It is deliberate and it is what
	// this command is for: the compressed and decompressed sizes together are
	// the thing a person wants to know about a tile, and the reader exposes
	// them through two methods. A command that decodes a whole tile to count
	// its features is not the place to economise on one round trip.
	if a.Remote() {
		// Said out loud because the trace is about to show the same byte
		// range fetched twice, which looks like a bug and is not.
		fmt.Fprintf(stderr, "osmbase: fetching the tile twice, stored and decoded, to report both sizes\n")
	}
	stored, ok, err := a.Reader().RawTile(loc.z, loc.x, loc.y)
	if err != nil {
		return fmt.Errorf("reading tile %d/%d/%d: %w", loc.z, loc.x, loc.y, err)
	}
	if !ok {
		return loc.absent(a)
	}
	data, ok, err := a.Reader().Tile(loc.z, loc.x, loc.y)
	if err != nil {
		return fmt.Errorf("reading tile %d/%d/%d: %w", loc.z, loc.x, loc.y, err)
	}
	if !ok {
		return loc.absent(a)
	}
	tile, err := mvt.Decode(data)
	if err != nil {
		return fmt.Errorf("decoding tile %d/%d/%d of %s: %w", loc.z, loc.x, loc.y, a.Name(), err)
	}

	writeTileReport(stdout, loc, a, tile, len(stored), len(data))
	if tags {
		writeTagReport(stdout, tile)
	}
	return nil
}

// location is the tile a coordinate landed in, kept together with the
// coordinate so that an error can name both.
type location struct {
	lat, lon float64
	z        uint8
	x, y     uint32
}

// locate turns the flags into a tile, refusing a zoom the archive does not
// hold before any tile is looked for.
//
// The zoom is checked against the header rather than left to come back as "no
// tile there", because the two failures have different answers: a zoom of 18
// against a build that stops at 15 is not a place with no data, it is a
// question this archive cannot be asked. See docs/architecture.md, trap T4.
func locate(a *archive, c coordFlags) (location, error) {
	h := a.Reader().Header()
	z := uint8(c.zoom)
	if z > h.MaxZoom || z < h.MinZoom {
		return location{}, usageErrorf("--zoom %d is outside the zoom levels %s holds, which are %d to %d",
			c.zoom, a.Name(), h.MinZoom, h.MaxZoom)
	}
	x, y, err := mercator.TileAt(z, c.lon, c.lat)
	if err != nil {
		return location{}, usageError{msg: err.Error()}
	}
	return location{lat: c.lat, lon: c.lon, z: z, x: x, y: y}, nil
}

// absent explains a coordinate the archive holds no tile for.
//
// A missing tile is an answer rather than a failure -- an archive legitimately
// omits ocean, and a regional extract holds one region -- so the message says
// where the archive does have data instead of merely reporting a false.
func (l location) absent(a *archive) error {
	h := a.Reader().Header()
	msg := fmt.Sprintf("%s holds no tile at %d/%d/%d, which is where latitude %s, longitude %s falls at zoom %d",
		a.Name(), l.z, l.x, l.y, formatCoord(l.lat), formatCoord(l.lon), l.z)
	// An archive that declares no bounds leaves all four at zero, which is a
	// point in the Gulf of Guinea rather than a rectangle. Saying "its bounds
	// are 0, 0, 0, 0" would send someone looking in the wrong place.
	if h.MinLon != 0 || h.MinLat != 0 || h.MaxLon != 0 || h.MaxLat != 0 {
		msg += fmt.Sprintf("; it covers west %s, south %s, east %s, north %s at zooms %d to %d",
			formatCoord(h.MinLon), formatCoord(h.MinLat), formatCoord(h.MaxLon), formatCoord(h.MaxLat),
			h.MinZoom, h.MaxZoom)
	}
	return fmt.Errorf("%s. Try a shallower --zoom, or a coordinate inside that area", msg)
}

func writeTileReport(w io.Writer, loc location, a *archive, tile mvt.Tile, stored, decoded int) {
	h := a.Reader().Header()
	var head table
	head.row("source", a.Name())
	head.row("coordinate", fmt.Sprintf("latitude %s, longitude %s", formatCoord(loc.lat), formatCoord(loc.lon)))
	head.row("tile", fmt.Sprintf("%d/%d/%d", loc.z, loc.x, loc.y))
	if west, south, east, north, err := mercator.TileBounds(loc.z, loc.x, loc.y); err == nil {
		head.row("tile covers", fmt.Sprintf("west %s  south %s  east %s  north %s",
			formatCoord(west), formatCoord(south), formatCoord(east), formatCoord(north)))
	}
	head.row("tile size", fmt.Sprintf("%s stored as %s, %s decoded",
		humanBytes(int64(stored)), h.TileCompression, humanBytes(int64(decoded))))
	head.row("layers", fmt.Sprintf("%d", len(tile.Layers)))
	head.write(w)

	if len(tile.Layers) == 0 {
		// A tile with no layers decodes from zero bytes as readily as from a
		// real one, so this says what was there rather than leaving a blank
		// table to be read as a crash.
		fmt.Fprintf(w, "\nThe tile has no layers at all: %d decoded bytes carrying nothing.\n", decoded)
		return
	}

	fmt.Fprintln(w)
	var t table
	// Everything but the layer name is a count, and counts read as a column
	// when their digits line up.
	t.rightAlign(1, 2, 3, 4, 5, 6, 7)
	t.row("LAYER", "FEATURES", "EXTENT", "POINTS", "LINES", "POLYGONS", "UNKNOWN", "VERTICES")
	var total counts
	for _, l := range tile.Layers {
		c := countLayer(l)
		total.add(c)
		t.row(l.Name,
			strconv.Itoa(len(l.Features)), strconv.FormatUint(uint64(l.Extent), 10),
			strconv.Itoa(c.points), strconv.Itoa(c.lines), strconv.Itoa(c.polygons),
			strconv.Itoa(c.unknown), strconv.Itoa(c.vertices))
	}
	t.row("(total)", strconv.Itoa(total.features), "",
		strconv.Itoa(total.points), strconv.Itoa(total.lines), strconv.Itoa(total.polygons),
		strconv.Itoa(total.unknown), strconv.Itoa(total.vertices))
	t.write(w)
}

// counts is the geometry breakdown of a layer.
type counts struct {
	features                         int
	points, lines, polygons, unknown int
	vertices                         int
}

func (c *counts) add(o counts) {
	c.features += o.features
	c.points += o.points
	c.lines += o.lines
	c.polygons += o.polygons
	c.unknown += o.unknown
	c.vertices += o.vertices
}

// countLayer counts a layer by geometry type.
//
// The count is of FEATURES by type, and the vertex count is separate, because
// the two answer different questions: how much is here, and how much work it
// will be to draw. A layer of 40 features and 300,000 vertices is a coastline
// and a layer of 4,000 features and 16,000 vertices is a road network.
func countLayer(l mvt.Layer) counts {
	c := counts{features: len(l.Features)}
	for _, f := range l.Features {
		switch f.Type {
		case mvt.GeomPoint:
			c.points++
		case mvt.GeomLineString:
			c.lines++
		case mvt.GeomPolygon:
			c.polygons++
		default:
			c.unknown++
		}
		c.vertices += countVertices(f.Geometry)
	}
	return c
}

func countVertices(g mvt.Geometry) int {
	n := len(g.Points)
	for _, line := range g.Lines {
		n += len(line)
	}
	for _, p := range g.Polygons {
		n += len(p.Exterior)
		for _, hole := range p.Holes {
			n += len(hole)
		}
	}
	return n
}

// maxSampleValues is how many distinct values --tags shows per key. Enough to
// see what a key is for; short enough that a "name" key does not print the
// street directory.
const maxSampleValues = 6

func writeTagReport(w io.Writer, tile mvt.Tile) {
	for _, l := range tile.Layers {
		fmt.Fprintf(w, "\nlayer %q, %d features\n", l.Name, len(l.Features))
		keys, samples := sampleTags(l)
		if len(keys) == 0 {
			fmt.Fprintln(w, "  (no tags on any feature)")
			continue
		}
		var t table
		for _, k := range keys {
			s := samples[k]
			values := joinValues(s.values)
			if s.distinct > len(s.values) {
				values += fmt.Sprintf(" (+%d more)", s.distinct-len(s.values))
			}
			t.row("  "+k, s.kind, fmt.Sprintf("%d/%d features", s.features, len(l.Features)), values)
		}
		t.write(w)
	}
}

// tagSample is what --tags shows for one key.
type tagSample struct {
	kind     string
	features int
	distinct int
	values   []string
	seen     map[string]bool
}

// sampleTags walks a layer's features and collects, per key, how many features
// carry it and a few of its values.
//
// The keys come back sorted and the values in first-appearance order over the
// features, so the same tile always prints the same report. Ranging over a
// feature's tag map would put the runtime's map ordering into the output, and
// a diagnostic whose lines move between runs cannot be diffed.
//
// A key whose values are not all of one kind is reported as "mixed" rather
// than as whichever kind happened to come first, because a schema that encodes
// a value two ways is something to notice.
func sampleTags(l mvt.Layer) ([]string, map[string]*tagSample) {
	samples := map[string]*tagSample{}
	for _, f := range l.Features {
		// Sorted so that first appearance is defined by the FEATURE order and
		// not by the map order within one feature.
		keys := make([]string, 0, len(f.Tags))
		for k := range f.Tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := f.Tags[k]
			s := samples[k]
			if s == nil {
				s = &tagSample{kind: v.Kind.String(), seen: map[string]bool{}}
				samples[k] = s
			}
			s.features++
			if s.kind != v.Kind.String() {
				s.kind = "mixed"
			}
			text := v.String()
			if !s.seen[text] {
				s.seen[text] = true
				s.distinct++
				if len(s.values) < maxSampleValues {
					s.values = append(s.values, text)
				}
			}
		}
	}
	keys := make([]string, 0, len(samples))
	for k := range samples {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, samples
}

func joinValues(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}
