package render

import (
	"image"
	"image/color"
	"math"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"

	"github.com/wisborg/osmbase/mercator"
)

// Fit holds the whole rectangle, with its margin, at the image's own aspect:
// filled along one axis, centred along the other.
func TestFitHoldsTheRectangleAtTheImagesAspect(t *testing.T) {
	b := Bounds{West: 9, South: 55, East: 11, North: 56}
	v, cropped := Fit(b, 1600, 1000, MaxFitZoom)
	if cropped || v.Width != 1600 || v.Height != 1000 {
		t.Fatalf("view %+v, cropped %v", v, cropped)
	}
	bx0, by0 := mercator.Project(b.West, b.North)
	bx1, by1 := mercator.Project(b.East, b.South)
	vx0, vy0 := mercator.Project(v.Bounds.West, v.Bounds.North)
	vx1, vy1 := mercator.Project(v.Bounds.East, v.Bounds.South)
	if vx0 > bx0 || vx1 < bx1 || vy0 > by0 || vy1 < by1 {
		t.Errorf("view %+v does not hold %+v", v.Bounds, b)
	}
	if got := (vx1 - vx0) / (vy1 - vy0); math.Abs(got-1.6) > 1e-6 {
		t.Errorf("view aspect %v in the projection, want the image's 1.6", got)
	}
	fx, fy := (bx1-bx0)*(1+2*FitMargin)/(vx1-vx0), (by1-by0)*(1+2*FitMargin)/(vy1-vy0)
	if math.Abs(math.Max(fx, fy)-1) > 1e-6 {
		t.Errorf("the rectangle fills %v by %v of the view; one axis should be filled", fx, fy)
	}
}

// A small rectangle is drawn no deeper than the zoom asked for.
func TestFitStopsAtTheDeepestZoomAskedFor(t *testing.T) {
	b := Bounds{West: 10, South: 55, East: 10.001, North: 55.001}
	for _, z := range []float64{MaxFitZoom, 18} {
		v, _ := Fit(b, 1024, 768, z)
		if _, got, err := v.Zoom(); err != nil || math.Abs(got-z) > 1e-9 {
			t.Errorf("max %v: continuous zoom %v (%v)", z, got, err)
		}
	}
}

// The credit goes into the pixels, bottom right, and nothing is drawn with
// no face or no credit.
func TestDrawCreditWritesIntoThePicture(t *testing.T) {
	blank := func() *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, 300, 100))
		for i := range img.Pix {
			img.Pix[i] = 0x20
		}
		return img
	}
	img := blank()
	DrawCredit(img, `<a href="https://openstreetmap.org">&copy; OpenStreetMap</a>`, basicfont.Face7x13)
	changed := func(r image.Rectangle) bool {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if img.RGBAAt(x, y) != (color.RGBA{0x20, 0x20, 0x20, 0x20}) {
					return true
				}
			}
		}
		return false
	}
	if !changed(image.Rect(200, 80, 300, 100)) {
		t.Error("nothing drawn bottom right")
	}
	if changed(image.Rect(0, 0, 100, 50)) {
		t.Error("drawn top left")
	}
	for _, tc := range []struct {
		credit string
		face   bool
	}{{"", true}, {"(c) OpenStreetMap", false}} {
		img = blank()
		var f font.Face
		if tc.face {
			f = basicfont.Face7x13
		}
		if DrawCredit(img, tc.credit, f); changed(img.Bounds()) {
			t.Errorf("credit %q, face %v: drew something", tc.credit, tc.face)
		}
	}
}
