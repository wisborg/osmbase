package render_test

import (
	"context"
	"image"
	"image/color"
	"strings"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/render"
)

// recorder is basicfont, remembering every letter it is asked to draw.
type recorder struct {
	font.Face
	drew *strings.Builder
}

func (r recorder) Glyph(dot fixed.Point26_6, c rune) (image.Rectangle, image.Image, image.Point, fixed.Int26_6, bool) {
	r.drew.WriteRune(c)
	return r.Face.Glyph(dot, c)
}

// Options.Language reaches the labels: a place with a name in the language
// asked for is written in it, and without Language in its own.
func TestLabelsAreWrittenInTheLanguageAskedFor(t *testing.T) {

	src := newSource()
	place := osmbasetest.FeatureSpec{
		Type: mvt.GeomPoint,
		Tags: []osmbasetest.Tag{
			{Key: "kind", Value: mvt.StringValue("locality")},
			{Key: "name", Value: mvt.StringValue("Krungthep")},
			{Key: "name:en", Value: mvt.StringValue("Bangkok")},
			{Key: "name:he", Value: mvt.StringValue("בנגקוק")},
		},
		Geometry: mvt.Geometry{Points: []mvt.Point{{X: testExtent / 2, Y: testExtent / 2}}},
	}
	src.put(t, 4, 3, 5, wholeTile("earth", ""), osmbasetest.LayerSpec{Name: "places", Features: []osmbasetest.FeatureSpec{place}})
	style := testStyle()
	style.Labels = []render.LabelRule{{Layer: "places", Field: "name", Placement: render.PlacePoint, MaxZoom: render.MaxRuleZoom}}
	palette := testPalette
	palette.Label = color.RGBA{A: 0xff}
	v := tileView(t, 4, 3, 5, 3, 5, 256)

	// A right-to-left name is drawn in the order it is read in, right to
	// left: its letters reach the face last first.
	for lang, want := range map[string]string{"": "Krungthep", "en": "Bangkok", "he": "קוקגנב"} {
		var drew strings.Builder
		r, err := render.New(src, render.Options{Style: style, Palette: palette,
			LabelFace: recorder{basicfont.Face7x13, &drew}, Language: lang})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Render(context.Background(), v); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(drew.String(), want) {
			t.Errorf("language %q drew %q, want %q", lang, drew.String(), want)
		}
	}
}
