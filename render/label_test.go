package render

import (
	"image"
	"image/color"

	"golang.org/x/image/font"
	"strings"
	"testing"

	"golang.org/x/image/font/basicfont"

	"github.com/wisborg/osmbase/mvt"
)

func testFace() *basicfont.Face { return basicfont.Face7x13 }

func cand(text string, x, y float64, priority, rank int) candidate {
	return candidate{text: text, x: x, y: y, priority: priority, rank: rank, key: text, face: testFace()}
}

// placeLabelsT fills in the face every candidate now carries, so the tests
// below stay about placement rather than about typography.
func placeLabelsT(cands []candidate, face font.Face, pad int, bounds image.Rectangle) []placed {
	for i := range cands {
		if cands[i].face == nil {
			cands[i].face = face
		}
	}
	return placeLabels(cands, pad, bounds)
}

// TestPlaceLabels_DropsWhatWillNotFitRatherThanOverlapping is the whole job of
// the placement pass.
//
// A map with two names written across each other is worse than a map with one
// name on it: the second label destroys the first as well as itself. So the
// rule is that a label either fits or is not drawn, and the one that gives
// way is the less important one -- which has to be decided, not left to
// whichever feature the tile walk reached first.
func TestPlaceLabels_DropsWhatWillNotFitRatherThanOverlapping(t *testing.T) {
	bounds := image.Rect(0, 0, 400, 200)
	// Two names at the same point: they cannot both be drawn.
	got := placeLabelsT([]candidate{
		cand("Smallville", 200, 100, 20, 1),
		cand("Metropolis", 200, 100, 30, 1),
	}, testFace(), DefaultLabelPadding, bounds)

	if len(got) != 1 {
		t.Fatalf("placed %d labels at one point, want 1: %v", len(got), got)
	}
	if got[0].text != "Metropolis" {
		t.Errorf("kept %q, want the higher-priority Metropolis", got[0].text)
	}
}

// TestPlaceLabels_RankDecidesWithinOnePriority covers the ordering the data
// supplies rather than the rule.
//
// Two places of the same kind collide all the time -- a village beside a town
// -- and the rule they came from cannot separate them. labelRank reads the
// producer's own min_zoom and population_rank for that, and this is what
// makes those fields load-bearing rather than decorative.
func TestPlaceLabels_RankDecidesWithinOnePriority(t *testing.T) {
	bounds := image.Rect(0, 0, 400, 200)
	got := placeLabelsT([]candidate{
		cand("Hamlet", 200, 100, 20, 5),
		cand("Town", 200, 100, 20, 900),
	}, testFace(), DefaultLabelPadding, bounds)

	if len(got) != 1 || got[0].text != "Town" {
		t.Errorf("placed %v, want only the higher-ranked Town", got)
	}
}

// TestPlaceLabels_IsTheSameEveryRun is not a nicety here.
//
// The renderer's contract is that the same tiles draw the same bytes, and a
// label pass that resolved ties by map order or by whichever candidate
// arrived first would break it in a way nothing else would catch: the picture
// stays plausible, and two runs differ by one name.
func TestPlaceLabels_IsTheSameEveryRun(t *testing.T) {
	bounds := image.Rect(0, 0, 400, 200)
	// Identical priority AND rank, so only the tiebreak can separate them.
	in := []candidate{
		{text: "Eastwick", x: 200, y: 100, priority: 20, rank: 7, key: "b"},
		{text: "Westwick", x: 200, y: 100, priority: 20, rank: 7, key: "a"},
	}
	first := placeLabelsT(append([]candidate(nil), in...), testFace(), DefaultLabelPadding, bounds)
	for i := range 20 {
		got := placeLabelsT(append([]candidate(nil), in...), testFace(), DefaultLabelPadding, bounds)
		if len(got) != len(first) || got[0].text != first[0].text {
			t.Fatalf("run %d placed %v, first run placed %v: a tie must resolve the same way every time", i, got, first)
		}
	}
}

