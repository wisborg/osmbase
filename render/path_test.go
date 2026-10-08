package render

import (
	"image/color"
	"slices"
	"testing"
)

// A palette naming no Path draws paths in Ink, as every palette did before
// the role: a consumer's own palette is unchanged by it.
func TestPath_APaletteNamingNoneDrawsPathsInInk(t *testing.T) {
	p := LightPalette()
	p.Path = color.RGBA{}
	if p.colour(RolePath) != p.Ink {
		t.Errorf("paths drawn in %v, want the ink %v", p.colour(RolePath), p.Ink)
	}
}

// Every built-in palette names a path ink of its own, apart from the rail
// and boundaries' ink, and passes the contrast rules with it against the
// overlay inks it was tuned for -- a route drawn along a trail still reads
// over it.
func TestPath_EveryBuiltInPaletteHasALegalPathInk(t *testing.T) {
	for _, c := range []struct {
		name string
		p    Palette
		o    Overlay
	}{
		{"light", LightPalette(), LightOverlay()},
		{"dark", DarkPalette(), DarkOverlay()},
		{"dark-linework", DarkLineworkPalette(), DarkOverlay()},
		{"outdoors", OutdoorsPalette(), OutdoorsOverlay()},
	} {
		if c.p.Path == (color.RGBA{}) || c.p.Path == c.p.Ink {
			t.Errorf("%s: path ink %v, want one of its own", c.name, c.p.Path)
		}
		if err := c.p.CheckContrast(c.o); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// A path widens with the map, as a road does: under a pixel when a town is
// in view, several when a map is close enough to be of a walk. It was 0.7
// pixels at every zoom, and vanished from a flyover of a coastal walk.
func TestPath_WidensWithTheMap(t *testing.T) {
	s := BasemapStyle()
	i := slices.IndexFunc(s.Rules, func(r Rule) bool { return r.Layer == "roads" && slices.Equal(r.Kinds, []string{"path"}) })
	if i < 0 {
		t.Fatal("no path rule")
	}
	paint := s.Rules[i].Paint
	if paint.Role != RolePath || len(paint.Dash) == 0 {
		t.Errorf("paths drawn in role %v, dash %v; want RolePath, dashed", paint.Role, paint.Dash)
	}
	at := func(z float64) projection { return projection{zoom: z, tileZoom: uint8(z), tileScale: 1} }
	if w12, w18 := paint.strokeWidth(at(12)), paint.strokeWidth(at(18)); w12 > 1 || w18 < 3 {
		t.Errorf("a path is %g pixels at zoom 12 and %g at 18; want under 1 and at least 3", w12, w18)
	}
}
