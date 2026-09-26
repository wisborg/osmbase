package locate_test

import (
	"context"
	"math"
	"testing"

	"github.com/wisborg/osmbase/locate"
	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
)

// The tests here are for the questions a COURSE asks -- which park, which
// street it was on, which city -- and share one way of building a scene:
// features placed in metres from a centre, in whichever tile each belongs to.
// The centre is an invented place, nowhere in particular.

const sceneLat, sceneLon = 10.0, 20.0

// at is the coordinate east and north metres from the centre.
func at(east, north float64) (lat, lon float64) {
	const mPerLat = 111_320.0
	return sceneLat + north/mPerLat, sceneLon + east/(mPerLat*math.Cos(sceneLat*math.Pi/180))
}

// sceneFeature is one feature, its points in metres from the centre. A
// polygon is one exterior ring, a line one line, a point one point.
type sceneFeature struct {
	layer string
	typ   mvt.GeomType
	id    uint64 // 0 is no identifier
	tags  map[string]mvt.Value
	pts   [][2]float64
	// tileEast and tileNorth say which tile the feature is encoded in, as
	// the tile holding that point; zero is the centre's.
	tileEast, tileNorth float64
	// holes are rings cut out of a polygon.
	holes [][][2]float64
}

func text(kv ...string) map[string]mvt.Value {
	m := map[string]mvt.Value{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = mvt.StringValue(kv[i+1])
	}
	return m
}

// scene builds the tiles at zoom holding the features.
func scene(t *testing.T, zoom uint8, feats ...sceneFeature) tiles {
	t.Helper()
	type key = [3]uint32
	layers := map[key]map[string][]osmbasetest.FeatureSpec{}
	for _, f := range feats {
		lat, lon := at(f.tileEast, f.tileNorth)
		x, y, err := mercator.TileAt(zoom, lon, lat)
		if err != nil {
			t.Fatal(err)
		}
		tr, err := mercator.NewTileTransform(zoom, x, y, mvt.DefaultExtent)
		if err != nil {
			t.Fatal(err)
		}
		toRing := func(pts [][2]float64) []mvt.Point {
			var ring []mvt.Point
			for _, p := range pts {
				plat, plon := at(p[0], p[1])
				px, py := tileXY(t, tr, plon, plat)
				ring = append(ring, mvt.Point{X: px, Y: py})
			}
			return ring
		}
		ring := toRing(f.pts)
		spec := osmbasetest.FeatureSpec{Type: f.typ, ID: f.id, HasID: f.id != 0}
		for k, v := range f.tags {
			spec.Tags = append(spec.Tags, osmbasetest.Tag{Key: k, Value: v})
		}
		switch f.typ {
		case mvt.GeomPolygon:
			pg := mvt.Polygon{Exterior: ring}
			for _, h := range f.holes {
				// A hole winds the other way from its exterior.
				r := toRing(h)
				for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
					r[i], r[j] = r[j], r[i]
				}
				pg.Holes = append(pg.Holes, r)
			}
			spec.Geometry.Polygons = []mvt.Polygon{pg}
		case mvt.GeomLineString:
			spec.Geometry.Lines = [][]mvt.Point{ring}
		default:
			spec.Geometry.Points = ring
		}
		k := key{uint32(zoom), x, y}
		if layers[k] == nil {
			layers[k] = map[string][]osmbasetest.FeatureSpec{}
		}
		layers[k][f.layer] = append(layers[k][f.layer], spec)
	}
	out := tiles{}
	for k, ls := range layers {
		var specs []osmbasetest.LayerSpec
		for name, fs := range ls {
			specs = append(specs, osmbasetest.LayerSpec{Name: name, Features: fs})
		}
		data, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: specs})
		if err != nil {
			t.Fatal(err)
		}
		out[k] = data
	}
	return out
}

// square is a polygon of side 2*r metres about (e, n).
func square(e, n, r float64) [][2]float64 {
	return [][2]float64{{e - r, n - r}, {e + r, n - r}, {e + r, n + r}, {e - r, n + r}, {e - r, n - r}}
}

