package render_test

import (
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/render"
)

// Everything here is synthetic. No archive is downloaded, no coordinate comes
// from anywhere real, and the whole content of every fixture is in this file:
// a tile whose water is a square and whose road is a straight line is a tile
// whose correct picture can be worked out on paper, which is the only kind a
// test can check.

const testExtent = mvt.DefaultExtent

// The test palette is eight saturated, unmistakable colours, so that a pixel
// can be compared for equality rather than for being roughly the right shade.
// The real palettes are muted on purpose and every one of their colours is
// within a few steps of the others, which makes them useless here.
var testPalette = render.Palette{
	Background: color.RGBA{R: 0x00, G: 0x00, B: 0x00, A: 0xff},
	Land:       color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	Water:      color.RGBA{R: 0x00, G: 0x00, B: 0xff, A: 0xff},
	Green:      color.RGBA{R: 0x00, G: 0xff, B: 0x00, A: 0xff},
	Built:      color.RGBA{R: 0xff, G: 0x00, B: 0xff, A: 0xff},
	Road:       color.RGBA{R: 0xff, G: 0x00, B: 0x00, A: 0xff},
	Ink:        color.RGBA{R: 0xff, G: 0xff, B: 0x00, A: 0xff},
	NoData:     color.RGBA{R: 0x00, G: 0xff, B: 0xff, A: 0xff},
}

// testStyle draws four things in a known order. The road is eight tile pixels
// wide, which at one surface pixel per tile pixel is eight rows of solid colour
// -- wide enough that a pixel two from the centre is unambiguously inside it.
func testStyle() render.Style {
	all := func(layer string, kinds []string, p render.Paint) render.Rule {
		return render.Rule{Layer: layer, Kinds: kinds, MinZoom: 0, MaxZoom: render.MaxRuleZoom, Paint: p}
	}
	return render.Style{
		Name:   "test",
		Schema: "test",
		Rules: []render.Rule{
			all("earth", nil, render.Paint{Role: render.RoleLand, Fill: true}),
			all("landuse", []string{"park"}, render.Paint{Role: render.RoleGreen, Fill: true}),
			all("water", nil, render.Paint{Role: render.RoleWater, Fill: true}),
			all("roads", nil, render.Paint{Role: render.RoleRoad, Width: 8}),
		},
	}
}

// mapSource is a TileSource over a handful of prepared tiles.
//
// That it is four lines long is the point of the interface being one method
// wide: a store, an archive and a test fixture all answer the same question,
// and the renderer cannot tell which one it is talking to.
type mapSource struct {
	tiles map[tileID][]byte
	reads int
}

type tileID struct {
	z    uint8
	x, y uint32
}

func (s *mapSource) Tile(z uint8, x, y uint32) ([]byte, bool, error) {
	s.reads++
	data, ok := s.tiles[tileID{z, x, y}]
	return data, ok, nil
}

func newSource() *mapSource { return &mapSource{tiles: map[tileID][]byte{}} }

func (s *mapSource) put(t *testing.T, z uint8, x, y uint32, layers ...osmbasetest.LayerSpec) {
	t.Helper()
	data, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: layers})
	if err != nil {
		t.Fatalf("building tile %d/%d/%d: %v", z, x, y, err)
	}
	s.tiles[tileID{z, x, y}] = data
}

// areaLayer is one layer holding a single axis-aligned polygon in tile-local
// coordinates.
//
// The ring is wound clockwise with y running down, which is positive by the
// surveyor's formula and is what the vector tile specification calls an
// exterior. mvt normalises orientation on decode anyway, so this is the
// fixture saying what it means rather than a requirement.
func areaLayer(name, kind string, x0, y0, x1, y1 int32) osmbasetest.LayerSpec {
	f := osmbasetest.FeatureSpec{
		Type: mvt.GeomPolygon,
		Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{
			Exterior: mvt.Ring{{X: x0, Y: y0}, {X: x1, Y: y0}, {X: x1, Y: y1}, {X: x0, Y: y1}},
		}}},
	}
	if kind != "" {
		f.Tags = []osmbasetest.Tag{{Key: "kind", Value: mvt.StringValue(kind)}}
	}
	return osmbasetest.LayerSpec{Name: name, Features: []osmbasetest.FeatureSpec{f}}
}

