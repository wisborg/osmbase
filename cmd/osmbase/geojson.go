package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
)

func geojsonUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `usage: osmbase geojson [SOURCE] --lat LAT --lon LON [--zoom N] [--layer NAME]

Write the tile covering a coordinate to stdout as a GeoJSON FeatureCollection,
with each feature's tags as its properties and the layer it came from as
"@layer". Coordinates are longitude then latitude in WGS84 degrees, which is
what geojson.io, QGIS and every other GeoJSON consumer expects.

Paste the output into https://geojson.io to look at it. One whole tile at zoom
14 is a few megabytes of JSON and tens of thousands of features, so --layer is
usually what you want.

examples:
  osmbase geojson --lat -33.8568 --lon 151.2153 --layer roads > roads.geojson
      the road network around the Sydney Opera House

  osmbase geojson --lat 51.5081 --lon -0.0759 --zoom 15 --layer buildings
      building footprints by the Tower of London, to stdout

  osmbase geojson ./sydney.pmtiles --lat -33.8568 --lon 151.2153
      every layer of the tile, from a local archive, contacting nobody

Run "osmbase tile --lat ... --lon ... --tags" first to see which layers and
tags the archive actually holds there.

`)
	printFlags(w, fs)
	fmt.Fprint(w, "\n"+sourceHelp)
}

