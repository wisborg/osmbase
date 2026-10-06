package render_test

import (
	"bytes"
	"context"
	"github.com/wisborg/osmbase/osmbasetest"
	"golang.org/x/image/font/basicfont"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/render"
)

// heights is a HeightSource of one zoom-0 tile.
type heights struct {
	size int
	h    func(i, j int) float32
}

func (s heights) Heights(z uint8, x, y uint32) ([]float32, int, bool, error) {
	if z != 0 || s.h == nil {
		return nil, 0, false, nil
	}
	out := make([]float32, s.size*s.size)
	for j := 0; j < s.size; j++ {
		for i := 0; i < s.size; i++ {
			out[j*s.size+i] = s.h(i, j)
		}
	}
	return out, s.size, true, nil
}

// ridge runs north to south down the middle of the world. Its heights are
// absurd -- hundreds of kilometres -- so that at zoom 1, where a pixel is
// tens of kilometres of ground, its flanks are slopes of about 35°. Not
// steeper: past 60° or so even a flank facing the sun turns away from it,
// and is darker than flat ground, which is right and is not this test.
var ridge = heights{size: 64, h: func(i, j int) float32 {
	d := float32(i) - 31.5
	if d < 0 {
		d = -d
	}
	return 1e6 - d*1e4
}}

// reliefMap is the world at zoom 1, all land, with a road along the
// equator.
func reliefMap(t *testing.T) (*mapSource, render.View) {
	src := newSource()
	for y := uint32(0); y < 2; y++ {
		for x := uint32(0); x < 2; x++ {
			src.put(t, 1, x, y, wholeTile("earth", ""))
		}
	}
	src.put(t, 1, 0, 0, wholeTile("earth", ""), lineLayer("roads", "", mvt.Point{X: 0, Y: testExtent - 2}, mvt.Point{X: testExtent, Y: testExtent - 2}))
	src.put(t, 1, 1, 0, wholeTile("earth", ""), lineLayer("roads", "", mvt.Point{X: 0, Y: testExtent - 2}, mvt.Point{X: testExtent, Y: testExtent - 2}))
	return src, tileView(t, 1, 0, 0, 1, 1, 256)
}

