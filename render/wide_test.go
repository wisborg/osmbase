package render

import (
	"image/color"
	"math"
	"slices"
	"strings"
	"testing"

	"golang.org/x/image/font"
)

// A palette that names no road surface draws it in Road; one that does draws
// it in RoadFill.
func TestRoadSurfaceFallsBackToRoad(t *testing.T) {
	p := DarkPalette()
	if p.colour(RoleRoadFill) != p.Road {
		t.Errorf("no RoadFill: the surface is %v, want Road %v", p.colour(RoleRoadFill), p.Road)
	}
	l := LightPalette()
	if l.colour(RoleRoadFill) != l.RoadFill || l.RoadFill == l.Road {
		t.Errorf("the light surface is %v, want its own RoadFill %v", l.colour(RoleRoadFill), l.RoadFill)
	}
}

// The road surface is held apart from every colour but the land -- a pale
// surface edged in Road is how a street map draws a street -- and a label
// has to read on it, as a street's name is written there.
func TestRoadSurfaceContrast(t *testing.T) {
	ok := LightPalette()
	if err := ok.CheckContrast(LightOverlay()); err != nil {
		t.Fatalf("the light palette with its white roads: %v", err)
	}
	// The same surface as the land: only the pair that is exempt.
	same := LightPalette()
	same.RoadFill = same.Land
	if err := same.CheckContrast(LightOverlay()); err != nil {
		t.Errorf("a surface the colour of the land was refused: %v", err)
	}
	// The same surface as the built-up fill: refused, as any two roles are.
	built := LightPalette()
	built.RoadFill = built.Built
	if err := built.CheckContrast(LightOverlay()); err == nil || !strings.Contains(err.Error(), "RoadFill and Built") && !strings.Contains(err.Error(), "Built and RoadFill") {
		t.Errorf("a surface the colour of the built-up fill: %v", err)
	}
	// A surface names cannot be read on.
	dim := LightPalette()
	dim.RoadFill = color.RGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xff}
	if err := dim.CheckContrast(LightOverlay()); err == nil || !strings.Contains(err.Error(), "against the road surface") {
		t.Errorf("a grey surface under dark names: %v", err)
	}
	// Without a surface, names are haloed in the background, which they
	// are held to already: a solid road as bright as the names is no fault.
	solid := DarkPalette()
	if ContrastRatio(solid.Label, solid.Road) >= MinLabelRatio {
		t.Fatal("the test wants a solid road the names cannot be read on alone")
	}
	if err := solid.CheckContrast(DarkOverlay()); err != nil {
		t.Errorf("names over a solid road, haloed in the background: %v", err)
	}
	if solid.nameHalo() != solid.Background || ok.nameHalo() != ok.RoadFill {
		t.Errorf("halos: solid %v, light %v; want the background and the surface", solid.nameHalo(), ok.nameHalo())
	}
}

// From zoom 15 every road is drawn twice, casing then surface, every casing
// before any surface so junctions run together; below it as a line. Rail,
// paths, runways and taxiways are lines at every zoom -- widening ones, some
// of them, but lines.
func TestBasemapRoadsAreStripsCloseIn(t *testing.T) {
	s := BasemapStyle()
	var casing, surface []int
	for i, r := range s.Rules {
		if r.Layer != "roads" || !r.appliesAt(lineRoadsTo+1) || slices.Contains(r.Kinds, "rail") || slices.Contains(r.Kinds, "path") || slices.Contains(r.Kinds, "aeroway") {
			continue
		}
		switch r.Paint.Role {
		case RoleRoadFill:
			surface = append(surface, i)
		case RoleRoad:
			if len(r.Paint.Widths) > 0 {
				casing = append(casing, i)
			}
		}
		if r.appliesAt(lineRoadsTo) && len(r.Paint.Widths) > 0 {
			t.Errorf("rule %d draws a strip at zoom %d", i, lineRoadsTo)
		}
	}
	if len(casing) != 4 || len(surface) != 4 || casing[3] > surface[0] {
		t.Fatalf("casings %v, surfaces %v; want four of each, casings first", casing, surface)
	}
	at := func(z float64) projection { return projection{zoom: z, tileZoom: uint8(z), tileScale: 1} }
	for k := range casing {
		c, f := s.Rules[casing[k]].Paint, s.Rules[surface[k]].Paint
		if c.strokeWidth(at(15)) != f.strokeWidth(at(15))+2*roadEdge {
			t.Errorf("road %d: at zoom 15 an edge of %g, want %g", k, (c.strokeWidth(at(15))-f.strokeWidth(at(15)))/2, roadEdge)
		}
		if c.strokeWidth(at(18)) != f.strokeWidth(at(18))+3*roadEdge || f.strokeWidth(at(18)) < 14 {
			t.Errorf("road %d: at zoom 18 casing %g around %g; want the surface at least 14 and an edge of %g", k, c.strokeWidth(at(18)), f.strokeWidth(at(18)), 1.5*roadEdge)
		}
		// Still widening past 18, as the ground does, and held from 20.
		if !(f.strokeWidth(at(20)) > 2.5*f.strokeWidth(at(18))) || f.strokeWidth(at(22)) != f.strokeWidth(at(20)) {
			t.Errorf("road %d: %g at 18, %g at 20, %g at 22; want it widening to 20 and held", k, f.strokeWidth(at(18)), f.strokeWidth(at(20)), f.strokeWidth(at(22)))
		}
	}
}

