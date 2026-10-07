package render_test

import (
	"context"
	"math"
	"testing"

	"golang.org/x/image/font/basicfont"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/render"
)

// Lifting every label hands back names along lines too, with the direction
// they run, and draws no name at all; and it places a name whose box runs
// past the image's edge, as the map beyond would have it, rather than
// leaving it out. Placed only where its box fitted, a tile drawn for a
// flyover had no names in a band along every edge -- the edges between
// tiles, where nobody would have placed them.
func TestLiftLabels_HandsBackEveryNameAsTheMapWouldPlaceIt(t *testing.T) {
	src := newSource()
	// A place a few pixels in from the west edge: its name, centred on it,
	// runs off the image.
	place := osmbasetest.FeatureSpec{
		Type:     mvt.GeomPoint,
		Tags:     []osmbasetest.Tag{{Key: "name", Value: mvt.StringValue("Edgeville")}},
		Geometry: mvt.Geometry{Points: []mvt.Point{{X: 64, Y: 1024}}},
	}
	// A road running north-east to south-west across the middle.
	road := osmbasetest.FeatureSpec{
		Type:     mvt.GeomLineString,
		Tags:     []osmbasetest.Tag{{Key: "name", Value: mvt.StringValue("Long Road")}},
		Geometry: mvt.Geometry{Lines: [][]mvt.Point{{{X: 4096, Y: 0}, {X: 0, Y: 4096}}}},
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
	r, err := render.New(src, render.Options{Style: st, Palette: labelledPalette(), LabelFace: basicfont.Face7x13, LiftLabels: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Render(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.PointLabels) != 1 || res.PointLabels[0].Text != "Edgeville" {
		t.Errorf("places handed back: %+v, want Edgeville, whose name runs off the image", res.PointLabels)
	}
	if len(res.LineLabels) == 0 {
		t.Fatal("no names along lines handed back")
	}
	for _, l := range res.LineLabels {
		if l.Text != "Long Road" || l.Face == nil {
			t.Errorf("handed back %+v, want Long Road with its face", l)
		}
		// The road runs from the north-east down to the south-west: on a
		// north-up map, 45° or 225° clockwise from east -- either, as the
		// name may have been turned to read left to right.
		if d := math.Mod(l.Angle*180/math.Pi+360, 180); math.Abs(d-135) > 2 {
			t.Errorf("Long Road runs at %.1f°, want along the road, 135° (or 315°) clockwise from east", l.Angle*180/math.Pi)
		}
		// On the road: its centre has latitude and longitude in the same
		// proportion through the tile, mirrored.
		x, y, err := v.Pixel(l.At)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(x+y-256) > 3 {
			t.Errorf("Long Road's name is centred at (%.1f, %.1f), off the road", x, y)
		}
	}
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			if c := res.Image.RGBAAt(x, y); c.R < 0x70 && c.G < 0x70 && c.B < 0x80 {
				t.Fatalf("label ink at (%d, %d): a name was drawn though every name was lifted", x, y)
			}
		}
	}
}