func drawWith(t *testing.T, src render.TileSource, v render.View, pal render.Palette, hs render.HeightSource) *render.Result {
	t.Helper()
	r, err := render.New(src, render.Options{Style: testStyle(), Palette: pal, Attribution: "map", Terrain: hs, TerrainAttribution: "ground", TerrainNotice: "the ground's full notice"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Render(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// shadingPalette shades toward black and lights toward a blue, so on white
// land the two are told apart by channel: shadow takes blue down, light
// takes red down and leaves blue.
func shadingPalette() render.Palette {
	p := testPalette
	p.Shade = color.RGBA{A: 0xff}
	p.Highlight = color.RGBA{R: 0x40, G: 0x40, B: 0xff, A: 0xff}
	return p
}

// A ridge under a north-west sun: its west flank, facing the light, is lit,
// its east flank in shadow, and the road over both is drawn as it would be
// without terrain, since the shade is under the lines.
func TestRelief_TheSunlitFlankIsLitAndTheRoadIsNotShaded(t *testing.T) {
	src, v := reliefMap(t)
	res := drawWith(t, src, v, shadingPalette(), ridge)
	img := res.Image

	west, east := img.RGBAAt(128, 128), img.RGBAAt(384, 128)
	if !(west.B == 0xff && west.R < 0xf0) {
		t.Errorf("west flank %v, want lit: red down toward the highlight, blue untouched", west)
	}
	if !(east.B < 0xf0) {
		t.Errorf("east flank %v, want in shadow", east)
	}
	// The road is centred two tile pixels above the equator, eight wide.
	for _, x := range []int{128, 384} {
		at(t, img, x, 254, testPalette.Road, "a road over shaded ground keeps its colour")
	}
	if res.TerrainCovered != 1 || res.TerrainZoom != 0 {
		t.Errorf("Shaded %v, TerrainZoom %d; want 1 and 0", res.TerrainCovered, res.TerrainZoom)
	}
	if res.Attribution != "map | ground" {
		t.Errorf("credit %q, want the terrain's after the map's", res.Attribution)
	}
	if res.TerrainNotice != "the ground's full notice" {
		t.Errorf("notice %q, want the options' passed through", res.TerrainNotice)
	}
}

// Shading is a tint on slopes. Flat ground, a palette that names no shade,
// and a source with no heights all draw the map exactly as it is without
// terrain -- and only the first of them is reported as shaded and credited,
// because only there was elevation actually used.
func TestRelief_NothingToShadeChangesNothing(t *testing.T) {
	src, v := reliefMap(t)
	plain := drawWith(t, src, v, testPalette, nil).Image

	for _, c := range []struct {
		name   string
		pal    render.Palette
		hs     render.HeightSource
		shaded bool
	}{
		{"flat ground", shadingPalette(), heights{size: 8, h: func(int, int) float32 { return 120 }}, true},
		{"a palette with no shade", testPalette, ridge, false},
		{"a source with no heights", shadingPalette(), heights{}, false},
	} {
		res := drawWith(t, src, v, c.pal, c.hs)
		if !bytes.Equal(res.Image.Pix, plain.Pix) {
			t.Errorf("%s: the image differs from one without terrain", c.name)
		}
		if got := res.TerrainCovered > 0; got != c.shaded {
			t.Errorf("%s: Shaded %v", c.name, res.TerrainCovered)
		}
		if want := map[bool]string{true: "map | ground", false: "map"}[c.shaded]; res.Attribution != want {
			t.Errorf("%s: credit %q, want %q", c.name, res.Attribution, want)
		}
		if got := res.TerrainNotice != ""; got != c.shaded {
			t.Errorf("%s: notice %q", c.name, res.TerrainNotice)
		}
	}
}

// Contours are drawn by a palette naming a Contour colour and not omitting
// the role, at a zoom deep enough for them, and the result says at what
// interval.
func TestRelief_ContoursFollowThePalette(t *testing.T) {
	src := newSource()
	src.put(t, 12, 2048, 1360, wholeTile("earth", ""))
	v := tileView(t, 12, 2048, 1360, 2048, 1360, 256)
	ramp := heights{size: 64, h: func(i, j int) float32 { return float32(i) * 1e5 }}

	with := shadingPalette()
	with.Contour = color.RGBA{R: 0xa0, G: 0x60, B: 0x40, A: 0xff}
	if res := drawWith(t, src, v, with, ramp); res.ContourInterval == 0 {
		t.Error("a palette with a Contour colour drew no contours")
	}
	omitting := with
	omitting.Omitted = render.Roles(render.RoleContour)
	for name, pal := range map[string]render.Palette{"no Contour colour": shadingPalette(), "Contour omitted": omitting} {
		if res := drawWith(t, src, v, pal, ramp); res.ContourInterval != 0 {
			t.Errorf("%s: contours every %v m", name, res.ContourInterval)
		}
	}
	// A palette drawing contours and no shading still reads the terrain,
	// and owes its credit.
	only := testPalette
	only.Contour = with.Contour
	res := drawWith(t, src, v, only, ramp)
	if res.ContourInterval == 0 || res.TerrainCovered != 1 || res.Attribution != "map | ground" {
		t.Errorf("contours alone: interval %v, covered %v, credit %q", res.ContourInterval, res.TerrainCovered, res.Attribution)
	}
}

// Heights reads the ground under every pixel of a view: here a ridge, so
// the heights rise from the west edge to the middle and fall again, and a
// source with no heights is NaN everywhere rather than a flat zero.
func TestHeights_ReadsTheGroundUnderEveryPixel(t *testing.T) {
	_, v := reliefMap(t)
	v.Width, v.Height = 64, 64
	hs, err := render.Heights(ridge, v)
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 64*64 {
		t.Fatalf("%d heights for a 64 by 64 view", len(hs))
	}
	row := hs[32*64 : 33*64]
	if !(row[0] < row[16] && row[16] < row[31] && row[31] > row[48] && row[48] > row[63]) {
		t.Errorf("across the ridge: %v, %v, %v, %v, %v; want rising to the middle and falling", row[0], row[16], row[31], row[48], row[63])
	}
	none, err := render.Heights(heights{}, v)
	if err != nil {
		t.Fatal(err)
	}
	if h := none[100]; h == h {
		t.Errorf("no heights read as %v, want NaN: unknown is not zero", h)
	}
}

// Lifted, a place's name is handed back where it is and not drawn; a name
// written along a line is drawn as before. Not lifted, both are drawn and
// nothing is handed back.
func TestLiftPointLabels_HandsBackPlacesAndKeepsLines(t *testing.T) {
	src := newSource()
	place := osmbasetest.FeatureSpec{
		Type:     mvt.GeomPoint,
		Tags:     []osmbasetest.Tag{{Key: "name", Value: mvt.StringValue("Testville")}},
		Geometry: mvt.Geometry{Points: []mvt.Point{{X: 1024, Y: 1024}}},
	}
	road := osmbasetest.FeatureSpec{
		Type:     mvt.GeomLineString,
		Tags:     []osmbasetest.Tag{{Key: "name", Value: mvt.StringValue("Long Road")}},
		Geometry: mvt.Geometry{Lines: [][]mvt.Point{{{X: 0, Y: 3000}, {X: 4096, Y: 3000}}}},
	}
	src.put(t, 12, 2048, 1360, wholeTile("earth", ""),
		osmbasetest.LayerSpec{Name: "places", Features: []osmbasetest.FeatureSpec{place}},
		osmbasetest.LayerSpec{Name: "roads", Features: []osmbasetest.FeatureSpec{road}})
	v := tileView(t, 12, 2048, 1360, 2048, 1360, 256)
	st := testStyle()
	st.Labels = []render.LabelRule{
		{Layer: "places", Field: "name", Placement: render.PlacePoint, MaxZoom: render.MaxRuleZoom, Priority: 10},
		{Layer: "roads", Field: "name", Placement: render.PlaceLine, MaxZoom: render.MaxRuleZoom, Priority: 5},
	}
	draw := func(lift bool) *render.Result {
		r, err := render.New(src, render.Options{Style: st, Palette: labelledPalette(), LabelFace: basicfont.Face7x13, LiftPointLabels: lift})
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.Render(context.Background(), v)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	flat, lifted := draw(false), draw(true)
	if flat.PointLabels != nil {
		t.Errorf("not lifted, labels handed back: %+v", flat.PointLabels)
	}
	if len(lifted.PointLabels) != 1 || lifted.PointLabels[0].Text != "Testville" {
		t.Fatalf("lifted: %+v, want Testville alone", lifted.PointLabels)
	}
	// Where it was, a quarter of the way into the tile each way.
	want, err := v.Coord(64, 64)
	if err != nil {
		t.Fatal(err)
	}
	if got := lifted.PointLabels[0].At; math.Abs(got.Lat-want.Lat) > 0.002 || math.Abs(got.Lon-want.Lon) > 0.002 {
		t.Errorf("Testville handed back at %v, want about %v", got, want)
	}
	// Drawn into the image only when not lifted: the place's ink is in the
	// flat image near it, and not in the lifted one; the road's name is in both.
	inkNear := func(img *image.RGBA, x0, y0, x1, y1 int) bool {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				// Dark ink: a name written along a line is turned and
				// averaged down, so its pixels are blends of the label ink
				// rather than exactly it.
				if c := img.RGBAAt(x, y); c.R < 0x70 && c.G < 0x70 && c.B < 0x80 {
					return true
				}
			}
		}
		return false
	}
	if !inkNear(flat.Image, 30, 50, 100, 80) || inkNear(lifted.Image, 30, 50, 100, 80) {
		t.Errorf("the place's name: drawn flat %v, drawn lifted %v; want only flat", inkNear(flat.Image, 30, 50, 100, 80), inkNear(lifted.Image, 30, 50, 100, 80))
	}
	if !inkNear(lifted.Image, 0, 175, 256, 200) {
		t.Error("lifting the places took the road's name with them")
	}
}

func labelledPalette() render.Palette {
	p := testPalette
	p.Label = color.RGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xff}
	return p
}