// lineLayer is one layer holding a single polyline in tile-local coordinates.
func lineLayer(name, kind string, pts ...mvt.Point) osmbasetest.LayerSpec {
	f := osmbasetest.FeatureSpec{
		Type:     mvt.GeomLineString,
		Geometry: mvt.Geometry{Lines: [][]mvt.Point{pts}},
	}
	if kind != "" {
		f.Tags = []osmbasetest.Tag{{Key: "kind", Value: mvt.StringValue(kind)}}
	}
	return osmbasetest.LayerSpec{Name: name, Features: []osmbasetest.FeatureSpec{f}}
}

// wholeTile is a polygon covering a tile exactly.
func wholeTile(name, kind string) osmbasetest.LayerSpec {
	return areaLayer(name, kind, 0, 0, testExtent, testExtent)
}

// tileView returns the view showing the rectangle of tiles from x0,y0 to x1,y1
// at zoom z, at px pixels per tile.
//
// The rectangle is a whole number of tiles on both axes and the image is the
// matching whole number of px-sized squares, so with px equal to the 256 a zoom
// level is defined against, the view resolves to exactly zoom z at one surface
// pixel per tile pixel. Every expected pixel in this file is worked out from
// that.
func tileView(t *testing.T, z uint8, x0, y0, x1, y1 uint32, px int) render.View {
	t.Helper()
	west, _, _, north, err := mercator.TileBounds(z, x0, y0)
	if err != nil {
		t.Fatalf("mercator.TileBounds: %v", err)
	}
	_, south, east, _, err := mercator.TileBounds(z, x1, y1)
	if err != nil {
		t.Fatalf("mercator.TileBounds: %v", err)
	}
	return render.View{
		Bounds: render.Bounds{West: west, South: south, East: east, North: north},
		Width:  px * int(x1-x0+1),
		Height: px * int(y1-y0+1),
	}
}

func draw(t *testing.T, src render.TileSource, v render.View) *render.Result {
	t.Helper()
	r, err := render.New(src, render.Options{Style: testStyle(), Palette: testPalette})
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	res, err := r.Render(context.Background(), v)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return res
}

// near reports whether two colours are the same to within one or two steps of
// eight-bit alpha.
//
// The slack is not cosmetic and it is not there to make a failing test pass.
// Coverage inside the rasterizer is a float32 running sum over the whole pixel
// buffer, and geometry at or beyond the image's last column is folded onto a
// single accumulator slot, so a path carrying a few dozen strokes that reach
// the right edge -- which is what a view whose roads run off it is -- can leave
// about one 255th of coverage behind for the rows after it. Measured: a pure
// white pixel comes back as 254 in one channel. That is invisible and it is
// real, so an exact comparison here would be a test that fails on a correct
// picture. Every colour in this file's palette differs from every other by 255
// in at least one channel, so two steps discriminates perfectly.
func near(a, b color.RGBA, tol int) bool {
	d := func(p, q uint8) int {
		if p > q {
			return int(p) - int(q)
		}
		return int(q) - int(p)
	}
	return d(a.R, b.R) <= tol && d(a.G, b.G) <= tol && d(a.B, b.B) <= tol && d(a.A, b.A) <= tol
}

// at asserts the colour of one pixel, naming what it found instead.
func at(t *testing.T, img *image.RGBA, x, y int, want color.RGBA, why string) {
	t.Helper()
	if got := img.RGBAAt(x, y); !near(got, want, 2) {
		t.Errorf("pixel (%d, %d) is %v, want %v: %s", x, y, got, want, why)
	}
}