// Buildings are drawn in their own colour where a palette names one, held
// apart from every other role, and in Built where it does not.
func TestBuildingsHaveTheirOwnColour(t *testing.T) {
	l := LightPalette()
	if l.colour(RoleBuilding) != l.Building || l.Building == l.Built {
		t.Errorf("light buildings %v, want their own %v", l.colour(RoleBuilding), l.Building)
	}
	if d := DarkPalette(); d.colour(RoleBuilding) != d.Built {
		t.Errorf("dark buildings %v, want Built %v", d.colour(RoleBuilding), d.Built)
	}
	same := LightPalette()
	same.Building = same.Built
	if err := same.CheckContrast(LightOverlay()); err == nil || !strings.Contains(err.Error(), "Building") {
		t.Errorf("buildings the colour of the built-up landuse: %v", err)
	}
	for _, r := range BasemapStyle().Rules {
		if r.Layer == "buildings" && r.Paint.Role != RoleBuilding {
			t.Errorf("the buildings rule draws in role %d", r.Paint.Role)
		}
	}
}

// Names grow with the map past zoom 17, by ratio at the continuous zoom:
// a rule's face is asked for at its own scale times the growth there.
func TestLabelsGrowWithTheMap(t *testing.T) {
	g := BasemapStyle().LabelGrowth
	for _, c := range []struct{ z, want float64 }{{14, 1}, {17, 1}, {18, 1.3}, {20, 2}, {23, 2}} {
		if got := float64(widthAt(g, c.z)); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("growth at zoom %g = %g, want %g", c.z, got, c.want)
		}
	}
	var asked []float64
	faceFor := func(s float64) font.Face { asked = append(asked, s); return testFace() }
	d := &drawer{p: projection{zoom: 18, tileZoom: 18, tileScale: 1}}
	rules := []LabelRule{{Layer: "places", MinZoom: 0, MaxZoom: MaxRuleZoom, SizeScale: 1.5}, {Layer: "roads", MinZoom: 0, MaxZoom: MaxRuleZoom}}
	d.collectLabels(rules, g, nil, 18, faceFor)
	if len(asked) != 2 || math.Abs(asked[0]-1.95) > 1e-6 || math.Abs(asked[1]-1.3) > 1e-6 {
		t.Errorf("faces asked for at %v; want 1.5×1.3 and 1×1.3", asked)
	}
	asked = nil
	d.collectLabels(rules, nil, nil, 18, faceFor)
	if len(asked) != 2 || asked[0] != 1.5 || asked[1] != 1 {
		t.Errorf("with no growth, faces asked for at %v; want 1.5 and 1", asked)
	}
}

// Close in, a railway is a solid line of ink that widens with the map with
// dashes of the road surface along it, the dashes in multiples of their own
// width so they keep their shape as it grows; further out the thin dashed
// line it always was.
func TestRailCloseIn(t *testing.T) {
	var line, base, dashes *Rule
	for i, r := range BasemapStyle().Rules {
		if !slices.Contains(r.Kinds, "rail") {
			continue
		}
		switch {
		case r.appliesAt(lineRoadsTo):
			line = &BasemapStyle().Rules[i]
		case r.Paint.Role == RoleInk:
			base = &BasemapStyle().Rules[i]
		case r.Paint.Role == RoleRoadFill:
			dashes = &BasemapStyle().Rules[i]
		}
	}
	if line == nil || base == nil || dashes == nil || line.appliesAt(lineRoadsTo+1) {
		t.Fatalf("rail rules: line %v, base %v, dashes %v", line, base, dashes)
	}
	at19 := projection{zoom: 19, tileZoom: 19, tileScale: 1}
	if w := base.Paint.strokeWidth(at19); w < 4 {
		t.Errorf("the rail at zoom 19 is %g wide, want at least 4", w)
	}
	if b, d := base.Paint.strokeWidth(at19), dashes.Paint.strokeWidth(at19); !(d < b) || len(dashes.Paint.Dash) == 0 {
		t.Errorf("dashes %g wide inside a rail %g, pattern %v", d, b, dashes.Paint.Dash)
	}
	w := dashes.Paint.strokeWidth(at19)
	if s := dashes.Paint.dashScale(at19, w); s != w {
		t.Errorf("a growing stroke's dashes scale by %g, want its width %g", s, w)
	}
	if s := line.Paint.dashScale(projection{tileScale: 1.3}, 1); s != 1.3 {
		t.Errorf("a fixed stroke's dashes scale by %g, want the tile scale", s)
	}
}