// TestPlaceLabels_TheSamePlaceTwiceIsDrawnOnce covers what tile buffers do.
//
// A place near a tile edge appears in its own tile AND in the buffer of the
// neighbour, so the label pass sees it twice at the same coordinate. Drawn
// twice it would be a name written over itself -- slightly bolder, slightly
// blurred, and impossible to attribute to anything when somebody notices.
//
// It is handled by the ordinary overlap test rather than by matching on the
// text, and the second half of this asserts that difference. Two places that
// genuinely share a name -- which is common, and which a map should show both
// of -- must survive when they are far enough apart to fit.
func TestPlaceLabels_TheSamePlaceTwiceIsDrawnOnce(t *testing.T) {
	bounds := image.Rect(0, 0, 400, 200)
	got := placeLabelsT([]candidate{
		{text: "Riverside", x: 200, y: 100, priority: 20, rank: 3, key: "tile-a"},
		{text: "Riverside", x: 201, y: 100, priority: 20, rank: 3, key: "tile-b"},
	}, testFace(), DefaultLabelPadding, bounds)

	if len(got) != 1 {
		t.Errorf("the same name from two tiles was placed %d times, want 1", len(got))
	}

	apart := placeLabelsT([]candidate{
		{text: "Newport", x: 80, y: 40, priority: 20, rank: 3, key: "a"},
		{text: "Newport", x: 320, y: 160, priority: 20, rank: 3, key: "b"},
	}, testFace(), DefaultLabelPadding, bounds)
	if len(apart) != 2 {
		t.Errorf("two different places sharing a name were placed %d times, want 2: suppressing one would hide a real place", len(apart))
	}
}

// TestPlaceLabels_PriorityBeatsRank pins the order the two comparisons are
// applied in, which nothing else here can see.
//
// Rank comes from the data and priority from the rule, and they answer
// different questions: rank says how important a feature is among others of
// its own kind, priority says which kind wins. A major road can carry a far
// higher rank than a small suburb and must still lose to it, because a name
// for the place is worth more than a name for the street when only one fits.
// Comparing rank first gives the opposite answer and looks entirely
// reasonable until a map comes out labelled with roads and no places.
func TestPlaceLabels_PriorityBeatsRank(t *testing.T) {
	bounds := image.Rect(0, 0, 400, 200)
	got := placeLabelsT([]candidate{
		cand("High Street", 200, 100, 10, 5000),
		cand("Bermondsey", 200, 100, 20, 1),
	}, testFace(), DefaultLabelPadding, bounds)

	if len(got) != 1 || got[0].text != "Bermondsey" {
		t.Errorf("placed %v, want the place rather than the road: priority is compared before rank", got)
	}
}

// TestPlaceLabels_AnythingOffTheEdgeIsDropped covers the label that would be
// cut in half by the frame.
//
// Dropped rather than nudged inward, because a label moved to fit no longer
// sits on the thing it names, and on a map a name in the wrong place is a
// false statement rather than an untidy one.
func TestPlaceLabels_AnythingOffTheEdgeIsDropped(t *testing.T) {
	bounds := image.Rect(0, 0, 400, 200)
	got := placeLabelsT([]candidate{
		cand("Centre", 200, 100, 20, 1),
		cand("Edgetown", 2, 100, 20, 1),
		cand("Topton", 200, 1, 20, 1),
	}, testFace(), DefaultLabelPadding, bounds)

	if len(got) != 1 || got[0].text != "Centre" {
		var names []string
		for _, p := range got {
			names = append(names, p.text)
		}
		t.Errorf("placed %v, want only Centre: a label crossing the frame edge is dropped", strings.Join(names, ", "))
	}
}