func geojsonCommand(args []string, stdout, stderr io.Writer) error {
	var (
		coords coordFlags
		layer  string
	)
	fs := newFlagSet("geojson", geojsonUsage)
	coords.bind(fs)
	fs.StringVar(&layer, "layer", "", "write only this layer instead of every layer in the tile")

	source, err := parseArgs(fs, args, stdout)
	if err != nil {
		return err
	}
	if err := coords.check(fs, "geojson"); err != nil {
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
	data, ok, err := a.Tile(loc.z, loc.x, loc.y)
	if err != nil {
		return fmt.Errorf("reading tile %d/%d/%d: %w", loc.z, loc.x, loc.y, err)
	}
	if !ok {
		return loc.absent(a)
	}
	tile, err := mvt.Decode(data)
	if err != nil {
		return fmt.Errorf("decoding tile %d/%d/%d of %s: %w", loc.z, loc.x, loc.y, a.name, err)
	}

	layers, err := chooseLayers(tile, layer)
	if err != nil {
		return err
	}

	fmt.Fprintf(stderr, "osmbase: tile %d/%d/%d, %s decoded\n", loc.z, loc.x, loc.y, humanBytes(int64(len(data))))
	written, skipped, err := writeFeatureCollection(stdout, layers, loc.z, loc.x, loc.y)
	if err != nil {
		return fmt.Errorf("writing GeoJSON for tile %d/%d/%d: %w", loc.z, loc.x, loc.y, err)
	}
	fmt.Fprintf(stderr, "osmbase: %d features written from %d layers\n", written, len(layers))
	if skipped > 0 {
		// Said out loud rather than dropped quietly: a feature that does not
		// reach the output is a difference between what the archive holds and
		// what the file shows, and a silent one would be discovered as a
		// missing building.
		fmt.Fprintf(stderr, "osmbase: %d features skipped, having no geometry GeoJSON can express\n", skipped)
	}
	if written == 0 {
		fmt.Fprintf(stderr, "osmbase: the collection is empty, which is a tile with nothing in the layers asked for\n")
	}
	return nil
}

// chooseLayers applies --layer, refusing a name the tile does not have.
//
// The error lists the layers that ARE there, because the alternative is a
// valid empty FeatureCollection for a typo, and an empty map looks exactly
// like an area with no data.
func chooseLayers(tile mvt.Tile, name string) ([]mvt.Layer, error) {
	if name == "" {
		return tile.Layers, nil
	}
	if l, ok := tile.Layer(name); ok {
		return []mvt.Layer{l}, nil
	}
	names := make([]string, 0, len(tile.Layers))
	for _, l := range tile.Layers {
		names = append(names, l.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("this tile has no layers at all, so it has no layer %q", name)
	}
	sort.Strings(names)
	return nil, usageErrorf("this tile has no layer %q; it has %s", name, strings.Join(names, ", "))
}

// writeFeatureCollection writes the layers as one GeoJSON FeatureCollection.
//
// Output is streamed, one feature per line, rather than built as a value and
// marshalled. A zoom 14 tile holds tens of thousands of features and several
// megabytes of JSON, and one feature per line is what makes the result
// greppable, diffable and readable in a pager -- all of which a person
// exploring an unfamiliar schema will want.
//
// # The conversion, and the flip that is not in it
//
// Tile coordinates are integers against the LAYER's extent, so the transform
// is rebuilt per layer: a tile whose landcover layer is at extent 512 and
// whose building layer is at 4096 needs two, and using one for both would draw
// a layer at an eighth of its size in the right place.
//
// Tile y and Web Mercator y both run south, so no axis is flipped on the way
// through; the turn to northward latitude happens inside the projection. See
// mercator.Project.
//
// # Ring order, which the two formats disagree about
//
// Every ring is written in REVERSE, and that is not an accident of the
// conversion. The two formats want opposite windings:
//
//   - MVT wants an exterior ring with positive area by the surveyor's formula
//     in tile space, where y runs DOWN. Drawn on a north-up map, that ring is
//     clockwise.
//   - RFC 7946 wants an exterior ring counterclockwise by the right-hand rule
//     in lon/lat, where latitude runs UP. Drawn on the same map, that ring is
//     counterclockwise.
//
// So the same points satisfy one rule or the other, never both. The confusion
// worth heading off is that the sign of the area and the visual direction are
// not the same claim: latitude runs opposite to tile y, so converting a ring
// to degrees NEGATES its signed area while leaving the picture exactly as it
// was. A reader who checks only the sign will conclude the windings already
// agree, and a reader who checks only the picture will conclude they cannot be
// fixed by reversing. Both are wrong; reversing the point order is what turns
// one convention into the other.
//
// It is worth doing even though RFC 7946 tells parsers not to reject a
// polygon that gets this wrong, and even though geojson.io draws either.
// Something that cares -- a strict validator, an area computation, a tool that
// decides which side is inside -- will otherwise read every lake as a hole in
// nothing.
func writeFeatureCollection(w io.Writer, layers []mvt.Layer, z uint8, x, y uint32) (written, skipped int, err error) {
	bw := bufio.NewWriter(w)
	fw := &featureWriter{w: bw}

	fw.write(`{"type":"FeatureCollection","features":[`)
	for _, l := range layers {
		extent := l.Extent
		if extent == 0 {
			extent = mvt.DefaultExtent
		}
		tr, err := mercator.NewTileTransform(z, x, y, extent)
		if err != nil {
			return fw.written, fw.skipped, fmt.Errorf("layer %q: %w", l.Name, err)
		}
		for _, f := range l.Features {
			fw.feature(l.Name, f, tr)
		}
	}
	if fw.written > 0 {
		fw.write("\n")
	}
	fw.write("]}\n")
	if fw.err != nil {
		return fw.written, fw.skipped, fw.err
	}
	return fw.written, fw.skipped, bw.Flush()
}

// featureWriter streams features and remembers the first write error, so that
// the geometry code below reads as a description of the format rather than as
// error handling.
type featureWriter struct {
	w       *bufio.Writer
	err     error
	written int
	skipped int
}

func (f *featureWriter) write(s string) {
	if f.err != nil {
		return
	}
	_, f.err = f.w.WriteString(s)
}

// feature writes one feature, or counts it as skipped.
func (f *featureWriter) feature(layer string, feat mvt.Feature, tr mercator.TileTransform) {
	geometry, ok := geometryJSON(feat, tr)
	if !ok {
		f.skipped++
		return
	}
	if f.written == 0 {
		f.write("\n")
	} else {
		f.write(",\n")
	}
	f.write(`{"type":"Feature",`)
	if feat.HasID {
		// The RFC's own place for an identifier. It is repeated in the
		// properties below because geojson.io and most viewers show a
		// feature's properties and not its id.
		f.write(`"id":` + strconv.FormatUint(feat.ID, 10) + `,`)
	}
	f.write(`"properties":` + propertiesJSON(layer, feat) + `,`)
	f.write(`"geometry":` + geometry + "}")
	f.written++
}

// propertiesJSON renders a feature's tags, with the layer it came from.
//
// "@layer" is prefixed rather than plain because a tile schema is free to have
// a tag called "layer" -- OSM does -- and a synthetic property that silently
// overwrote a real one would be a lie in the output. Keys are sorted so that
// the same tile produces the same bytes every time: ranging over the tag map
// would put Go's randomised map order into the file and make two runs differ.
func propertiesJSON(layer string, f mvt.Feature) string {
	var b strings.Builder
	b.WriteString(`{"@layer":`)
	b.Write(jsonString(layer))
	if f.HasID {
		b.WriteString(`,"@id":`)
		b.WriteString(strconv.FormatUint(f.ID, 10))
	}
	keys := make([]string, 0, len(f.Tags))
	for k := range f.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(",")
		b.Write(jsonString(k))
		b.WriteString(":")
		b.WriteString(valueJSON(f.Tags[k]))
	}
	b.WriteString("}")
	return b.String()
}

// valueJSON renders one attribute value.
//
// The numeric kinds are kept apart rather than folded into a float64: a uint64
// above 2^53 does not survive that trip, and JSON numbers are not bounded by
// what a float can hold. A NaN or an infinity is written as a STRING, because
// JSON has no spelling for either -- dropping the property would hide a value
// the tile really does carry, and writing a bare NaN would produce a file no
// parser accepts.
func valueJSON(v mvt.Value) string {
	switch v.Kind {
	case mvt.ValueString:
		return string(jsonString(v.Str))
	case mvt.ValueBool:
		return strconv.FormatBool(v.Bool)
	case mvt.ValueInt:
		return strconv.FormatInt(v.Int, 10)
	case mvt.ValueSint:
		return strconv.FormatInt(v.Sint, 10)
	case mvt.ValueUint:
		return strconv.FormatUint(v.Uint, 10)
	case mvt.ValueFloat:
		return jsonNumber(float64(v.Float), 32)
	case mvt.ValueDouble:
		return jsonNumber(v.Double, 64)
	}
	// ValueNone, which a decoded tile cannot produce: a value table entry with
	// no field set is refused by the decoder. Written as null rather than
	// dropped so that the property count matches the tag count.
	return "null"
}

func jsonNumber(v float64, bits int) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return string(jsonString(strconv.FormatFloat(v, 'g', -1, bits)))
	}
	return strconv.FormatFloat(v, 'g', -1, bits)
}