// pixelOf projects a coordinate into a view's image, independently of the
// renderer.
//
// It goes through mercator.Project and the view's own rectangle rather than
// through anything in render, so that a test asserting "this coordinate is
// water" is checking the renderer against the projection and not against
// itself. It refuses a view whose two scales differ, because the arithmetic
// below assumes the simple case.
func pixelOf(t *testing.T, v render.View, lon, lat float64) (int, int) {
	t.Helper()
	x0, y0 := mercator.Project(v.Bounds.West, v.Bounds.North)
	x1, y1 := mercator.Project(v.Bounds.East, v.Bounds.South)
	sx := float64(v.Width) / (x1 - x0)
	sy := float64(v.Height) / (y1 - y0)
	if math.Abs(sx-sy) > 1e-6*sx {
		t.Fatalf("pixelOf assumes a view whose axes share a scale; they are %g and %g", sx, sy)
	}
	wx, wy := mercator.Project(lon, lat)
	return int((wx - x0) * sx), int((wy - y0) * sy)
}

// TestRender_AKnownCoordinateIsWater is the first thing the design asks this
// step to prove.
//
// The fixture is one tile at zoom 4 holding land over the whole tile and a
// square of water from tile unit 1024 to 3072 of 4096. The view is that tile on
// 256 by 256 pixels, so a tile unit is 256/4096 = one sixteenth of a pixel and
// the lake covers pixels 64 through 191 on both axes. The coordinate under test
// is the lake's centre, tile unit 2048, which is pixel 128 -- but it is
// converted to degrees by mercator and back to a pixel by this file's own
// arithmetic, so what is being checked is that the renderer put the water where
// the projection says the coordinate is, and not merely where the fixture put
// it.
func TestRender_AKnownCoordinateIsWater(t *testing.T) {
	const z, tx, ty = 4, 9, 6

	src := newSource()
	src.put(t, z, tx, ty,
		wholeTile("earth", "earth"),
		areaLayer("water", "lake", 1024, 1024, 3072, 3072),
	)
	v := tileView(t, z, tx, ty, tx, ty, 256)
	res := draw(t, src, v)

	tr, err := mercator.NewTileTransform(z, tx, ty, testExtent)
	if err != nil {
		t.Fatalf("mercator.NewTileTransform: %v", err)
	}

	lakeLon, lakeLat := tr.LonLat(2048, 2048)
	x, y := pixelOf(t, v, lakeLon, lakeLat)
	if x != 128 || y != 128 {
		t.Fatalf("the lake's centre projects to pixel (%d, %d), want (128, 128); the fixture and the projection disagree before the renderer is involved", x, y)
	}
	at(t, res.Image, x, y, testPalette.Water, "the coordinate is inside the lake")

	// A coordinate a quarter of the way across the tile is outside the lake,
	// which starts at tile unit 1024 -- exactly a quarter. Tile unit 512 is
	// half of that and unambiguously on land.
	dryLon, dryLat := tr.LonLat(512, 512)
	dx, dy := pixelOf(t, v, dryLon, dryLat)
	at(t, res.Image, dx, dy, testPalette.Land, "the coordinate is outside the lake")

	if res.Zoom != z {
		t.Errorf("Zoom = %d, want %d", res.Zoom, z)
	}
	if res.Covered != 1 {
		t.Errorf("Covered = %g, want 1", res.Covered)
	}
	if res.Overzoomed != 0 {
		t.Errorf("Overzoomed = %g, want 0; the tile asked for was there", res.Overzoomed)
	}
}

// TestRender_ARoadFromOneTileDrawsOverLanduseFromAnother is the layer-ordering
// property, and the one that catches drawing tile by tile.
//
// Two tiles side by side at zoom 4. The left one holds a road running the whole
// width of the tile, ending exactly on the shared edge; the right one holds a
// park covering all of it. The style draws landuse before roads, so the road
// wins wherever they meet.
//
// Where they meet is the part worth being precise about. Each tile's geometry
// is clipped to that tile's own square, so the road's last point is on the seam
// -- pixel 256 of a 512-pixel image -- and the round cap the stroker puts at
// every end is a disc of half the width, four pixels, centred there. So the
// road paints pixels 256 to 259 of the RIGHT tile's square, over ground whose
// only feature is the park.
//
// Drawn tile by tile, the right tile's park is filled after the left tile's
// road and covers exactly that sliver. It is four pixels wide, it is the same
// four pixels along every tile boundary in the map, and it is invisible on any
// view that does not cross one.
func TestRender_ARoadFromOneTileDrawsOverLanduseFromAnother(t *testing.T) {
	const z, ty = 4, 6

	src := newSource()
	src.put(t, z, 9, ty,
		wholeTile("earth", "earth"),
		lineLayer("roads", "minor_road",
			mvt.Point{X: 0, Y: testExtent / 2},
			mvt.Point{X: testExtent, Y: testExtent / 2},
		),
	)
	src.put(t, z, 10, ty, wholeTile("landuse", "park"))

	v := tileView(t, z, 9, ty, 10, ty, 256)
	res := draw(t, src, v)

	at(t, res.Image, 258, 128, testPalette.Road,
		"the road from the left tile reaches two pixels into the right tile's square, and landuse there is drawn before every road in the view")
	at(t, res.Image, 200, 128, testPalette.Road, "the road in its own tile")
	at(t, res.Image, 300, 128, testPalette.Green,
		"eight pixels past the seam is beyond the road's cap and is park")
	at(t, res.Image, 300, 40, testPalette.Green, "the park away from the road")
}