// TestLabelRank_ReadsTheProducersOwnImportance pins that the ordering comes
// from the data rather than from anything invented here.
//
// min_zoom is inverted deliberately: a feature shown from further out is a
// bigger thing, so a smaller min_zoom must rank higher. Getting that backwards
// would label every hamlet and drop every city, and the map would still look
// like a map.
func TestLabelRank_ReadsTheProducersOwnImportance(t *testing.T) {
	city := &mvt.Feature{Tags: map[string]mvt.Value{
		"min_zoom": mvt.SintValue(8), "population_rank": mvt.SintValue(11),
	}}
	suburb := &mvt.Feature{Tags: map[string]mvt.Value{
		"min_zoom": mvt.SintValue(13), "population_rank": mvt.SintValue(2),
	}}
	unknown := &mvt.Feature{Tags: map[string]mvt.Value{}}

	if labelRank(city) <= labelRank(suburb) {
		t.Error("a city ranks no higher than a suburb; min_zoom must be inverted, since a thing shown from further out is a bigger thing")
	}
	if labelRank(unknown) >= labelRank(suburb) {
		t.Error("a feature carrying no importance at all outranks one that does; an unranked label should lose, because the ranked one is KNOWN to matter")
	}
}

// TestLineAnchor_TakesTheMidpointOfTheLongestPartByDISTANCE covers where a
// line's name is written, which is two decisions rather than one.
//
// A feature arrives as several parts when a tile cut it, so the longest part
// is taken: pinning a name to the two-pixel fragment of a road that clipped a
// tile corner would put it at the edge of the view on a stub nobody can see
// is a road. And within that part the midpoint is found by walking the
// distance rather than by taking the middle vertex, because a line that
// curves and then runs straight has its points bunched at one end -- its
// middle vertex can sit a long way from its middle.
func TestLineAnchor_TakesTheMidpointOfTheLongestPartByDistance(t *testing.T) {
	// A short stub and a long line. The long one runs 0..1000 on x with its
	// vertices crowded into the first tenth.
	long := []mvt.Point{{X: 0, Y: 50}, {X: 20, Y: 50}, {X: 40, Y: 50}, {X: 60, Y: 50}, {X: 1000, Y: 50}}
	stub := []mvt.Point{{X: 0, Y: 900}, {X: 8, Y: 900}}

	x, y, ok := lineAnchor([][]mvt.Point{stub, long})
	if !ok {
		t.Fatal("no anchor found for a feature with two parts")
	}
	if y != 50 {
		t.Errorf("anchor is at y=%d, want 50: it should sit on the long part, not the stub", y)
	}
	if x < 400 || x > 600 {
		t.Errorf("anchor is at x=%d, want near the midpoint 500: the middle VERTEX is at x=40, so this is taking the vertex rather than walking the distance", x)
	}
}

// TestLineAnchor_HasNothingToSayAboutAnEmptyGeometry pins the case that would
// otherwise put a name at the origin.
func TestLineAnchor_HasNothingToSayAboutAnEmptyGeometry(t *testing.T) {
	for _, c := range []struct {
		name  string
		lines [][]mvt.Point
	}{
		{"no parts", nil},
		{"an empty part", [][]mvt.Point{{}}},
		{"a part with one point", [][]mvt.Point{{{X: 5, Y: 5}}}},
	} {
		if _, _, ok := lineAnchor(c.lines); ok {
			t.Errorf("%s produced an anchor; a name would be drawn at 0,0 or on a line with no length", c.name)
		}
	}
}

// TestPlaceLabels_OncePerNameIsARulesChoiceAndNotThePassesHabit is the
// difference between a road and a place, held as a property.
//
// A road is cut into a feature per tile and often several within one, so a
// street crossing the view arrives as a dozen features with the same name;
// drawn as they come, the map writes the name a dozen times down one road. A
// PLACE must not be treated that way -- two towns can share a name and a map
// should show both -- so this cannot be a habit of the placement pass, and
// the test asserts both halves to stop it becoming one.
func TestPlaceLabels_OncePerNameIsARulesChoiceAndNotThePassesHabit(t *testing.T) {
	bounds := image.Rect(0, 0, 600, 400)
	spread := func(once bool) []candidate {
		return []candidate{
			{text: "Vestergade", x: 100, y: 80, priority: 10, rank: 1, key: "a", once: once},
			{text: "Vestergade", x: 400, y: 300, priority: 10, rank: 1, key: "b", once: once},
		}
	}

	if got := placeLabelsT(spread(true), testFace(), DefaultLabelPadding, bounds); len(got) != 1 {
		t.Errorf("a rule asking for one label per name placed %d; a street crossing the view would be written repeatedly", len(got))
	}
	if got := placeLabelsT(spread(false), testFace(), DefaultLabelPadding, bounds); len(got) != 2 {
		t.Errorf("a rule NOT asking for that placed %d; two places sharing a name must both be shown", len(got))
	}
}

