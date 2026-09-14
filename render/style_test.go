package render

import (
	"strings"
	"testing"
)

// TestValidate_RefusesARuleThatDrawsAtNoZoom is what the validation exists for.
//
// MinZoom and MaxZoom are both required and both inclusive, so a rule that sets
// the first and forgets the second has MaxZoom 0 below MinZoom and draws
// nowhere. Left alone that is a layer missing from a map that otherwise looks
// entirely correct, which reads as a schema problem or a data problem and sends
// the reader looking anywhere but at the style.
func TestValidate_RefusesARuleThatDrawsAtNoZoom(t *testing.T) {
	s := Style{Name: "forgetful", Rules: []Rule{
		{Layer: "roads", MinZoom: 12, Paint: Paint{Role: RoleRoad, Width: 1}},
	}}
	err := s.Validate()
	if err == nil {
		t.Fatal("Validate accepted a rule whose MaxZoom is below its MinZoom")
	}
	for _, want := range []string{"forgetful", "roads", "MaxRuleZoom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is %q, want it to mention %q", err, want)
		}
	}
}

// TestValidate_AcceptsARuleThatDrawsOnlyAtZoomZero is the other side of the
// same decision, and is why the zero value is not a sentinel for "every zoom".
// A coastline or an administrative boundary wanted only on a world view is a
// real rule, and it is spelled MinZoom 0, MaxZoom 0.
func TestValidate_AcceptsARuleThatDrawsOnlyAtZoomZero(t *testing.T) {
	s := Style{Name: "world", Rules: []Rule{
		{Layer: "earth", MinZoom: 0, MaxZoom: 0, Paint: Paint{Role: RoleLand, Fill: true}},
	}}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	r := s.Rules[0]
	if !r.appliesAt(0) || r.appliesAt(1) {
		t.Errorf("a rule bounded 0 to 0 applies at zoom 0: %v, at zoom 1: %v", r.appliesAt(0), r.appliesAt(1))
	}
}

func TestValidate_RefusesARuleThatNeitherFillsNorStrokes(t *testing.T) {
	s := Style{Name: "silent", Rules: []Rule{
		{Layer: "water", MinZoom: 0, MaxZoom: MaxRuleZoom, Paint: Paint{Role: RoleWater}},
	}}
	if err := s.Validate(); err == nil {
		t.Fatal("Validate accepted a rule that draws nothing")
	}
}

// TestRuleMatches_AMissingKindIsNotTheEmptyString keeps the distinction mvt is
// careful about.
//
// A feature with no "kind" attribute and a feature whose kind is "" are
// different features. Treating the first as the second would let a rule listing
// "" pick up everything unclassified, which is the kind of accident that shows
// up as one wrong colour over a whole city.
func TestRuleMatches_AMissingKindIsNotTheEmptyString(t *testing.T) {
	named := Rule{Kinds: []string{"lake", ""}}
	anything := Rule{}

	for _, tc := range []struct {
		name    string
		rule    Rule
		kind    string
		present bool
		want    bool
	}{
		{"listed kind", named, "lake", true, true},
		{"unlisted kind", named, "ocean", true, false},
		{"empty kind, present", named, "", true, true},
		{"no kind at all", named, "", false, false},
		{"no kinds listed, feature has one", anything, "lake", true, true},
		{"no kinds listed, feature has none", anything, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rule.matches(tc.kind, tc.present); got != tc.want {
				t.Errorf("matches(%q, %v) = %v, want %v", tc.kind, tc.present, got, tc.want)
			}
		})
	}
}

// TestMaxStrokeWidth_OnlyCountsRulesDrawingAtThisZoom pins the number the
// renderer uses to decide how far outside the image geometry is still worth
// keeping.
//
// Too small and a road whose centreline is just off the edge is culled instead
// of painting half its width onto the image, leaving a ragged strip along every
// border. Counting a rule that does not draw at this zoom only wastes work, so
// the test is about the zoom filter rather than about the maximum.
func TestMaxStrokeWidth_OnlyCountsRulesDrawingAtThisZoom(t *testing.T) {
	s := Style{Rules: []Rule{
		{Layer: "roads", MinZoom: 0, MaxZoom: 10, Paint: Paint{Role: RoleRoad, Width: 9}},
		{Layer: "roads", MinZoom: 11, MaxZoom: MaxRuleZoom, Paint: Paint{Role: RoleRoad, Width: 3}},
		{Layer: "water", MinZoom: 0, MaxZoom: MaxRuleZoom, Paint: Paint{Role: RoleWater, Fill: true}},
	}}
	if got := s.maxStrokeWidth(5); got != 9 {
		t.Errorf("maxStrokeWidth(5) = %g, want 9", got)
	}
	if got := s.maxStrokeWidth(14); got != 3 {
		t.Errorf("maxStrokeWidth(14) = %g, want 3; the wide rule stops at zoom 10", got)
	}
}

// TestBasemapStyle_IsValid checks the built-in style against its own rules,
// because it is the one style nobody passes through New by hand before the
// first picture is drawn with it.
func TestBasemapStyle_IsValid(t *testing.T) {
	if err := BasemapStyle().Validate(); err != nil {
		t.Fatalf("the built-in style does not validate: %v", err)
	}
}

// TestPaletteColour_EveryRoleHasItsOwnColour guards the switch in Palette, where
// a copy-and-paste slip would silently draw water in the land colour -- a map
// that looks fine unless you know the place.
func TestPaletteColour_EveryRoleHasItsOwnColour(t *testing.T) {
	p := LightPalette()
	roles := []Role{RoleBackground, RoleLand, RoleWater, RoleGreen, RoleBuilt, RoleRoad, RoleInk, RoleNoData}
	want := []any{p.Background, p.Land, p.Water, p.Green, p.Built, p.Road, p.Ink, p.NoData}
	for i, r := range roles {
		if got := p.colour(r); any(got) != want[i] {
			t.Errorf("colour(role %d) = %v, want %v", r, got, want[i])
		}
	}
}