// TestRender_AMissingTileIsDrawnFromItsAncestorAndReported covers the design's
// answer to a cache miss: walk up the pyramid.
//
// Only the zoom-4 ancestor exists. The view asks for four zoom-6 tiles, gets
// none of them, and is drawn entirely from that one ancestor -- sharp, because
// vector data overzooms without blurring, and less detailed, because the detail
// is not in the tile. That is why it has to be REPORTED: it looks complete.
func TestRender_AMissingTileIsDrawnFromItsAncestorAndReported(t *testing.T) {
	// Tile 6/40/24 sits inside tile 4/10/6: shifting the coordinates right by
	// two zoom levels gives 40>>2 = 10 and 24>>2 = 6.
	src := newSource()
	src.put(t, 4, 10, 6, wholeTile("earth", "earth"), wholeTile("water", "ocean"))

	v := tileView(t, 6, 40, 24, 41, 25, 256)
	res := draw(t, src, v)

	if res.Zoom != 6 {
		t.Errorf("Zoom = %d, want 6; the view still resolves to the zoom it asked for", res.Zoom)
	}
	if res.Covered != 1 {
		t.Errorf("Covered = %g, want 1", res.Covered)
	}
	if res.Overzoomed != 1 {
		t.Errorf("Overzoomed = %g, want 1; every square came from a shallower tile", res.Overzoomed)
	}
	if res.TilesDrawn != 4 || res.TilesRequested != 4 {
		t.Errorf("TilesDrawn = %d of %d requested, want 4 of 4", res.TilesDrawn, res.TilesRequested)
	}
	if len(res.Gaps) != 0 {
		t.Errorf("Gaps = %v, want none; an overzoomed square is covered", res.Gaps)
	}
	at(t, res.Image, 256, 256, testPalette.Water, "the ancestor's water fills the view")
}

// TestRender_NoTilesAtAllIsAnErrorAndNoImage pins the design's absent-data
// table: no coverage returns ErrNoCoverage and nothing to draw.
//
// Not a blank image and not a fully hatched one. Both of those are pictures, and
// the consumer has to be able to lay out its frame as though no basemap were
// configured at all -- which is a different decision from "there is a map here
// with holes in it", and is not this library's to make.
func TestRender_NoTilesAtAllIsAnErrorAndNoImage(t *testing.T) {
	r, err := render.New(newSource(), render.Options{Style: testStyle(), Palette: testPalette})
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	res, err := r.Render(context.Background(), tileView(t, 8, 100, 100, 100, 100, 256))
	if !errors.Is(err, render.ErrNoCoverage) {
		t.Fatalf("Render returned %v, want an error wrapping ErrNoCoverage", err)
	}
	if res != nil {
		t.Errorf("Render returned a result as well as ErrNoCoverage; there is no image for a view with no data")
	}
}

