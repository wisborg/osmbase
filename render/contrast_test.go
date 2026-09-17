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

// TestContrastFunctionsRejectANilColourClearly pins the message rather than
// the crash.
//
// These two took color.RGBA until the premultiplication fix moved them onto
// color.Color, and an interface can be nil where a struct cannot. That
// regression reached a consumer: a zero-valued struct of interface fields
// produced "invalid memory address" from three frames inside image/color,
// which says nothing about which colour was left unset.
//
// The panic stays -- there is no honest float64 to return, and treating nil as
// black would be worse than crashing, because a palette check against a dark
// theme would then PASS and the consumer would ship the missing ink. What is
// asserted is that the panic names the package, the function and the cause.
func TestContrastFunctionsRejectANilColourClearly(t *testing.T) {
	black := color.RGBA{A: 0xff}
	for _, c := range []struct {
		name string
		call func()
	}{
		{"ContrastRatio, first argument", func() { render.ContrastRatio(nil, black) }},
		{"ContrastRatio, second argument", func() { render.ContrastRatio(black, nil) }},
		{"ColourDistance, first argument", func() { render.ColourDistance(nil, black) }},
		{"ColourDistance, second argument", func() { render.ColourDistance(black, nil) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("a nil colour was accepted; the result would be a number nobody can trust")
				}
				msg, ok := r.(string)
				if !ok {
					t.Fatalf("panicked with %T (%v), want a string naming the cause", r, r)
				}
				for _, want := range []string{"osmbase/render", "nil colour"} {
					if !strings.Contains(msg, want) {
						t.Errorf("the panic does not mention %q: %s", want, msg)
					}
				}
			}()
			c.call()
		})
	}
}

// TestAGarishPaletteFailsEvenWhenEveryOtherRuleHolds is the chroma ceiling's
// reason for existing, and the fixture is not invented.
//
// A consumer deriving a palette from its own theme produced greens and browns
// at chroma 48 against this package's own 15 to 28. Every luminance rule held,
// every pair was distinguishable, CheckContrast returned nil, and the rendered
// map shouted over the route drawn on top of it. Saturation is a third axis:
// contrast ratio sees only luminance, and delta-E measures how far two colours
// sit from EACH OTHER rather than how far either sits from neutral, so a
// uniformly vivid palette scores perfectly on both.
func TestAGarishPaletteFailsEvenWhenEveryOtherRuleHolds(t *testing.T) {
	p := render.DarkPalette()
	garish := color.RGBA{R: 0x14, G: 0x5a, B: 0x14, A: 0xff} // chroma ~48
	p.Green = garish

	// The precondition that makes this test mean something: the substitution
	// must break ONLY the chroma rule. If it also broke a luminance rule the
	// failure would prove nothing about the ceiling.
	if c := render.ColourDistance(garish, p.Background); c < render.MinRoleSeparation {
		t.Fatalf("fixture: the garish green is only %.1f from the background, so it fails for another reason", c)
	}
	if r := render.ContrastRatio(garish, p.Background); r > render.MaxContextRatio {
		t.Fatalf("fixture: the garish green is %.2f against the background, so it fails the loudness rule too", r)
	}

	err := p.CheckContrast(render.DarkOverlay())
	if err == nil {
		t.Fatal("a palette three times more saturated than any that has been looked at passed")
	}
	if !strings.Contains(err.Error(), "chroma") {
		t.Errorf("the failure does not name saturation:\n%v", err)
	}
}

