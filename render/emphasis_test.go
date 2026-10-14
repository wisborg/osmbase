package render_test

import (
	"context"
	"image/color"
	"testing"

	"golang.org/x/image/font/basicfont"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/render"
)

// strongInk is the colour a border drawn to be seen is drawn in, in this
// file's palette: BoundaryLabel, unlike anything else in it.
var strongInk = color.RGBA{R: 0xff, G: 0x80, B: 0x00, A: 0xff}

// A rule for major divisions draws a region's border only where the render's
// test says the border is a major division's, is asked a point on the
// border, and draws nothing for a render with no test.
func TestAMajorDivisionsBorderIsDrawnWhereTheTestSaysSo(t *testing.T) {
	const z, x, y = 5, 17, 10
	src := newSource()
	mid := int32(testExtent / 2)
	src.put(t, z, x, y, wholeTile("earth", ""), lineLayer("boundaries", "region", mvt.Point{X: 0, Y: mid}, mvt.Point{X: testExtent, Y: mid}))
	style := testStyle()
	style.Rules = append(style.Rules, render.Rule{Layer: "boundaries", Kinds: []string{"region"}, MajorDivision: true,
		MinZoom: 0, MaxZoom: render.MaxRuleZoom, Paint: render.Paint{Role: render.RoleBoundaryStrong, Width: 8}})
	pal := testPalette
	pal.BoundaryLabel = strongInk
	w, s, e, n, err := mercator.TileBounds(z, x, y)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		major func(render.Coord) bool
		want  bool
	}{
		{"a major division", func(render.Coord) bool { return true }, true},
		{"a minor one", func(render.Coord) bool { return false }, false},
		{"no test", nil, false},
	} {
		var asked []render.Coord
		major := c.major
		if major != nil {
			major = func(at render.Coord) bool { asked = append(asked, at); return c.major(at) }
		}
		r, err := render.New(src, render.Options{Style: style, Palette: pal, MajorDivision: major})
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.Render(context.Background(), tileView(t, z, x, y, x, y, 256))
		if err != nil {
			t.Fatal(err)
		}
		got := near(res.Image.RGBAAt(128, 128), strongInk, 2)
		if got != c.want {
			t.Errorf("%s: border drawn %v, want %v", c.name, got, c.want)
		}
		for _, a := range asked {
			if a.Lon < w || a.Lon > e || a.Lat < s || a.Lat > n {
				t.Errorf("%s: asked about %v, which is not on the tile's border", c.name, a)
			}
		}
		if major != nil && len(asked) == 0 {
			t.Errorf("%s: the test was never asked", c.name)
		}
	}
}

// A place the tiles do not hold, given as an extra place, is named by the
// style's rules as a feature of its layer with its tags would be -- a
// country's name in capitals -- and only from the tile it falls in.
func TestAnExtraPlaceIsNamedAsItsLayersFeatures(t *testing.T) {
	const z, x, y = 5, 17, 10
	src := newSource()
	src.put(t, z, x, y, wholeTile("earth", ""))
	src.put(t, z, x+1, y, wholeTile("earth", ""))
	style := testStyle()
	style.Labels = []render.LabelRule{{Layer: "places", Kinds: []string{"country"}, Field: "name",
		MinZoom: 0, MaxZoom: render.MaxRuleZoom, Political: true}}
	w, s, e, n, err := mercator.TileBounds(z, x, y)
	if err != nil {
		t.Fatal(err)
	}
	at := render.Coord{Lat: (s + n) / 2, Lon: (w + e) / 2}
	tags := map[string]mvt.Value{"kind": mvt.StringValue("country"), "name": mvt.StringValue("Testland")}
	for _, c := range []struct {
		name  string
		extra []render.ExtraPlace
		want  int
	}{
		{"given", []render.ExtraPlace{{Layer: "places", At: at, Tags: tags}}, 1},
		{"in another layer", []render.ExtraPlace{{Layer: "pois", At: at, Tags: tags}}, 0},
		{"none", nil, 0},
	} {
		r, err := render.New(src, render.Options{Style: style, Palette: testPalette, LabelFace: basicfont.Face7x13,
			LiftPointLabels: true, ExtraPlaces: c.extra})
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.Render(context.Background(), tileView(t, z, x, y, x+1, y, 256))
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for _, l := range res.PointLabels {
			if l.Text != "TESTLAND" || !l.Political {
				t.Errorf("%s: named %q (political %v), want TESTLAND as a country", c.name, l.Text, l.Political)
				continue
			}
			got++
			// Where the name is centred, which a label's placement may set a
			// few pixels off its point: within a tenth of the tile.
			if d := l.At.Lat - at.Lat; d > (n-s)/10 || d < -(n-s)/10 {
				t.Errorf("%s: named at %v, want at %v", c.name, l.At, at)
			}
		}
		if got != c.want {
			t.Errorf("%s: named %d times, want %d", c.name, got, c.want)
		}
	}
}
