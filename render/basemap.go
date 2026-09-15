package render

import "image/color"

// The built-in style and palettes.
//
// They exist so that this package draws a map with nothing supplied but a tile
// source, which is what makes it testable on its own and what makes the command
// line tool a few lines long. They are NOT the cartography this library is
// aiming at: the colours here are placeholders chosen to be legible and
// unobtrusive, and the widths are a first guess. Tuning them, and the contrast
// test that says a route drawn over this map can be seen, is a separate piece
// of work with the real palettes in front of it. See docs/architecture.md,
// "Styling".

// basemapSchema names the tile schema these rules are written against.
//
// It is recorded because the schema is somebody else's and has changed before:
// Protomaps renamed layers and moved kinds between versions once already, and a
// style written for one version against another draws a blank map rather than
// failing. A blank map with a schema string next to it is diagnosable.
const basemapSchema = "protomaps/basemap v4"

// BasemapStyle returns the built-in rule set: land, water, landcover, buildings
// and the road network, in the order they stack.
//
// The order in this list IS the map's z-order, applied across every tile of the
// view rather than within one. Moving a rule up or down moves it under or over
// every feature of every other layer everywhere, which is the point.
func BasemapStyle() Style {
	return Style{
		Name:   "basemap",
		Schema: basemapSchema,
		Rules: []Rule{
			// Land over the background. In this schema the sea is not a
			// polygon, it is the absence of one, so the background colour is
			// the ocean and this rule is what puts a continent on it.
			{
				Layer: "earth", MinZoom: 0, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleLand, Fill: true},
			},

			// Landcover is the low-zoom vegetation surface and landuse the
			// human one; they overlap in the middle zooms and both are drawn,
			// because a rule that draws nothing where the other has nothing is
			// cheaper to keep than a zoom boundary that has to be right.
			{
				Layer:   "landcover",
				Kinds:   []string{"grass", "forest", "wood", "scrub", "farmland", "wetland"},
				MinZoom: 0, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleGreen, Fill: true},
			},
			{
				Layer: "landuse",
				Kinds: []string{
					"park", "forest", "wood", "grass", "meadow", "garden",
					"nature_reserve", "recreation_ground", "village_green",
					"golf_course", "pitch", "cemetery", "allotments",
					"farmland", "scrub",
				},
				MinZoom: 0, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleGreen, Fill: true},
			},
			{
				Layer: "landuse",
				Kinds: []string{
					"residential", "neighbourhood", "industrial", "commercial",
					"retail", "military", "aerodrome", "university", "college",
					"school", "hospital", "pedestrian",
				},
				MinZoom: 11, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleBuilt, Fill: true},
			},

			// Every kind in the water layer, with no list: a lake, a bay, a
			// reservoir, a dock and a fountain are all water, and a kind this
			// list had not heard of would be the one thing on the map drawn as
			// dry land.
			{
				Layer: "water", MinZoom: 0, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleWater, Fill: true},
			},

			{
				Layer: "buildings", MinZoom: 13, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleBuilt, Fill: true},
			},

			// Roads, thinnest first, so a motorway crosses over a footpath
			// rather than being interrupted by it.
			{
				Layer: "roads", Kinds: []string{"path"},
				MinZoom: 12, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleRoad, Width: 0.7, Dash: []float32{3, 2}},
			},
			{
				Layer: "roads", Kinds: []string{"minor_road", "other"},
				MinZoom: 11, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleRoad, Width: 1.0},
			},
			{
				Layer: "roads", Kinds: []string{"medium_road"},
				MinZoom: 8, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleRoad, Width: 1.5},
			},
			{
				Layer: "roads", Kinds: []string{"major_road"},
				MinZoom: 6, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleRoad, Width: 2.0},
			},
			{
				Layer: "roads", Kinds: []string{"highway"},
				MinZoom: 4, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleRoad, Width: 2.6},
			},
			{
				Layer: "roads", Kinds: []string{"rail"},
				MinZoom: 11, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleInk, Width: 0.7, Dash: []float32{4, 3}},
			},

			{
				Layer: "boundaries", MinZoom: 0, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleInk, Width: 0.8, Dash: []float32{5, 3}},
			},
		},
	}
}