// TestOmittedRolesAreNotCheckedButCollapsedOnesStillAre is the whole point of
// saying which roles a style leaves out instead of just painting them the
// background colour.
//
// The two palettes below produce the SAME picture: in both, land, green and
// built are invisible. One says so and one does not, and they must be judged
// differently, because the check they run into was written to catch a real
// defect. A plausible one-ink edit to a consumer's theme once drove every
// derived map role to the same value, and the map came out as a solid
// rectangle while the summary reported that it had been drawn. Rule 3 exists
// to make that impossible.
//
// A style that deliberately drops the landuse fills -- black with the
// linework on it -- is indistinguishable from that collapse if the only
// evidence is the colours. So the intent goes in the data: an omitted role is
// not drawn and is not checked, and a role that merely happens to match the
// background remains a fault, with the same message it had before.
func TestOmittedRolesAreNotCheckedButCollapsedOnesStillAre(t *testing.T) {
	base := render.DarkPalette()
	overlay := render.DarkOverlay()

	collapsed := base
	collapsed.Land = base.Background
	collapsed.Green = base.Background
	collapsed.Built = base.Background

	if err := collapsed.CheckContrast(overlay); err == nil {
		t.Error("a palette whose land, green and built have collapsed into the background was accepted; that is the failure this check exists for")
	} else {
		// And it must still say WHICH roles, or the message cannot be acted
		// on by somebody who did not intend the collapse.
		for _, want := range []string{"Land", "Green", "Built"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not name %s: %v", want, err)
			}
		}
	}

	declared := collapsed
	declared.Omitted = render.Roles(render.RoleLand, render.RoleGreen, render.RoleBuilt)
	if err := declared.CheckContrast(overlay); err != nil {
		t.Errorf("a palette that declares those roles omitted was refused: %v", err)
	}

	// Omitting a role must not excuse the ones still drawn. Road is kept
	// here, so a road that has collapsed into the background is still caught
	// -- otherwise declaring one role omitted would quietly weaken the check
	// for every other.
	stillChecked := declared
	stillChecked.Road = base.Background
	if err := stillChecked.CheckContrast(overlay); err == nil {
		t.Error("a collapsed Road was accepted in a palette that omits other roles; omission must not widen into a blanket exemption")
	}
}

// TestOmitsReportsWhatThePaletteLeavesOut covers the accessor the renderer
// branches on, including the zero value -- which is every palette written
// before the field existed and must go on drawing everything.
func TestOmitsReportsWhatThePaletteLeavesOut(t *testing.T) {
	if p := render.DarkPalette(); p.Omits(render.RoleLand) || p.Omits(render.RoleRoad) {
		t.Error("a palette with no Omitted set omits something; every style predating the field draws every role")
	}
	p := render.DarkPalette()
	p.Omitted = render.Roles(render.RoleGreen)
	if !p.Omits(render.RoleGreen) {
		t.Error("Omits does not report a role that was named")
	}
	if p.Omits(render.RoleWater) {
		t.Error("Omits reports a role that was not named")
	}
}

// TestDarkLineworkPaletteCarriesItsOverlay holds the built-in linework style
// to the same standard as the other two.
//
// It matters more here than for the filled palettes, because this one sits
// close to a boundary it cannot see. Its road ink has a window of roughly 1.2
// to 1.6 against the background: below that the roads cannot be told from the
// background, and above it the overlay's accent -- the position dot -- drops
// under 3:1 against them and vanishes wherever the route crosses a road,
// which on a linework map is most of the route. An edit to make the roads
// "a bit brighter" is exactly the plausible change that breaks it.
func TestDarkLineworkPaletteCarriesItsOverlay(t *testing.T) {
	p := render.DarkLineworkPalette()
	if err := p.CheckContrast(render.DarkOverlay()); err != nil {
		t.Fatalf("the built-in linework palette does not carry the dark overlay: %v", err)
	}
	for _, r := range []render.Role{render.RoleLand, render.RoleGreen, render.RoleBuilt} {
		if !p.Omits(r) {
			t.Errorf("role %v is drawn; the point of this palette is that the landuse fills are not", r)
		}
	}
	// Water is the one fill kept, and it has to remain a fill: it is the
	// strongest orientation cue after the roads, and a linework map that drops
	// it turns a coastal route into an unplaceable squiggle.
	if p.Omits(render.RoleWater) {
		t.Error("water is omitted; it is deliberately the one filled feature this style keeps")
	}
}

// TestPaletteStaysComparable pins a property a value type should not lose
// quietly.
//
// Omitted was a slice for one version, which makes the struct containing it
// uncomparable and withdraws "==" from every consumer at once. The first
// casualty was a downstream test asserting that the same inks derive the same
// palette every run -- which is exactly the kind of claim a palette should be
// able to make about itself -- and a render cache keyed on a palette would
// have been next.
//
// Written as a comparison rather than as a comment, so that reintroducing a
// slice, a map or a pointer here fails to compile in this package instead of
// in somebody else's.
func TestPaletteStaysComparable(t *testing.T) {
	a, b := render.DarkLineworkPalette(), render.DarkLineworkPalette()
	if a != b {
		t.Error("two calls to the same built-in palette differ")
	}
	if a == render.DarkPalette() {
		t.Error("the linework palette compares equal to the filled one")
	}
	// And the set itself distinguishes membership rather than order.
	if render.Roles(render.RoleLand, render.RoleGreen) != render.Roles(render.RoleGreen, render.RoleLand) {
		t.Error("a role set depends on the order it was written in")
	}
	if render.Roles().Has(render.RoleLand) {
		t.Error("the empty set claims to hold a role; the zero value must omit nothing")
	}
}
