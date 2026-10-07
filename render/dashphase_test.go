package render_test

import (
	"context"
	"testing"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/render"
)

// A dashed line is dashed alike in every view that holds it: the pattern is
// measured along the line from where its geometry starts, not from where a
// view's edge cuts it. A view of the right half of a tile and a view of the
// whole tile draw the same dashes over the same ground -- which a flyover
// drawn from neighbouring views, or a camera moving over a map, needs not
// to shimmer. Measured from the view's edge, the half view's dashes were
// shifted by however far the edge cut into the line.
func TestDash_ThePatternIsAnchoredToTheLineNotTheView(t *testing.T) {
	src := newSource()
	// A line across tile 10/0/0, from x 0 to 4096 at mid-height.
	src.put(t, 10, 0, 0, wholeTile("earth", ""), lineLayer("roads", "path", mvt.Point{X: 0, Y: 2048}, mvt.Point{X: 4096, Y: 2048}))
	style := render.Style{Name: "dash", Schema: "test", Rules: []render.Rule{
		{Layer: "earth", MinZoom: 0, MaxZoom: render.MaxRuleZoom, Paint: render.Paint{Role: render.RoleLand, Fill: true}},
		{Layer: "roads", MinZoom: 0, MaxZoom: render.MaxRuleZoom, Paint: render.Paint{Role: render.RoleRoad, Width: 4, Dash: []float32{6, 10}}},
	}}
	r, err := render.New(src, render.Options{Style: style, Palette: testPalette})
	if err != nil {
		t.Fatal(err)
	}
	whole := tileView(t, 10, 0, 0, 0, 0, 256)
	west, south, east, north, err := mercator.TileBounds(10, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The ground from 43 pixels right of the middle to the east edge: a
	// view edge that, less the view's padding, is no whole number of the
	// pattern's periods from the line's start.
	cut := west + (east-west)*(128.0+43)/256
	half := render.View{Bounds: render.Bounds{West: cut, South: south, East: east, North: north}, Width: 256 - 128 - 43, Height: 256}

	a, err := r.Render(context.Background(), whole)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Render(context.Background(), half)
	if err != nil {
		t.Fatal(err)
	}
	on, off := 0, 0
	for x := 0; x < half.Width; x++ {
		got, want := b.Image.RGBAAt(x, 128), a.Image.RGBAAt(x+128+43, 128)
		if !near(got, want, 8) {
			t.Fatalf("pixel %d of the half view is %v; the whole view drew %v over the same ground", x, got, want)
		}
		if near(want, testPalette.Road, 8) {
			on++
		} else if near(want, testPalette.Land, 8) {
			off++
		}
	}
	if on == 0 || off == 0 {
		t.Fatalf("the line drew %d dash pixels and %d gap pixels: not a dashed line, so the comparison proves nothing", on, off)
	}
}