// jsonString quotes and escapes a string the way JSON requires.
//
// encoding/json does it rather than a hand-rolled quoter, because tag values
// are somebody else's data: a name containing a quote, a backslash, a control
// character or an unpaired surrogate is a file no parser will accept, and this
// is the one place in the output where arbitrary bytes appear.
func jsonString(s string) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		// json.Marshal of a string fails for no input: invalid UTF-8 is
		// replaced rather than refused.
		return []byte(`""`)
	}
	return b
}

// geometryJSON renders a feature's geometry, reporting false for a feature
// GeoJSON cannot express.
//
// Three things are declined, all of them honestly counted by the caller: a
// feature whose type this decoder could read but not interpret, an empty
// geometry, and a polygon whose exterior ring has fewer than three distinct
// positions. The last is a ring enclosing no area, which the vector tile
// format permits and RFC 7946 does not -- it requires four positions with the
// first repeated as the last -- so writing it would produce a file that
// validators reject and viewers draw as nothing anyway.
func geometryJSON(f mvt.Feature, tr mercator.TileTransform) (string, bool) {
	if f.Geometry.Empty() {
		return "", false
	}
	var b strings.Builder
	switch f.Type {
	case mvt.GeomPoint:
		pts := f.Geometry.Points
		if len(pts) == 1 {
			b.WriteString(`{"type":"Point","coordinates":`)
			writePosition(&b, tr, pts[0])
			b.WriteString("}")
			return b.String(), true
		}
		b.WriteString(`{"type":"MultiPoint","coordinates":[`)
		for i, p := range pts {
			if i > 0 {
				b.WriteString(",")
			}
			writePosition(&b, tr, p)
		}
		b.WriteString("]}")
		return b.String(), true

	case mvt.GeomLineString:
		lines := f.Geometry.Lines
		if len(lines) == 1 {
			b.WriteString(`{"type":"LineString","coordinates":`)
			writeLine(&b, tr, lines[0])
			b.WriteString("}")
			return b.String(), true
		}
		b.WriteString(`{"type":"MultiLineString","coordinates":[`)
		for i, line := range lines {
			if i > 0 {
				b.WriteString(",")
			}
			writeLine(&b, tr, line)
		}
		b.WriteString("]}")
		return b.String(), true

	case mvt.GeomPolygon:
		polys := make([]mvt.Polygon, 0, len(f.Geometry.Polygons))
		for _, p := range f.Geometry.Polygons {
			if len(p.Exterior) >= 3 {
				polys = append(polys, p)
			}
		}
		if len(polys) == 0 {
			return "", false
		}
		if len(polys) == 1 {
			b.WriteString(`{"type":"Polygon","coordinates":`)
			writePolygon(&b, tr, polys[0])
			b.WriteString("}")
			return b.String(), true
		}
		b.WriteString(`{"type":"MultiPolygon","coordinates":[`)
		for i, p := range polys {
			if i > 0 {
				b.WriteString(",")
			}
			writePolygon(&b, tr, p)
		}
		b.WriteString("]}")
		return b.String(), true
	}
	return "", false
}