// TestRender_UncoveredSquaresAreHatchedAndReported is the partial-coverage
// case, where an image IS returned.
//
// Half the view has tiles and half has none at any zoom. The missing half is
// painted with the no-data hatch rather than left as background, because
// background in this schema is the ocean: a plain gap would be indistinguishable
// from the sea and from a crash. The rectangle is reported as well, because what
// a half-covered map means for a layout is the caller's decision.
func TestRender_UncoveredSquaresAreHatchedAndReported(t *testing.T) {
	const z, ty = 4, 6

	src := newSource()
	src.put(t, z, 9, ty, wholeTile("earth", "earth"))

	v := tileView(t, z, 9, ty, 10, ty, 256)
	res := draw(t, src, v)

	if res.TilesRequested != 2 || res.TilesDrawn != 1 {
		t.Errorf("TilesDrawn = %d of %d requested, want 1 of 2", res.TilesDrawn, res.TilesRequested)
	}
	if math.Abs(res.Covered-0.5) > 1e-9 {
		t.Errorf("Covered = %g, want 0.5", res.Covered)
	}
	want := image.Rect(256, 0, 512, 256)
	if len(res.Gaps) != 1 || res.Gaps[0] != want {
		t.Errorf("Gaps = %v, want exactly %v", res.Gaps, want)
	}

	// The hatch has to be inside the gap and nowhere else. Counting is the
	// honest test: the lines are diagonal, so no single pixel is guaranteed to
	// be on one, and any pixel of the hatch colour outside the gap would be
	// this library claiming ignorance about ground it has data for.
	inside, outside := 0, 0
	b := res.Image.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			// "Substantially hatched" rather than "exactly the hatch
			// colour": the lines are antialiased, so their edges are every
			// shade between the two, and a pixel a long way from either is
			// what a leak would look like.
			if near(res.Image.RGBAAt(x, y), testPalette.NoData, 64) {
				if image.Pt(x, y).In(want) {
					inside++
				} else {
					outside++
				}
			}
		}
	}
	if inside == 0 {
		t.Error("the uncovered half has no hatch on it at all, so it is indistinguishable from ocean")
	}
	if outside != 0 {
		t.Errorf("%d hatched pixels fall outside the gap, on ground the source has a tile for", outside)
	}
	at(t, res.Image, 100, 128, testPalette.Land, "the covered half is drawn normally")
}

// TestRender_ALayerWithNoFeaturesIsNotAbsence is trap T9.
//
// The tile here has land and nothing else: no water, no roads, no buildings.
// That is the correct picture of an area with none of those, and reporting it
// as missing data would cry wolf on every stretch of empty country a route
// crosses. Coverage is a question about which tiles the source has, and this
// source has the tile.
func TestRender_ALayerWithNoFeaturesIsNotAbsence(t *testing.T) {
	src := newSource()
	src.put(t, 4, 9, 6, wholeTile("earth", "earth"))

	res := draw(t, src, tileView(t, 4, 9, 6, 9, 6, 256))

	if res.Covered != 1 {
		t.Errorf("Covered = %g, want 1", res.Covered)
	}
	if len(res.Gaps) != 0 {
		t.Errorf("Gaps = %v, want none", res.Gaps)
	}
	b := res.Image.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if got := res.Image.RGBAAt(x, y); near(got, testPalette.NoData, 64) {
				t.Fatalf("pixel (%d, %d) is %v, close to the hatch colour; a tile with no water in it is not a tile that is missing", x, y, got)
			}
		}
	}
}

// TestRender_TheSameViewTwiceIsTheSameBytes is the determinism contract.
//
// Coverage inside the rasterizer is a float32 sum, so the same features
// appended in a different order can move a pixel by one step of alpha. Ranging
// over a map anywhere in the draw path -- the decoded-tile cache, a feature's
// tags, a set of layer names -- would put Go's randomised iteration order into
// the image, and a video would flicker between two renders of one activity.
func TestRender_TheSameViewTwiceIsTheSameBytes(t *testing.T) {
	build := func() *render.Result {
		src := newSource()
		src.put(t, 5, 18, 12,
			wholeTile("earth", "earth"),
			areaLayer("water", "lake", 700, 900, 2600, 3300),
			lineLayer("roads", "minor_road",
				mvt.Point{X: 100, Y: 200}, mvt.Point{X: 3000, Y: 1500}, mvt.Point{X: 900, Y: 3900},
			),
		)
		return draw(t, src, tileView(t, 5, 18, 12, 18, 12, 256))
	}
	a, b := build(), build()
	if len(a.Image.Pix) != len(b.Image.Pix) {
		t.Fatalf("two renders gave %d and %d bytes", len(a.Image.Pix), len(b.Image.Pix))
	}
	for i := range a.Image.Pix {
		if a.Image.Pix[i] != b.Image.Pix[i] {
			t.Fatalf("two renders of one view differ at byte %d: %d against %d", i, a.Image.Pix[i], b.Image.Pix[i])
		}
	}
}