func lookup(t *testing.T, src tiles, east, north float64, o locate.Options) locate.Place {
	t.Helper()
	lat, lon := at(east, north)
	p, err := locate.At(context.Background(), src, locate.Coord{Lat: lat, Lon: lon}, o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A park is named by the label that shares its polygon's identifier -- the
// schema names the label, not the polygon -- and answered Within, as a fact
// from the tiles. The smallest holding area wins, a polygon with no label is
// passed over, and ground cover is not an area.
func TestAreaIsTheSmallestNamedPolygonHoldingThePoint(t *testing.T) {
	areas := []sceneFeature{
		{layer: "landuse", typ: mvt.GeomPolygon, id: 7, tags: text("kind", "park"), pts: square(0, 0, 400)},
		{layer: "pois", typ: mvt.GeomPoint, id: 7, tags: text("kind", "park", "name", "Big Park"), pts: [][2]float64{{300, 300}}},
		{layer: "landuse", typ: mvt.GeomPolygon, id: 8, tags: text("kind", "golf_course"), pts: square(-150, 0, 100)},
		{layer: "pois", typ: mvt.GeomPoint, id: 8, tags: text("kind", "golf_course", "name", "Little Links"), pts: [][2]float64{{-150, 50}}},
		{layer: "landuse", typ: mvt.GeomPolygon, id: 9, tags: text("kind", "cemetery"), pts: square(150, 0, 50)},
		{layer: "landuse", typ: mvt.GeomPolygon, id: 10, tags: text("kind", "grass"), pts: square(0, -150, 60)},
		{layer: "pois", typ: mvt.GeomPoint, id: 10, tags: text("kind", "grass", "name", "Some Lawn"), pts: [][2]float64{{0, -150}}},
	}
	src := scene(t, 14, areas...)
	for _, tc := range []struct {
		name         string
		east, north  float64
		want, source string
	}{
		{"in the park alone", 0, 200, "Big Park", "within"},
		{"on the golf course inside it", -150, 0, "Little Links", "within"},
		{"in an unlabelled cemetery inside it", 150, 0, "Big Park", "within"},
		{"on a named lawn inside it", 0, -150, "Big Park", "within"},
		{"outside everything", 600, 600, "", ""},
	} {
		p := lookup(t, src, tc.east, tc.north, locate.Options{Levels: []locate.Level{locate.Area}})
		m, ok := p.Match(locate.Area)
		if tc.want == "" {
			if ok {
				t.Errorf("%s: answered %+v", tc.name, m)
			}
			continue
		}
		if !ok || m.Name != tc.want || m.Source.String() != tc.source || m.DistanceM != 0 {
			t.Errorf("%s: %+v, want %s %s", tc.name, m, tc.source, tc.want)
		}
	}
}

// A large park's label is where its middle is, which may be a tile away from
// the part a course runs through; the label is looked for there too.
func TestAreaFindsItsLabelInTheNeighbouringTile(t *testing.T) {
	// Zoom 14 tiles here are about 2.4 km across; the label is 3 km east.
	src := scene(t, 14,
		sceneFeature{layer: "landuse", typ: mvt.GeomPolygon, id: 7, tags: text("kind", "park"), pts: square(1500, 0, 1600)},
		sceneFeature{layer: "pois", typ: mvt.GeomPoint, id: 7, tags: text("kind", "park", "name", "Wide Park"), pts: [][2]float64{{3000, 0}}, tileEast: 3000},
	)
	p := lookup(t, src, 0, 0, locate.Options{Levels: []locate.Level{locate.Area}})
	if m, ok := p.Match(locate.Area); !ok || m.Name != "Wide Park" {
		t.Errorf("area %+v; the label in the next tile was not found", m)
	}
}

func way(name, kind string, north float64) sceneFeature {
	tags := text("kind", kind)
	if name != "" {
		tags["name"] = mvt.StringValue(name)
	}
	return sceneFeature{layer: "roads", typ: mvt.GeomLineString, tags: tags, pts: [][2]float64{{-500, north}, {500, north}}}
}

// With OnWay, the nearest way decides. An unnamed path nearer than any named
// street means the point is on the path and on no street; a named street
// within SidewalkM of the path is the street its pavement belongs to; and an
// aeroway is not a way at all. Without OnWay, the nearest named street is the
// answer, as it always was.
func TestOnWayIsTheWayThePointIsOn(t *testing.T) {
	onWay := locate.Options{Levels: []locate.Level{locate.Street}, OnWay: true}
	nearest := locate.Options{Levels: []locate.Level{locate.Street}}
	for _, tc := range []struct {
		name           string
		ways           []sceneFeature
		onWay, nearest string
	}{
		{"a park path 25 m from a road", []sceneFeature{way("", "path", 1), way("Main Road", "major_road", 25)}, "", "Main Road"},
		{"a pavement 8 m from its street", []sceneFeature{way("", "path", 1), way("Main Road", "major_road", 8)}, "Main Road", "Main Road"},
		{"on the street itself", []sceneFeature{way("Main Road", "major_road", 1), way("", "path", 20)}, "Main Road", "Main Road"},
		{"a taxiway", []sceneFeature{way("B", "aeroway", 1)}, "", "B"},
		{"a named path", []sceneFeature{way("Creek Path", "path", 1), way("Main Road", "major_road", 25)}, "Creek Path", "Creek Path"},
	} {
		src := scene(t, 14, tc.ways...)
		for _, c := range []struct {
			o    locate.Options
			want string
			mode string
		}{{onWay, tc.onWay, "OnWay"}, {nearest, tc.nearest, "nearest"}} {
			m, ok := lookup(t, src, 0, 0, c.o).Match(locate.Street)
			if got := map[bool]string{true: m.Name}[ok]; got != c.want {
				t.Errorf("%s, %s: %q, want %q", tc.name, c.mode, got, c.want)
			}
		}
	}
}

// A suburb in reach is the neighbourhood, over a nearer minor neighbourhood
// label; the minor one answers only where there is no suburb.
func TestNeighbourhoodPrefersASuburb(t *testing.T) {
	label := func(name, detail string, east float64) sceneFeature {
		return sceneFeature{layer: "places", typ: mvt.GeomPoint,
			tags: text("kind", "neighbourhood", "kind_detail", detail, "name", name), pts: [][2]float64{{east, 0}}}
	}
	o := locate.Options{Levels: []locate.Level{locate.Neighbourhood}}
	src := scene(t, 14, label("Little Precinct", "neighbourhood", 100), label("The Suburb", "suburb", 1000))
	if m, _ := lookup(t, src, 0, 0, o).Match(locate.Neighbourhood); m.Name != "The Suburb" {
		t.Errorf("answered %q; the suburb in reach should win", m.Name)
	}
	src = scene(t, 14, label("Little Precinct", "neighbourhood", 100))
	if m, _ := lookup(t, src, 0, 0, o).Match(locate.Neighbourhood); m.Name != "Little Precinct" {
		t.Errorf("answered %q; with no suburb the neighbourhood is the answer", m.Name)
	}
}

// Prominent takes the place the map shows first, then the most populous,
// over the nearest; without it the nearest is the answer.
func TestProminentLocalityIsTheOneTheMapShowsFirst(t *testing.T) {
	town := func(name string, minZoom int64, pop uint64, east float64) sceneFeature {
		tags := text("kind", "locality", "name", name)
		tags["min_zoom"] = mvt.IntValue(minZoom)
		tags["population"] = mvt.UintValue(pop)
		return sceneFeature{layer: "places", typ: mvt.GeomPoint, tags: tags, pts: [][2]float64{{east, 0}}}
	}
	src := scene(t, 10,
		town("Nearby Town", 8, 20_000, 3_000),
		town("Big City", 2, 4_000_000, 15_000),
		town("Other Town", 8, 60_000, 5_000),
	)
	near := lookup(t, src, 0, 0, locate.Options{Levels: []locate.Level{locate.Locality}})
	if m, _ := near.Match(locate.Locality); m.Name != "Nearby Town" {
		t.Errorf("nearest is %q", m.Name)
	}
	prom := lookup(t, src, 0, 0, locate.Options{Levels: []locate.Level{locate.Locality}, Prominent: true})
	if m, _ := prom.Match(locate.Locality); m.Name != "Big City" {
		t.Errorf("prominent is %q", m.Name)
	}
	src = scene(t, 10, town("Nearby Town", 8, 20_000, 3_000), town("Other Town", 8, 60_000, 5_000))
	prom = lookup(t, src, 0, 0, locate.Options{Levels: []locate.Level{locate.Locality}, Prominent: true})
	if m, _ := prom.Match(locate.Locality); m.Name != "Other Town" {
		t.Errorf("between two towns shown at the same zoom, prominent is %q; want the more populous", m.Name)
	}
}

// A point in a hole cut out of an area -- a lake in a park -- is not in it.
func TestAreaLeavesOutItsHoles(t *testing.T) {
	src := scene(t, 14,
		sceneFeature{layer: "landuse", typ: mvt.GeomPolygon, id: 7, tags: text("kind", "park"),
			pts: square(0, 0, 400), holes: [][][2]float64{square(0, 0, 100)}},
		sceneFeature{layer: "pois", typ: mvt.GeomPoint, id: 7, tags: text("kind", "park", "name", "Ring Park"), pts: [][2]float64{{300, 300}}},
	)
	o := locate.Options{Levels: []locate.Level{locate.Area}}
	if m, ok := lookup(t, src, 0, 0, o).Match(locate.Area); ok {
		t.Errorf("a point in the park's lake is in %+v", m)
	}
	if m, ok := lookup(t, src, 250, 0, o).Match(locate.Area); !ok || m.Name != "Ring Park" {
		t.Errorf("a point on the park's lawn is in %+v", m)
	}
}

// The preference holds across tiles: a suburb in the neighbouring tile beats
// a nearer minor label in the point's own, as it would in the same tile.
func TestNeighbourhoodPrefersASuburbFromTheNextTile(t *testing.T) {
	src := scene(t, 14,
		sceneFeature{layer: "places", typ: mvt.GeomPoint, tags: text("kind", "neighbourhood", "kind_detail", "neighbourhood", "name", "Little Precinct"), pts: [][2]float64{{50, 0}}},
		sceneFeature{layer: "places", typ: mvt.GeomPoint, tags: text("kind", "neighbourhood", "kind_detail", "suburb", "name", "The Suburb"), pts: [][2]float64{{2600, 0}}, tileEast: 2600},
	)
	if len(src) != 2 {
		t.Fatalf("the fixture is in %d tiles; the suburb must be in the next one", len(src))
	}
	m, _ := lookup(t, src, 0, 0, locate.Options{Levels: []locate.Level{locate.Neighbourhood}}).Match(locate.Neighbourhood)
	if m.Name != "The Suburb" {
		t.Errorf("answered %q", m.Name)
	}
}
