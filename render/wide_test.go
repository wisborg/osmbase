package render

import (
	"image/color"
	"strings"
	"testing"
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
// before any surface so junctions run together; below it as a line.
func TestBasemapRoadsAreStripsCloseIn(t *testing.T) {
	s := BasemapStyle()
	var casing, surface []int
	for i, r := range s.Rules {
		if r.Layer != "roads" || !r.appliesAt(lineRoadsTo+1) {
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
	at18 := projection{zoom: 18, tileZoom: 18, tileScale: 1}
	for k := range casing {
		c, f := s.Rules[casing[k]].Paint.strokeWidth(at18), s.Rules[surface[k]].Paint.strokeWidth(at18)
		if c != f+2*roadEdge || f < 14 {
			t.Errorf("at zoom 18 casing %g around a surface %g; want the surface at least 14 and an edge of %g", c, f, roadEdge)
		}
	}
}
