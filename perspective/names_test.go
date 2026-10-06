package perspective

import (
	"image"
	"image/color"
	"testing"

	"golang.org/x/image/font/basicfont"

	"github.com/wisborg/osmbase/render"
)

// inked counts the pixels in r that are the colour c.
func inked(img *image.RGBA, r image.Rectangle, c color.RGBA) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.RGBAAt(x, y) == c {
				n++
			}
		}
	}
	return n
}

// A name is drawn where its place appears, in the label ink on a halo of the
// land's colour; a second name over the first, a place off the picture and a
// place behind a hill are left out.
func TestDrawPlaceNames(t *testing.T) {
	pal := render.LightPalette()
	pal.Label = color.RGBA{R: 0xff, A: 0xff}
	face := basicfont.Face7x13

	draw := func() *Picture {
		pic, err := Render(scene(0.02, flat, quadrants), Camera{Target: centre, Distance: 2000, Pitch: 90}, Options{Width: 200, Height: 100})
		if err != nil {
			t.Fatal(err)
		}
		return pic
	}
	alone := draw()
	alone.DrawPlaceNames([]render.PointLabel{{Text: "Here", At: centre, Face: face}}, pal)
	pic := draw()
	far := render.Coord{Lat: centre.Lat + 1, Lon: centre.Lon}
	pic.DrawPlaceNames([]render.PointLabel{
		{Text: "Here", At: centre, Face: face},
		{Text: "Overlap", At: centre, Face: face},
		{Text: "Gone", At: far, Face: face},
	}, pal)
	img := pic.Image
	if n := inked(img, image.Rect(80, 40, 120, 60), pal.Label); n == 0 {
		t.Error("no name drawn at the place looked at")
	}
	if n, want := inked(img, img.Bounds(), pal.Label), inked(alone.Image, img.Bounds(), pal.Label); n != want {
		t.Errorf("%d pixels of ink, where the first name alone has %d: another name was drawn", n, want)
	}
	if n := inked(img, image.Rect(80, 40, 120, 60), pal.Land); n == 0 {
		t.Error("the name has no halo in the land's colour")
	}

	// The cone hides the place 1.8 km behind it, seen from low down.
	cone := func(e, n float64) float64 { return max(0, 600*(1-(e*e+n*n)/(1500*1500))) }
	hidden, err := Render(scene(0.05, cone, quadrants), Camera{Target: centre, Distance: 4000, Pitch: 2}, Options{Width: 300, Height: 200})
	if err != nil {
		t.Fatal(err)
	}
	behind := render.Coord{Lat: centre.Lat + 1800.0/110_574, Lon: centre.Lon}
	hidden.DrawPlaceNames([]render.PointLabel{{Text: "Behind", At: behind, Face: face}}, pal)
	if n := inked(hidden.Image, hidden.Image.Bounds(), pal.Label); n != 0 {
		t.Errorf("a place behind the hill had its name drawn: %d pixels", n)
	}
}