// TestRender_TheStylesTilePixelsBecomeSurfacePixels checks the width
// conversion end to end.
//
// The style's road is 8 tile pixels wide and the view is one tile on 256
// pixels, which is one surface pixel per tile pixel, so the road is 8 pixels
// wide. Its centreline is at tile unit 2048, which is pixel 128, so the stroke
// runs from 124 to 132 and covers rows 124 through 131 completely. Eight rows,
// derived rather than recorded: anything else means a style width has been
// treated as an output-pixel constant, or the half width has been used as the
// width.
func TestRender_TheStylesTilePixelsBecomeSurfacePixels(t *testing.T) {
	src := newSource()
	src.put(t, 5, 18, 12,
		wholeTile("earth", "earth"),
		lineLayer("roads", "minor_road",
			mvt.Point{X: 0, Y: testExtent / 2},
			mvt.Point{X: testExtent, Y: testExtent / 2},
		),
	)
	res := draw(t, src, tileView(t, 5, 18, 12, 18, 12, 256))

	solid := 0
	for y := 0; y < 256; y++ {
		if near(res.Image.RGBAAt(128, y), testPalette.Road, 2) {
			solid++
		}
	}
	if solid != 8 {
		t.Errorf("the road is %d solid pixels thick, want 8", solid)
	}
	at(t, res.Image, 128, 124, testPalette.Road, "the top row of the stroke")
	at(t, res.Image, 128, 131, testPalette.Road, "the bottom row of the stroke")
	at(t, res.Image, 128, 123, testPalette.Land, "one row above the stroke")
	at(t, res.Image, 128, 132, testPalette.Land, "one row below the stroke")
}

// TestRender_OffSurfaceGeometryIsClippedRatherThanWalked is the cost contract,
// and it is a time bound because there is nothing else to measure it by.
//
// The fixture is the zoom-0 tile, and the view is a single zoom-20 tile, so the
// ancestor's geometry is drawn at 2^20 times its own scale: the ring's corners
// land about 1.3 times 10^8 pixels from the image. raster walks a segment
// scanline by scanline from where it starts down to the surface -- 12 ms for a
// million pixels and 4.5 seconds for a billion, measured -- so each of those
// segments is well over a second unclipped, and there are several. Clipped,
// the whole render is a handful of milliseconds.
//
// Two seconds is therefore not a performance target. It is far below the
// unclipped cost and far above the clipped one, which is the only kind of time
// bound worth writing.
func TestRender_OffSurfaceGeometryIsClippedRatherThanWalked(t *testing.T) {
	src := newSource()
	src.put(t, 0, 0, 0,
		wholeTile("earth", "earth"),
		areaLayer("water", "ocean", 0, 0, testExtent, testExtent/2),
		lineLayer("roads", "highway",
			mvt.Point{X: 0, Y: testExtent / 2},
			mvt.Point{X: testExtent, Y: testExtent / 2},
		),
	)

	const centre = 1 << 19 // the middle of the zoom-20 grid
	v := tileView(t, 20, centre, centre, centre, centre, 256)

	start := time.Now()
	res := draw(t, src, v)
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("rendering one tile took %s; geometry far outside the surface is being walked rather than clipped", elapsed)
	}
	if res.Overzoomed != 1 {
		t.Errorf("Overzoomed = %g, want 1", res.Overzoomed)
	}
	// The zoom-20 tile at the centre of the grid sits just south of the
	// equator, and the ancestor's water covers the northern half of the world,
	// so what is underneath is land.
	at(t, res.Image, 128, 128, testPalette.Land, "the overzoomed ancestor still draws the right thing")
}