// writePosition writes one coordinate pair: LONGITUDE FIRST, then latitude.
//
// That order is GeoJSON's and it is the opposite of how the flags on this
// command, and most people, say a coordinate. Getting it backwards puts
// Sydney off the coast of Somalia, which is the kind of wrong that looks like
// a projection bug.
func writePosition(b *strings.Builder, tr mercator.TileTransform, p mvt.Point) {
	lon, lat := tr.LonLat(p.X, p.Y)
	b.WriteString("[")
	b.WriteString(formatCoord(lon))
	b.WriteString(",")
	b.WriteString(formatCoord(lat))
	b.WriteString("]")
}

func writeLine(b *strings.Builder, tr mercator.TileTransform, line []mvt.Point) {
	b.WriteString("[")
	for i, p := range line {
		if i > 0 {
			b.WriteString(",")
		}
		writePosition(b, tr, p)
	}
	b.WriteString("]")
}

// writePolygon writes an exterior ring followed by its holes.
//
// Degenerate holes are dropped for the same reason the caller drops degenerate
// exteriors: a ring of fewer than three points encloses no area, so it cuts
// nothing out of anything, and it is not expressible here.
func writePolygon(b *strings.Builder, tr mercator.TileTransform, p mvt.Polygon) {
	b.WriteString("[")
	writeRing(b, tr, p.Exterior)
	for _, hole := range p.Holes {
		if len(hole) < 3 {
			continue
		}
		b.WriteString(",")
		writeRing(b, tr, hole)
	}
	b.WriteString("]")
}

// writeRing writes one linear ring, reversed and closed.
//
// REVERSED because MVT's winding and GeoJSON's are opposite; see
// writeFeatureCollection. Reversing rather than recomputing an orientation is
// right because the decoder has already normalised every ring it returns, so
// each one's role is known and flipping all of them preserves the exterior and
// hole relationship exactly.
//
// CLOSED by repeating the first position of the reversed ring, because the
// vector tile format leaves the closing point implicit -- a triangle is three
// points -- and RFC 7946 requires it to be explicit. A ring written without it
// is rejected by strict validators and drawn open by lenient ones.
func writeRing(b *strings.Builder, tr mercator.TileTransform, ring mvt.Ring) {
	b.WriteString("[")
	for i := len(ring) - 1; i >= 0; i-- {
		writePosition(b, tr, ring[i])
		b.WriteString(",")
	}
	writePosition(b, tr, ring[len(ring)-1])
	b.WriteString("]")
}