// LightPalette is a pale basemap: a map to put something else on top of.
//
// Every ink is close to the paper on purpose. The map is context, and the
// consumer's route, marker and labels have to read against all of it -- so the
// separation that matters is not between the map's own colours but between each
// of them and whatever is drawn over them.
//
// Background is the water colour because in this schema the sea is what shows
// where no land polygon was drawn. NoData is neither: it is the one colour here
// that is meant to be noticed.
func LightPalette() Palette {
	return Palette{
		Background: color.RGBA{R: 0xe2, G: 0xeb, B: 0xf3, A: 0xff},
		Land:       color.RGBA{R: 0xfa, G: 0xf7, B: 0xee, A: 0xff},
		Water:      color.RGBA{R: 0xcf, G: 0xe4, B: 0xf7, A: 0xff},
		Green:      color.RGBA{R: 0xdf, G: 0xf1, B: 0xd2, A: 0xff},
		Built:      color.RGBA{R: 0xf1, G: 0xe6, B: 0xd6, A: 0xff},
		Road:       color.RGBA{R: 0xe3, G: 0xd4, B: 0xbf, A: 0xff},
		Ink:        color.RGBA{R: 0xd0, G: 0xc0, B: 0xa8, A: 0xff},
		NoData:     color.RGBA{R: 0xb8, G: 0x6a, B: 0x5c, A: 0xff},
	}
}

// DarkPalette is the same map for a dark surround.
//
// It is not the light palette inverted. Inverting puts the brightest ink on the
// least important feature, because a light map's darkest colour is its roads
// and a dark map's brightest colour should be too -- which is the one thing
// inversion gets right and everything else it gets backwards.
func DarkPalette() Palette {
	return Palette{
		Background: color.RGBA{R: 0x0b, G: 0x12, B: 0x18, A: 0xff},
		Land:       color.RGBA{R: 0x1b, G: 0x1e, B: 0x22, A: 0xff},
		Water:      color.RGBA{R: 0x0d, G: 0x1e, B: 0x30, A: 0xff},
		Green:      color.RGBA{R: 0x12, G: 0x25, B: 0x16, A: 0xff},
		Built:      color.RGBA{R: 0x24, G: 0x22, B: 0x1f, A: 0xff},
		Road:       color.RGBA{R: 0x2b, G: 0x2b, B: 0x2e, A: 0xff},
		Ink:        color.RGBA{R: 0x33, G: 0x30, B: 0x28, A: 0xff},
		NoData:     color.RGBA{R: 0x7a, G: 0x3f, B: 0x36, A: 0xff},
	}
}

// LightOverlay and DarkOverlay are the overlay inks each built-in palette was
// tuned against.
//
// They exist so the built-ins are CHECKABLE: a palette on its own cannot pass
// or fail CheckContrast, because the question is always "against what". Without
// a stated reference, the library would ship two palettes nobody had verified
// and a test that could only be run by a consumer.
//
// They are not a recommendation and a consumer should pass its own. What they
// say is which END of the range each palette is for, and that is the thing a
// caller most often gets wrong: a light map with light overlay inks is a route
// nobody can see, which is exactly what the first light palette here did --
// a white line measured 1.11 against its land, which is invisible.
func LightOverlay() Overlay {
	return Overlay{
		Foreground: color.RGBA{R: 0x1a, G: 0x1a, B: 0x1a, A: 0xff},
		Accent:     color.RGBA{R: 0xb3, G: 0x24, B: 0x0f, A: 0xff},
		Highlight:  color.RGBA{R: 0x43, G: 0x25, B: 0x99, A: 0xff},
		Dim:        color.RGBA{R: 0x5e, G: 0x5e, B: 0x5e, A: 0xff},
	}
}

func DarkOverlay() Overlay {
	return Overlay{
		Foreground: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
		Accent:     color.RGBA{R: 0xff, G: 0x5b, B: 0x3c, A: 0xff},
		Highlight:  color.RGBA{R: 0x9a, G: 0x7c, B: 0xf0, A: 0xff},
		Dim:        color.RGBA{R: 0x8a, G: 0x8a, B: 0x8a, A: 0xff},
	}
}
