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
		Labels: placeLabelRules(),
	}
}

// placeLabelRules names settlements, and nothing else yet.
//
// # Why the density is mostly not decided here
//
// Every feature in this schema carries its own min_zoom, which is the
// producer's statement of when it is worth showing: a country from zoom 1, a
// city from 8, a neighbourhood from 13. Honouring that is what makes label
// density fall out of the data rather than out of a number somebody tuned,
// and it is why these rules span the whole zoom range instead of each being
// pinned to a band. What remains for the placement pass is the part the data
// cannot know: how much room is actually on screen.
//
// # Why the kinds are split across three rules
//
// They differ in priority, not in zoom. When two names cannot both fit, the
// bigger place should win, and "bigger" is not something min_zoom alone
// settles once a city and a suburb are both eligible. Splitting them lets the
// order be stated rather than inferred.
//
// # What is deliberately absent
//
// Roads and water carry names too -- 432 of the 466 roads in one central
// London tile -- and neither is here. Road names want line placement, which
// draws nothing yet, and want it only at the deepest zooms: a name per street
// across a five-kilometre frame is not a map, it is a wall of text over a
// route. Water wants the same placement machinery for rivers. Both are
// expected, which is why LabelRule already carries a Placement and why this
// list is a list.
func placeLabelRules() []LabelRule {
	return []LabelRule{
		{
			Layer:   "places",
			Kinds:   []string{"country", "region", "province", "state"},
			Field:   "name",
			MinZoom: 0, MaxZoom: MaxRuleZoom,
			Priority: 40,
		},
		{
			Layer:   "places",
			Kinds:   []string{"locality", "city", "town", "village", "hamlet"},
			Field:   "name",
			MinZoom: 0, MaxZoom: MaxRuleZoom,
			Priority: 30,
		},
		{
			// The granularity that tells somebody where a route actually
			// went. A suburb is a "neighbourhood" in this schema and carries
			// min_zoom 13, so it appears only once the map is close enough
			// for the name to mean something.
			Layer:   "places",
			Kinds:   []string{"macrohood", "neighbourhood", "borough", "suburb", "quarter"},
			Field:   "name",
			MinZoom: 0, MaxZoom: MaxRuleZoom,
			Priority: 20,
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
		Background: color.RGBA{R: 0xd6, G: 0xe6, B: 0xf5, A: 0xff},
		Land:       color.RGBA{R: 0xfb, G: 0xf8, B: 0xef, A: 0xff},
		Water:      color.RGBA{R: 0xb5, G: 0xd4, B: 0xf0, A: 0xff},
		Green:      color.RGBA{R: 0xd9, G: 0xef, B: 0xc8, A: 0xff},
		Built:      color.RGBA{R: 0xf2, G: 0xe4, B: 0xcd, A: 0xff},
		Road:       color.RGBA{R: 0xdc, G: 0xc3, B: 0x9f, A: 0xff},
		Ink:        color.RGBA{R: 0xc9, G: 0xae, B: 0x7e, A: 0xff},
		// Darker than anything else on this map, which is the point: a label
		// is held to a floor against the background rather than the ceiling
		// the surfaces and linework are held to. See MinLabelRatio.
		Label:  color.RGBA{R: 0x3a, G: 0x40, B: 0x4a, A: 0xff},
		NoData: color.RGBA{R: 0x8f, G: 0x2f, B: 0x20, A: 0xff},
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
		Background: color.RGBA{R: 0x08, G: 0x0e, B: 0x14, A: 0xff},
		Land:       color.RGBA{R: 0x20, G: 0x24, B: 0x2c, A: 0xff},
		Water:      color.RGBA{R: 0x0c, G: 0x21, B: 0x38, A: 0xff},
		Green:      color.RGBA{R: 0x14, G: 0x2c, B: 0x14, A: 0xff},
		Built:      color.RGBA{R: 0x2d, G: 0x26, B: 0x20, A: 0xff},
		Road:       color.RGBA{R: 0x3a, G: 0x3a, B: 0x40, A: 0xff},
		Ink:        color.RGBA{R: 0x45, G: 0x3d, B: 0x2e, A: 0xff},
		// Brighter than anything else on this map. See MinLabelRatio: text is
		// held to a floor, not to the ceiling the rest of the palette obeys.
		Label:  color.RGBA{R: 0x9a, G: 0xa3, B: 0xb0, A: 0xff},
		NoData: color.RGBA{R: 0xb4, G: 0x56, B: 0x4a, A: 0xff},
	}
}

// DarkLineworkPalette is the dark basemap with its fills taken out: the road
// network, the railways and the boundaries over near-black, with water as the
// only filled feature left.
//
// # What it is for
//
// The landuse fills are what make a basemap read as a MAP -- green for parks,
// a warmer tone for the built-up area -- and they are also the loudest thing
// on it. For a route drawn over the top they are pure background noise, and
// on a dark palette they arrive as large flat regions of olive and brown that
// dominate the frame while carrying almost no information about where the
// activity went. Dropping them leaves the linework, which is what a viewer
// actually reads a route against: the streets it followed and the railway it
// crossed.
//
// Water stays because it is the strongest orientation cue after the roads. A
// coast, a lake or a river is what makes a place recognisable, it is rarely
// dense enough to be noisy, and a linework map that drops it turns a seaside
// run into an unplaceable squiggle.
//
// # Why the roads are not brighter
//
// "Black with bright linework" is the obvious way to describe this style and
// it is not achievable, for a reason the contrast check states precisely.
// Every map ink must stand 3:1 clear of every overlay ink, and a dark
// overlay's accent -- the position dot -- sits at about 6:1 from the
// background. A road brought up to where it reads as "light" lands in the
// accent's own luminance neighbourhood, and the dot then disappears wherever
// it crosses a road, which is most of the route.
//
// Measured against DarkOverlay, the window for the road ink is roughly 1.2 to
// 1.6 against the background: below that it cannot be told from the
// background at all, above it the accent collides. These colours sit in the
// middle of that window. The style still looks nothing like the filled one --
// the fills were the whole difference -- but its linework is quiet, which is
// what a basemap is for.
func DarkLineworkPalette() Palette {
	bg := color.RGBA{R: 0x08, G: 0x0e, B: 0x14, A: 0xff}
	return Palette{
		Background: bg,
		// The three omitted roles are set to the background rather than left
		// at some other value: if anything ever clears Omitted, the palette
		// collapses and the contrast check says so loudly, which is a better
		// failure than three fills quietly reappearing.
		Land:  bg,
		Green: bg,
		Built: bg,
		Water: color.RGBA{R: 0x10, G: 0x28, B: 0x42, A: 0xff},
		Road:  color.RGBA{R: 0x2e, G: 0x32, B: 0x3a, A: 0xff},
		Ink:   color.RGBA{R: 0x3e, G: 0x34, B: 0x26, A: 0xff},
		// The linework is quiet by design and the names are not: on a map
		// stripped to its lines, the names are most of what is left to read.
		Label:   color.RGBA{R: 0x9a, G: 0xa3, B: 0xb0, A: 0xff},
		NoData:  color.RGBA{R: 0xb4, G: 0x56, B: 0x4a, A: 0xff},
		Omitted: Roles(RoleLand, RoleGreen, RoleBuilt),
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
