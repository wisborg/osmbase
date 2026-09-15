package render_test

import (
	"image/color"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/render"
)

// TestBuiltInPalettesCarryTheirOverlay is the deliverable of the styling work.
//
// The claim it defends is specific: a basemap drawn from the consumer's own
// palette needs no dimming. fitdash washes third-party imagery 65% toward its
// background because that imagery is busy, mid-toned and coloured by somebody
// else, and the wash is a correction applied after the damage. A palette that
// passes here has earned the right to be drawn at full strength, and the
// reason that is a guarantee rather than a hope is this test.
func TestBuiltInPalettesCarryTheirOverlay(t *testing.T) {
	for _, c := range []struct {
		name    string
		palette render.Palette
		overlay render.Overlay
	}{
		{"light", render.LightPalette(), render.LightOverlay()},
		{"dark", render.DarkPalette(), render.DarkOverlay()},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.palette.CheckContrast(c.overlay); err != nil {
				t.Errorf("%v", err)
			}
		})
	}
}

// TestPaletteThatWouldHaveNeededDimmingFails is the negative half, and without
// it the test above is satisfied by thresholds of zero.
//
// The fixture is not invented. It is the light palette this package shipped
// before the check existed, paired with the dark theme's white route line: the
// line measured 1.11 against the map's land, which is a white stroke on a
// near-white field. It rendered, it looked like a map, and the route was
// invisible -- which is exactly the failure a wash would have been reached for.
func TestPaletteThatWouldHaveNeededDimmingFails(t *testing.T) {
	wasShipped := render.Palette{
		Background: color.RGBA{R: 0xd7, G: 0xe4, B: 0xec, A: 0xff},
		Land:       color.RGBA{R: 0xf6, G: 0xf3, B: 0xec, A: 0xff},
		Water:      color.RGBA{R: 0xcd, G: 0xdf, B: 0xea, A: 0xff},
		Green:      color.RGBA{R: 0xdf, G: 0xe8, B: 0xd4, A: 0xff},
		Built:      color.RGBA{R: 0xe9, G: 0xe4, B: 0xda, A: 0xff},
		Road:       color.RGBA{R: 0xc2, G: 0xb9, B: 0xa9, A: 0xff},
		Ink:        color.RGBA{R: 0x8d, G: 0x86, B: 0x79, A: 0xff},
		NoData:     color.RGBA{R: 0xb8, G: 0x6a, B: 0x5c, A: 0xff},
	}
	err := wasShipped.CheckContrast(render.DarkOverlay())
	if err == nil {
		t.Fatal("a light map under a white route line passed; the check is not checking")
	}
	if !strings.Contains(err.Error(), "Foreground") {
		t.Errorf("the failure does not name the overlay ink that disappears:\n%v", err)
	}
}

// TestCheckContrastReportsEveryFailure keeps the error usable for tuning.
//
// A palette fails in patterns -- every map ink against one overlay colour, or
// one map ink against all of them -- and reporting only the first turns
// choosing colours into a guessing game where each fix reveals the next.
func TestCheckContrastReportsEveryFailure(t *testing.T) {
	// Every role the same mid grey: loud against nothing, indistinguishable
	// from everything, and too close to a mid-grey dim ink.
	grey := color.RGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xff}
	flat := render.Palette{
		Background: grey, Land: grey, Water: grey, Green: grey,
		Built: grey, Road: grey, Ink: grey, NoData: grey,
	}
	err := flat.CheckContrast(render.DarkOverlay())
	if err == nil {
		t.Fatal("a palette of one colour passed")
	}
	// Six roles beyond the background, each indistinguishable from the other
	// six, is 21 pairs; the count is not the point, more than one line is.
	if n := strings.Count(err.Error(), "\n"); n < 10 {
		t.Errorf("reported %d lines, want every failure:\n%v", n+1, err)
	}
}

// TestContrastRatioMatchesTheStandard pins the metric to values from WCAG 2's
// own definition rather than to this implementation's output.
//
// Black on white is exactly 21, the maximum the formula can produce, and any
// colour against itself is exactly 1. Both are properties of the standard, so
// a rewrite of the arithmetic is checked against the specification rather than
// against what the previous arithmetic happened to return.
func TestContrastRatioMatchesTheStandard(t *testing.T) {
	black := color.RGBA{A: 0xff}
	white := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	if got := render.ContrastRatio(black, white); got < 20.99 || got > 21.01 {
		t.Errorf("black on white is %.4f, want 21", got)
	}
	if got := render.ContrastRatio(white, black); got < 20.99 || got > 21.01 {
		t.Errorf("the ratio is not symmetric: %.4f", got)
	}
	for _, c := range []color.RGBA{black, white, {R: 0x7f, G: 0x33, B: 0xaa, A: 0xff}} {
		if got := render.ContrastRatio(c, c); got < 0.999 || got > 1.001 {
			t.Errorf("%v against itself is %.4f, want 1", c, got)
		}
	}
}