// TestLabelRule_ReadsOnlyTheGeometryItsPlacementIsFor stops a rule being
// handed features it did not ask for.
//
// The roads layer carries named POINTS as well as lines -- junctions, and in
// the water layer, fountains. A line rule handed those would label a road
// junction as though it were the road, at whatever point the junction happens
// to sit.
func TestLabelRule_ReadsOnlyTheGeometryItsPlacementIsFor(t *testing.T) {
	line := LabelRule{Placement: PlaceLine}
	point := LabelRule{Placement: PlacePoint}

	if line.labels(mvt.GeomPoint) {
		t.Error("a line rule accepts point features; a junction would be labelled as a road")
	}
	if !line.labels(mvt.GeomLineString) {
		t.Error("a line rule rejects line features")
	}
	if point.labels(mvt.GeomLineString) {
		t.Error("a point rule accepts line features")
	}
	if !point.labels(mvt.GeomPoint) {
		t.Error("a point rule rejects point features")
	}
}

// TestPlaceLabels_MeasuresEachLabelInItsOwnFace is what makes the size
// hierarchy safe.
//
// Once a place is set larger than a street, two labels competing for the same
// space are different sizes, and the box that decides the collision has to be
// measured in the face the label will actually be drawn in. Measuring both in
// one face -- the obvious shape, and what this did before rules had sizes --
// under-reserves space for the larger one, so a place name and a street name
// that "do not overlap" are drawn across each other.
func TestPlaceLabels_MeasuresEachLabelInItsOwnFace(t *testing.T) {
	big, small := testFace(), testFace()
	bounds := image.Rect(0, 0, 400, 200)

	// Same text, same point, different faces: whichever is kept, the box it
	// reserves must be the one its own face measures.
	got := placeLabels([]candidate{
		{text: "Hornsby", x: 200, y: 100, priority: 20, rank: 1, key: "a", face: big},
		{text: "Clarke Road", x: 200, y: 100, priority: 10, rank: 1, key: "b", face: small},
	}, DefaultLabelPadding, bounds)

	if len(got) != 1 {
		t.Fatalf("placed %d labels at one point, want 1", len(got))
	}
	if got[0].face == nil {
		t.Error("the placed label carries no face; it would be drawn in whatever the caller passed last")
	}
	want := labelBox(candidate{text: got[0].text, x: 200, y: 100, face: got[0].face}, got[0].face, DefaultLabelPadding)
	if got[0].box != want {
		t.Errorf("box is %v, want %v: the reserved space must be measured in the label's OWN face", got[0].box, want)
	}
}

// TestLabelInk_FallsBackWhenAPaletteNamesOnlyOne is what lets a palette
// decline the colour cue.
//
// An unset LabelMinor is a zero RGBA, which is transparent black. Read
// literally that would draw every street name as nothing at all, on exactly
// the palettes that chose to tell places from streets by size alone -- which
// is both built-in dark ones, because on a dark ground the readable floor at
// 4.5:1 and the place ink at 7.6:1 leave too little room for two colours to
// separate.
func TestLabelInk_FallsBackWhenAPaletteNamesOnlyOne(t *testing.T) {
	major := color.RGBA{R: 0x9a, G: 0xa3, B: 0xb0, A: 0xff}
	minor := color.RGBA{R: 0x58, G: 0x5e, B: 0x68, A: 0xff}

	two := Palette{Label: major, LabelMinor: minor}
	if got := labelInk(two, true); got != minor {
		t.Errorf("a palette naming two inks drew a minor label in %v, want %v", got, minor)
	}
	if got := labelInk(two, false); got != major {
		t.Errorf("a palette naming two inks drew a major label in %v, want %v", got, major)
	}

	one := Palette{Label: major}
	if got := labelInk(one, true); got != major {
		t.Errorf("a palette naming one ink drew a minor label in %v, want the ink it has, %v: the zero value is transparent black and the name would disappear", got, major)
	}
}
