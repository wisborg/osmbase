package render

import (
	"image/color"
	"slices"
)

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
		Rules: slices.Concat([]Rule{
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

			// Buildings in their own role, so that a building inside a
			// residential block is not the block's own colour and invisible
			// in it. See RoleBuilding.
			{
				Layer: "buildings", MinZoom: 13, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleBuilding, Fill: true},
			},

			// Roads, thinnest first, so a motorway crosses over a footpath
			// rather than being interrupted by it. Up to zoom 14 a road is a
			// line; see wideRoads for what it is closer than that.
			{
				Layer: "roads", Kinds: []string{"path"},
				MinZoom: 12, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleRoad, Width: 0.7, Dash: []float32{3, 2}},
			},
			{
				Layer: "roads", Kinds: []string{"minor_road", "other"},
				MinZoom: 11, MaxZoom: lineRoadsTo,
				Paint: Paint{Role: RoleRoad, Width: 1.0},
			},
			{
				Layer: "roads", Kinds: []string{"medium_road"},
				MinZoom: 8, MaxZoom: lineRoadsTo,
				Paint: Paint{Role: RoleRoad, Width: 1.5},
			},
			{
				Layer: "roads", Kinds: []string{"major_road"},
				MinZoom: 6, MaxZoom: lineRoadsTo,
				Paint: Paint{Role: RoleRoad, Width: 2.0},
			},
			{
				Layer: "roads", Kinds: []string{"highway"},
				MinZoom: 4, MaxZoom: lineRoadsTo,
				Paint: Paint{Role: RoleRoad, Width: 2.6},
			},
		}, wideRoads(), []Rule{
			{
				Layer: "roads", Kinds: []string{"rail"},
				MinZoom: 11, MaxZoom: lineRoadsTo,
				Paint: Paint{Role: RoleInk, Width: 0.7, Dash: []float32{4, 3}},
			},
			// Close in, a railway as a street map draws one: a solid line in
			// the ink, wider with the map, with dashes of the road surface
			// along its middle. As a thin dashed line it held at zoom 15 and
			// was lost at 19 among roads twenty pixels wide.
			{
				Layer: "roads", Kinds: []string{"rail"},
				MinZoom: lineRoadsTo + 1, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleInk, Widths: []WidthStop{{15, 1.6}, {17, 2.6}, {19, 5}, {20, 6}}},
			},
			{
				Layer: "roads", Kinds: []string{"rail"},
				MinZoom: lineRoadsTo + 1, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleRoadFill, Widths: []WidthStop{{15, 0.6}, {17, 1.1}, {19, 2.4}, {20, 3}}, Dash: []float32{5, 5}},
			},

			{
				Layer: "boundaries", MinZoom: 0, MaxZoom: MaxRuleZoom,
				Paint: Paint{Role: RoleInk, Width: 0.8, Dash: []float32{5, 3}},
			},
		}),
		Labels: placeLabelRules(),
		// Names grow from zoom 17, where streets are wide enough to carry
		// them, to twice their size at 20, roughly with the streets.
		LabelGrowth: []WidthStop{{17, 1}, {18, 1.3}, {19, 1.65}, {20, 2}},
	}
}

// lineRoadsTo is the deepest zoom a road is drawn as a line; past it, as a
// strip with edges. See wideRoads.
const lineRoadsTo = 14

// roadEdge is the width of a wide road's casing either side of its surface,
// in output pixels: one pixel, enough to be a clear edge without becoming a
// second, darker road beside the first.
const roadEdge = 1.0