// TestRender_APolygonSpanningTwoTilesHasNoSeam is the narrower half of trap
// T1, and the reason the road-over-landuse test above is not enough.
//
// That test catches a full loop swap -- tiles outside, rules inside -- because
// it destroys z-order across tiles. It does NOT catch the subtler wrong
// refactor: keeping rules outside and moving the FILL inside the tile loop.
// Z-order survives that, every other test in this package passes, and the
// picture grows a permanent hairline along every tile edge in a shade that is
// a legitimate darker version of the thing being drawn.
//
// The arithmetic is in raster's own seam tests: two abutting half-covered
// edges summed in one pass give 1.0, composited in two passes give
// 0.5 + 0.5*(1-0.5) = 0.75. What is pinned here is that the renderer actually
// arranges the one-pass case, at the level where the mistake would be made.
//
// Both tiles are filled edge to edge with the same layer and kind, so the two
// halves of one apparent polygon meet exactly on the seam. Any pixel there
// that is not the full ink is the hairline.
func TestRender_APolygonSpanningTwoTilesHasNoSeam(t *testing.T) {
	const z, ty = 4, 6

	src := newSource()
	src.put(t, z, 9, ty, wholeTile("water", "water"))
	src.put(t, z, 10, ty, wholeTile("water", "water"))

	// The width is chosen so the tile boundary lands MID-PIXEL, and that
	// detail is the whole test. Two tiles 256 pixels wide each put the seam at
	// x=256 exactly, every pixel belongs wholly to one tile or the other, and
	// separate fills produce no artefact at all -- which is how the first
	// version of this test passed against the very mutation it was written to
	// catch. At 501 pixels across, each tile is 250.5 wide, the seam falls
	// inside the pixel at x=250, and that pixel is half covered by each side.
	// Summed in one pass it is full water; composited in two it is
	// 0.5 + 0.5*(1-0.5) = 0.75 of the way from the background, which is the
	// hairline.
	west, _, _, north, err := mercator.TileBounds(z, 9, ty)
	if err != nil {
		t.Fatal(err)
	}
	_, south, east, _, err := mercator.TileBounds(z, 10, ty)
	if err != nil {
		t.Fatal(err)
	}
	v := render.View{
		Bounds: render.Bounds{West: west, South: south, East: east, North: north},
		Width:  501, Height: 250,
	}
	res := draw(t, src, v)

	for _, x := range []int{249, 250, 251} {
		for _, y := range []int{40, 125, 210} {
			at(t, res.Image, x, y, testPalette.Water,
				"two tiles' halves of one water polygon must meet without a seam")
		}
	}
}

// TestRender_AnOmittedRoleIsNotDrawnAtAll is the drawing half of
// Palette.Omitted: the role does not reach the picture, rather than reaching
// it in a colour nobody can see.
//
// The same tile is rendered twice against palettes that differ only in
// whether the park role is declared omitted. The first shows park; the second
// shows the background through it, and the ROAD crossing the same square is
// untouched -- which is what separates "this role is left out" from "this
// style has stopped drawing".
func TestRender_AnOmittedRoleIsNotDrawnAtAll(t *testing.T) {
	const z, tx, ty = 4, 9, 6
	src := newSource()
	src.put(t, z, tx, ty,
		wholeTile("landuse", "park"),
		lineLayer("roads", "minor_road",
			mvt.Point{X: 0, Y: testExtent / 2},
			mvt.Point{X: testExtent, Y: testExtent / 2},
		),
	)
	v := tileView(t, z, tx, ty, tx, ty, 256)

	drawWith := func(p render.Palette) *image.RGBA {
		t.Helper()
		r, err := render.New(src, render.Options{Style: testStyle(), Palette: p})
		if err != nil {
			t.Fatalf("render.New: %v", err)
		}
		res, err := r.Render(context.Background(), v)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		return res.Image
	}

	shown := drawWith(testPalette)
	at(t, shown, 128, 40, testPalette.Green, "the park is drawn when nothing is omitted")

	omitting := testPalette
	omitting.Omitted = []render.Role{render.RoleGreen}
	hidden := drawWith(omitting)
	at(t, hidden, 128, 40, testPalette.Background, "an omitted role leaves the background showing")
	at(t, hidden, 128, 128, testPalette.Road, "the road is still drawn: omitting one role must not stop the others")
}
