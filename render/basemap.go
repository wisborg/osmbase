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
		Background: color.RGBA{R: 0xd7, G: 0xe4, B: 0xec, A: 0xff},
		Land:       color.RGBA{R: 0xf6, G: 0xf3, B: 0xec, A: 0xff},
		Water:      color.RGBA{R: 0xcd, G: 0xdf, B: 0xea, A: 0xff},
		Green:      color.RGBA{R: 0xdf, G: 0xe8, B: 0xd4, A: 0xff},
		Built:      color.RGBA{R: 0xe9, G: 0xe4, B: 0xda, A: 0xff},
		Road:       color.RGBA{R: 0xc2, G: 0xb9, B: 0xa9, A: 0xff},
		Ink:        color.RGBA{R: 0x8d, G: 0x86, B: 0x79, A: 0xff},
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
		Background: color.RGBA{R: 0x0e, G: 0x16, B: 0x1d, A: 0xff},
		Land:       color.RGBA{R: 0x1a, G: 0x20, B: 0x27, A: 0xff},
		Water:      color.RGBA{R: 0x11, G: 0x1d, B: 0x28, A: 0xff},
		Green:      color.RGBA{R: 0x1c, G: 0x27, B: 0x20, A: 0xff},
		Built:      color.RGBA{R: 0x23, G: 0x2a, B: 0x32, A: 0xff},
		Road:       color.RGBA{R: 0x41, G: 0x4b, B: 0x55, A: 0xff},
		Ink:        color.RGBA{R: 0x5c, G: 0x67, B: 0x72, A: 0xff},
		NoData:     color.RGBA{R: 0x7a, G: 0x3f, B: 0x36, A: 0xff},
	}
}
