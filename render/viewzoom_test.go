package render

import (
	"testing"

	"github.com/wisborg/osmbase/mercator"
)

// View.Zoom is what Render will report, because it is the same resolution.
// A view whose scale sits just past halfway between two zooms is the case
// that tells a rounding rule from a truncating one.
func TestViewZoomIsWhatRenderResolves(t *testing.T) {
	// 256 pixels over a quarter of the world is zoom 2; over 0.17 of it is
	// zoom 2.56, which rounds to 3 and truncates to 2.
	w, n := mercator.Unproject(0.40, 0.40)
	e, s := mercator.Unproject(0.57, 0.57)
	v := View{Bounds: Bounds{West: w, South: s, East: e, North: n}, Width: 256, Height: 256}
	tile, cont, err := v.Zoom()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := resolve(v)
	if tile != p.tileZoom || cont != p.zoom {
		t.Errorf("Zoom = %d, %v; resolve says %d, %v", tile, cont, p.tileZoom, p.zoom)
	}
	if tile != 3 {
		t.Errorf("tile zoom %d for a continuous %.2f, want the nearest, 3", tile, cont)
	}
	if _, _, err := (View{Width: 0, Height: 10, Bounds: v.Bounds}).Zoom(); err == nil {
		t.Error("a view with no pixels resolved")
	}
}