// TestColourDistanceSeesHueWhereContrastRatioCannot is the reason two metrics
// are used, stated as a test so the next reader does not collapse them.
//
// Contrast ratio is a function of luminance alone. Two colours of equal
// luminance have a ratio of exactly 1 however different they look, so it
// cannot answer "can these two areas be told apart" -- and for a dark palette
// it must not be asked to, because clearing the overlay threshold pins every
// map ink into a band whose widest available ratio is 2.03.
func TestColourDistanceSeesHueWhereContrastRatioCannot(t *testing.T) {
	// Chosen to sit at near-identical luminance and opposite hue.
	blue := color.RGBA{R: 0x00, G: 0x5c, B: 0xc8, A: 0xff}
	olive := color.RGBA{R: 0x6a, G: 0x52, B: 0x00, A: 0xff}

	if r := render.ContrastRatio(blue, olive); r > 1.25 {
		t.Fatalf("fixture: the two differ by %.2f in luminance, so this proves nothing", r)
	}
	if d := render.ColourDistance(blue, olive); d < render.MinRoleSeparation {
		t.Errorf("two obviously different colours measure %.1f apart, below the threshold of %.1f",
			d, render.MinRoleSeparation)
	}
}

// TestContrastRatioUnpremultipliesItsInput pins the bug that made this
// package's agreement with fitdash aspirational rather than real.
//
// Go's color.RGBA is alpha-premultiplied by definition, so a consumer
// expressing a half-transparent ink the obvious way -- converting its own
// theme colour with color.RGBAModel -- hands over channels already scaled by
// the alpha. Read directly, a mid grey at 50% alpha measured 1.97 against the
// dark background where the colour itself is 5.46: CheckContrast would pass
// and the ink the viewer actually sees would fail.
func TestContrastRatioUnpremultipliesItsInput(t *testing.T) {
	grey := color.NRGBA{R: 0x8a, G: 0x8a, B: 0x8a, A: 0xff}
	half := color.NRGBA{R: 0x8a, G: 0x8a, B: 0x8a, A: 0x80}
	bg := render.DarkPalette().Background

	full := render.ContrastRatio(grey, bg)
	premultiplied := render.ContrastRatio(color.RGBAModel.Convert(half), bg)

	// The colour is the same; only the alpha differs. A ratio is a property of
	// the colour, so the two must agree -- but not exactly, and the tolerance
	// is the honest part of this test. Premultiplying 0x8a by an alpha of 0x80
	// gives 0x45, and 0x45 does not divide back to 0x8a: the round trip
	// through eight-bit channels loses about a level, which moves the ratio by
	// a few hundredths. Exact agreement is unavailable at any implementation
	// quality, so the assertion is that the two are the same NUMBER rather
	// than the same bits. Before the fix they were 5.46 and 1.97.
	if diff := full - premultiplied; diff > 0.15 || diff < -0.15 {
		t.Errorf("the same grey measured %.2f opaque and %.2f at half alpha; "+
			"the premultiplied channels are being read as the colour", full, premultiplied)
	}
}

// TestTheHatchIsHeldToItsOwnConstraints records which of the four rules apply
// to NoData and which deliberately does not, because it was in NONE of them
// and the obvious repair -- adding it everywhere -- is unsatisfiable.
//
// It must stand clear of the background, since a gap painted in something
// background-ish is indistinguishable from ocean and from a crash. It must not
// be mistakable for a map role. It is NOT held to the overlay threshold: a gap
// is filled with the background and then struck with diagonal lines a sixth of
// their spacing wide, so a route crossing one lies on background for seven
// eighths of its length. Requiring 3:1 against the hatch ink would price in a
// solid fill that never happens, and cannot be satisfied anyway, because the
// hatch must also stand 3:1 clear of the background while the overlay inks
// span the range between the two.
func TestTheHatchIsHeldToItsOwnConstraints(t *testing.T) {
	p := render.DarkPalette()

	if r := render.ContrastRatio(p.NoData, p.Background); r < render.MinNoDataRatio {
		t.Errorf("the hatch is %.2f from the background, below %.2f", r, render.MinNoDataRatio)
	}

	// Sunk into the background, it must fail.
	invisible := p
	invisible.NoData = color.RGBA{R: 0x10, G: 0x16, B: 0x1c, A: 0xff}
	if err := invisible.CheckContrast(render.DarkOverlay()); err == nil {
		t.Error("a hatch the colour of the background passed; a gap would read as ocean")
	} else if !strings.Contains(err.Error(), "NoData") {
		t.Errorf("the failure does not name the hatch:\n%v", err)
	}

	// But the shipped hatch is close to the overlay inks, and that is allowed.
	if r := render.ContrastRatio(p.NoData, render.DarkOverlay().Dim); r >= render.MinOverlayRatio {
		t.Log("note: the shipped hatch happens to clear the overlay threshold; " +
			"the point of this test is that it is not REQUIRED to")
	}
}
