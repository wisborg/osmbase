package render

import (
	"image/color"
	"slices"
	"testing"

	"golang.org/x/image/font"
)

// The place shift moves the names of places and nothing else: not a
// country's or a state's, not a street's or a river's.
func TestShiftPlaceLabels(t *testing.T) {
	base := BasemapStyle()
	shifted := base.ShiftPlaceLabels(-2)
	moved := 0
	for i, r := range shifted.Labels {
		was := base.Labels[i].ZoomBias
		switch {
		case r.Political || r.Minor || r.Layer != "places":
			if r.ZoomBias != was {
				t.Errorf("rule %d (%v) was shifted", i, r.Kinds)
			}
		default:
			if r.ZoomBias != was-2 {
				t.Errorf("rule %d (%v) at bias %d, want %d", i, r.Kinds, r.ZoomBias, was-2)
			}
			moved++
		}
	}
	if moved == 0 {
		t.Error("no place rule was shifted")
	}
	if base.Labels[1].ZoomBias != 0 {
		t.Error("the shift changed the style it was made from")
	}
	if same := base.ShiftPlaceLabels(0); &same.Labels[0] != &base.Labels[0] {
		t.Error("a shift of nothing copied the rules")
	}
}

// Countries and states are the political names; their rule is marked so.
func TestCountriesArePolitical(t *testing.T) {
	for _, r := range BasemapStyle().Labels {
		if slices.Contains(r.Kinds, "country") && !r.Political {
			t.Error("the countries' rule is not political")
		}
		if slices.Contains(r.Kinds, "city") && r.Political {
			t.Error("the cities' rule is political")
		}
	}
}

// A tracked face sets each letter apart by the tracking: a name in it
// measures wider by that much a letter.
func TestTrackedFace(t *testing.T) {
	face := testFace()
	tf := tracked(face, politicalTracking)
	plain := font.MeasureString(face, "DANMARK")
	spaced := font.MeasureString(tf, "DANMARK")
	extra := tf.(trackedFace).extra
	if spaced != plain+7*extra || extra <= 0 {
		t.Errorf("DANMARK measures %v tracked and %v plain; want 7 × %v wider", spaced, plain, extra)
	}
	if tracked(face, 0) != face {
		t.Error("no tracking wrapped the face")
	}
}

// A political name is in BoundaryLabel where the palette names one and in
// Label where it does not; a border in Boundary, or in Ink.
func TestPoliticalInks(t *testing.T) {
	p := Palette{Label: color.RGBA{R: 1, A: 255}, LabelMinor: color.RGBA{G: 1, A: 255}, Ink: color.RGBA{B: 1, A: 255}}
	if labelInk(p, false, true) != p.Label || p.colour(RoleBoundary) != p.Ink {
		t.Error("a palette with no boundary inks does not fall back to Label and Ink")
	}
	p.BoundaryLabel, p.Boundary = color.RGBA{R: 2, A: 255}, color.RGBA{R: 3, A: 255}
	if labelInk(p, false, true) != p.BoundaryLabel || labelInk(p, true, false) != p.LabelMinor || p.colour(RoleBoundary) != p.Boundary {
		t.Error("the boundary inks are not used")
	}
}

// Emphasised, a style draws a country's border in the strong ink over a
// band of land, adds a rule for major divisions' borders, and names a
// country larger than a state; the style it was made from is untouched.
func TestEmphasisePolitical(t *testing.T) {
	base := BasemapStyle()
	before := len(base.Rules)
	e := base.EmphasisePolitical()
	if len(base.Rules) != before {
		t.Fatal("the base style's rules changed")
	}
	var band, strong, major bool
	for _, r := range e.Rules {
		if r.Layer != "boundaries" {
			continue
		}
		switch {
		case r.MajorDivision && r.Paint.Role == RoleBoundaryStrong:
			major = true
		case slices.Equal(r.Kinds, []string{"country"}) && r.Paint.Role == RoleLand:
			band = true
		case slices.Equal(r.Kinds, []string{"country"}) && r.Paint.Role == RoleBoundaryStrong:
			if !band {
				t.Error("a country's border is drawn before the band of land under it")
			}
			strong = true
		case slices.Equal(r.Kinds, []string{"country"}):
			t.Errorf("a country's border is still drawn in %v", r.Paint.Role)
		}
	}
	if !band || !strong || !major {
		t.Errorf("band %v, strong border %v, major divisions %v; want all three", band, strong, major)
	}
	country, state := 0.0, 0.0
	for _, l := range e.Labels {
		if !l.Political {
			continue
		}
		if slices.Contains(l.Kinds, "country") {
			if len(l.Kinds) != 1 {
				t.Errorf("countries are named by one rule with %v", l.Kinds)
			}
			country = l.SizeScale
		}
		if slices.Contains(l.Kinds, "state") {
			state = l.SizeScale
		}
	}
	if !(country > state && state > 0) {
		t.Errorf("a country is named at %.2f and a state at %.2f; want the country larger", country, state)
	}
}

// The strong ink is the names' purple, then the borders', then Ink.
func TestRoleBoundaryStrongFallsBack(t *testing.T) {
	label, border, ink := color.RGBA{R: 1, A: 255}, color.RGBA{G: 1, A: 255}, color.RGBA{B: 1, A: 255}
	for _, c := range []struct {
		p    Palette
		want color.RGBA
	}{
		{Palette{BoundaryLabel: label, Boundary: border, Ink: ink}, label},
		{Palette{Boundary: border, Ink: ink}, border},
		{Palette{Ink: ink}, ink},
	} {
		if got := c.p.colour(RoleBoundaryStrong); got != c.want {
			t.Errorf("%+v: %v, want %v", c.p, got, c.want)
		}
	}
}