// wideRoads draws the road network, from zoom 15, the way a street map does:
// each road a strip as wide as it would be on paper, its surface pale and
// its edges dark, rather than a line of one weight at every zoom.
//
// # Why
//
// A line is right far out, where a road is a thread through a district and
// its width on the ground is a fraction of a pixel. Close in it is wrong
// twice over. A street is several pixels wide on the ground at zoom 17 and
// a one-pixel line in the middle of it says it is a path; and a name written
// beside a line has nowhere to go but over the houses, where on a strip it
// sits on the road it names.
//
// # How
//
// Two passes, every road's casing before any road's surface, so that where
// two roads meet their surfaces run together and the junction reads as one
// piece of tarmac rather than two strips each boxed in its own edge. The
// surfaces go thinnest first, as the lines did, so a motorway crosses over a
// lane rather than the reverse.
//
// The widths grow with the map: doubling, or nearly, each zoom, from about
// the width of the lines they replace at zoom 15 to wide enough at 18 for a
// street's name to sit inside its edges. They are a little short of the
// ground's own doubling at the deep end on purpose. A residential street
// drawn at its true width at zoom 18 would be 25 pixels, and the blocks
// between streets would shrink to slivers on a map whose subject is
// something drawn over it.
func wideRoads() []Rule {
	type road struct {
		kinds []string
		from  uint8
		// surface widths at zooms 15 to 18, in output pixels.
		w15, w16, w17, w18 float32
	}
	roads := []road{
		{[]string{"minor_road", "other"}, 15, 2.0, 4.0, 8.0, 14.0},
		{[]string{"medium_road"}, 15, 3.0, 5.5, 10.0, 17.0},
		{[]string{"major_road"}, 15, 4.0, 7.0, 12.0, 20.0},
		{[]string{"highway"}, 15, 5.0, 8.5, 14.0, 23.0},
	}
	// Past 18 the widths keep growing, at the factor 17 to 18 grew by, to
	// zoom 20, and are held there. edges are the casing's width either side
	// at each stop.
	zooms := []float64{15, 16, 17, 18, 19, 20}
	edges := []float32{roadEdge, roadEdge, roadEdge, 1.5 * roadEdge, 2 * roadEdge, 2.5 * roadEdge}
	widths := func(r road, edged bool) []WidthStop {
		g := r.w18 / r.w17
		ws := []float32{r.w15, r.w16, r.w17, r.w18, r.w18 * g, r.w18 * g * g}
		out := make([]WidthStop, len(ws))
		for i, w := range ws {
			if edged {
				w += 2 * edges[i]
			}
			out[i] = WidthStop{zooms[i], w}
		}
		return out
	}
	var casings, surfaces []Rule
	for _, r := range roads {
		casings = append(casings, Rule{
			Layer: "roads", Kinds: r.kinds, MinZoom: lineRoadsTo + 1, MaxZoom: MaxRuleZoom,
			Paint: Paint{Role: RoleRoad, Widths: widths(r, true)},
		})
		surfaces = append(surfaces, Rule{
			Layer: "roads", Kinds: r.kinds, MinZoom: lineRoadsTo + 1, MaxZoom: MaxRuleZoom,
			Paint: Paint{Role: RoleRoadFill, Widths: widths(r, false)},
		})
	}
	return append(casings, surfaces...)
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
// # Roads and water
//
// Roads and water carry names too -- 432 of the 466 roads in one central
// London tile -- and are named only at the deeper zooms: a name per street
// across a five-kilometre frame is not a map, it is a wall of text over a
// route. Their names are written along them (PlaceLine), and a street's on
// the street, haloed in the road's surface colour (OnRoad), as a close
// map's streets are strips wide enough to hold them.
func placeLabelRules() []LabelRule {
	return []LabelRule{
		{
			Layer:   "places",
			Kinds:   []string{"country", "region", "province", "state"},
			Field:   "name",
			MinZoom: 0, MaxZoom: MaxRuleZoom,
			Priority:  40,
			SizeScale: 1.5,
		},
		{
			Layer:   "places",
			Kinds:   []string{"locality", "city", "town", "village", "hamlet"},
			Field:   "name",
			MinZoom: 0, MaxZoom: MaxRuleZoom,
			Priority:  30,
			SizeScale: 1.35,
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
			Priority:  20,
			SizeScale: 1.2,
		},

		// Water, and only water big enough to orient by. A river or a canal
		// is the strongest cue a map has after the street pattern; a named
		// creek is not, and there are a great many of them. Left unfiltered
		// this rule put fifty names on one view of a suburb, almost all of
		// them gullies and brooks, and buried the places among them.
		//
		// The excluded kinds are the reason the list is explicit rather than
		// "every kind in the layer" the way the DRAWING rule for water is:
		// stream, dock and fountain are all worth drawing and none is worth
		// naming on a map somebody is reading a route off.
		{
			Layer:     "water",
			Kinds:     []string{"river", "canal", "lake", "water"},
			Field:     "name",
			Placement: PlaceLine,
			MinZoom:   13, MaxZoom: MaxRuleZoom,
			Priority:    15,
			Minor:       true,
			OncePerName: true,
		},

		// Roads, in four tiers rather than two. The tiers are what make
		// Style.ShiftLineLabels behave like a dial instead of a switch: with
		// every road in one rule, asking for "one zoom quieter" at the zoom
		// that rule starts at removes every street name at once, which is not
		// what anybody means by quieter. Four tiers a zoom apart give the
		// shift somewhere to land.
		//
		// This is also the rule the per-rule MinZoom exists for: the features carry min_zoom values
		// from 7 upward, so honouring the data alone would start naming trunk
		// roads on a view spanning a county, where a road name tells a reader
		// nothing they can use and takes the space a place name would have
		// used. 14 is where a street is long enough on screen for its name to
		// be about that street.
		//
		// Ranked below water and places deliberately. When a suburb and the
		// road through it cannot both fit, the suburb is the more useful
		// answer: it locates the route, where the road only names a line
		// already drawn.
		//
		// OncePerName because a road arrives cut into a feature per tile and
		// often several within one, so a street crossing the view is a dozen
		// features all called the same thing.
		{
			Layer:     "roads",
			Kinds:     []string{"highway"},
			Field:     "name",
			Placement: PlaceLine,
			MinZoom:   13, MaxZoom: MaxRuleZoom,
			Priority:    12,
			Minor:       true,
			OncePerName: true,
			OnRoad:      true,
		},
		{
			Layer:     "roads",
			Kinds:     []string{"major_road"},
			Field:     "name",
			Placement: PlaceLine,
			MinZoom:   14, MaxZoom: MaxRuleZoom,
			Priority:    10,
			Minor:       true,
			OncePerName: true,
			OnRoad:      true,
		},
		{
			// The street you actually ran along, which is worth naming only
			// once the map is close enough that it is the subject rather than
			// one line among hundreds. Splitting it from the rule above --
			// same layer, same everything but the zoom and the kinds -- is
			// what lets a z14 view name the roads that cross a district while
			// a z15 view names the streets within it.
			Layer:     "roads",
			Kinds:     []string{"medium_road"},
			Field:     "name",
			Placement: PlaceLine,
			MinZoom:   15, MaxZoom: MaxRuleZoom,
			Priority:    5,
			SizeScale:   0.9,
			Minor:       true,
			OncePerName: true,
			OnRoad:      true,
		},
		{
			// The street outside a front door. Only at the zoom where that is
			// what the map is of, which is one deeper than the roads that
			// connect districts: at 15 these put about seventy names on a
			// view of one suburb, and the places drown among them.
			Layer:     "roads",
			Kinds:     []string{"minor_road"},
			Field:     "name",
			Placement: PlaceLine,
			MinZoom:   16, MaxZoom: MaxRuleZoom,
			Priority:    3,
			SizeScale:   0.9,
			Minor:       true,
			OncePerName: true,
			OnRoad:      true,
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
		Label: color.RGBA{R: 0x3a, G: 0x40, B: 0x4a, A: 0xff},
		// Lighter than Label: quieter means closer to the background,
		// whichever direction that is. It works here and does not on the dark
		// palettes -- see DarkPalette -- because a light ground leaves room
		// between "as dark as a place name" and "too pale to read", where a
		// dark ground does not.
		LabelMinor: color.RGBA{R: 0x58, G: 0x5e, B: 0x68, A: 0xff},
		NoData:     color.RGBA{R: 0x8f, G: 0x2f, B: 0x20, A: 0xff},
		// White inside a casing of Road: a close map's streets as a street
		// map draws them. Only 4.5 from Land by colour, which rule 3 of
		// CheckContrast would refuse for any other pair -- but a road's
		// surface is never seen without its edges, and its edges are Road.
		RoadFill: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
		// A warm grey, out of the band of tans Built, Road and Ink share:
		// 8.8 from the nearest of them, where a darker tan between Built and
		// Road could not get 6 from both.
		Building:  color.RGBA{R: 0xd4, G: 0xcf, B: 0xca, A: 0xff},
		Shade:     color.RGBA{R: 0x6e, G: 0x77, B: 0x86, A: 0xff},
		Highlight: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
		// A pale red-brown, the colour of contours on most printed
		// maps, and the darkest of it the overlay's accent still reads
		// on at 3:1.
		Contour: color.RGBA{R: 0xd0, G: 0xa8, B: 0x98, A: 0xff},
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
		Label: color.RGBA{R: 0x9a, G: 0xa3, B: 0xb0, A: 0xff},
		// No LabelMinor, deliberately, so street names are told from place
		// names by SIZE alone here. It was tried and it does not work on a
		// dark ground: both inks are light greys, the readable floor is 4.5:1
		// and the place ink is at 7.6, so the whole available range is one
		// step wide and the difference between the two barely reads. On a
		// light ground the same idea works, and LightPalette uses it.
		NoData:    color.RGBA{R: 0xb4, G: 0x56, B: 0x4a, A: 0xff},
		Shade:     color.RGBA{A: 0xff},
		Highlight: color.RGBA{R: 0x50, G: 0x58, B: 0x64, A: 0xff},
		Contour:   color.RGBA{R: 0x4a, G: 0x38, B: 0x30, A: 0xff},
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
		Label: color.RGBA{R: 0x9a, G: 0xa3, B: 0xb0, A: 0xff},
		// See DarkPalette: on a dark ground the two label inks are too close
		// to separate, so this leans on size alone as well.
		NoData:    color.RGBA{R: 0xb4, G: 0x56, B: 0x4a, A: 0xff},
		Omitted:   Roles(RoleLand, RoleGreen, RoleBuilt),
		Shade:     color.RGBA{A: 0xff},
		Highlight: color.RGBA{R: 0x50, G: 0x58, B: 0x64, A: 0xff},
		Contour:   color.RGBA{R: 0x4a, G: 0x38, B: 0x30, A: 0xff},
	}
}

// OutdoorsPalette is a map of the ground for terrain: richer and darker than
// the light palette -- forest a real green, land a warm paper, roads a tan
// casing round white -- shaded and contoured, in the manner of a walking
// map.
//
// It exists because the light palette cannot be that and remain what it is.
// The light palette is pale so that mid-tone and dark routes stand out on
// it, and its green is already as dark as its overlay allows: one step
// darker and the overlay's dim ink falls below 3:1 against green in shadow
// on a steep slope. A map with green forests needs the routes drawn over it
// to be darker still, and that is a different pairing -- see
// OutdoorsOverlay -- not a change to the light one.
//
// It is held to every rule the others are: no map ink louder than 4.5:1
// against the background, every role told apart from every other, chroma
// under the ceiling, and every overlay ink 3:1 clear of every map ink,
// shaded extremes included, which is what makes the overlay so dark.
func OutdoorsPalette() Palette {
	return Palette{
		Background: color.RGBA{R: 0xa7, G: 0xcb, B: 0xe8, A: 0xff},
		Land:       color.RGBA{R: 0xf1, G: 0xed, B: 0xe0, A: 0xff},
		Water:      color.RGBA{R: 0x86, G: 0xb4, B: 0xde, A: 0xff},
		// Half the light palette's luminance: a wood reads as a wood.
		// Saturated as far as MaxContextChroma allows and no further.
		Green: color.RGBA{R: 0x97, G: 0xb9, B: 0x86, A: 0xff},
		// A faint pink-grey rather than a tan: a tan light enough to sit
		// quietly ran into the land's paper, and one far enough from the
		// paper was brown enough, in shadow, to blur the roads' tan casings.
		// Turning the hue instead of the lightness keeps both apart.
		Built:      color.RGBA{R: 0xe9, G: 0xde, B: 0xdd, A: 0xff},
		Building:   color.RGBA{R: 0xcb, G: 0xbf, B: 0xb1, A: 0xff},
		Road:       color.RGBA{R: 0xbe, G: 0x9f, B: 0x78, A: 0xff},
		RoadFill:   color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
		Ink:        color.RGBA{R: 0xa6, G: 0x89, B: 0x6a, A: 0xff},
		Label:      color.RGBA{R: 0x23, G: 0x2b, B: 0x26, A: 0xff},
		LabelMinor: color.RGBA{R: 0x3d, G: 0x46, B: 0x40, A: 0xff},
		NoData:     color.RGBA{R: 0x8f, G: 0x2f, B: 0x20, A: 0xff},
		Shade:      color.RGBA{R: 0x6a, G: 0x7a, B: 0x8c, A: 0xff},
		Highlight:  color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
		// The red-brown of contours on printed maps, darker than the light
		// palette's because the ground under it is.
		Contour: color.RGBA{R: 0xb9, G: 0x80, B: 0x60, A: 0xff},
	}
}

// OutdoorsOverlay is what is drawn over the outdoors palette: deep inks,
// because they have to stand 3:1 clear of a map whose darkest ink, green in
// shadow, is mid-toned -- a navy line, a crimson dot, a purple highlight, a
// charcoal for what is quieter. Dark lines on a mid-toned map are how a
// printed walking map draws its routes.
func OutdoorsOverlay() Overlay {
	return Overlay{
		Foreground: color.RGBA{R: 0x14, G: 0x1a, B: 0x33, A: 0xff},
		Accent:     color.RGBA{R: 0x6e, G: 0x0a, B: 0x26, A: 0xff},
		Highlight:  color.RGBA{R: 0x3e, G: 0x16, B: 0x70, A: 0xff},
		Dim:        color.RGBA{R: 0x35, G: 0x35, B: 0x35, A: 0xff},
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
