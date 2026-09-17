package render

import (
	"image"
	"strings"
	"testing"

	"golang.org/x/image/font/basicfont"

	"github.com/wisborg/osmbase/mvt"
)

func testFace() *basicfont.Face { return basicfont.Face7x13 }

func cand(text string, x, y float64, priority, rank int) candidate {
	return candidate{text: text, x: x, y: y, priority: priority, rank: rank, key: text}
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
	got := placeLabels([]candidate{
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
	got := placeLabels([]candidate{
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
	first := placeLabels(append([]candidate(nil), in...), testFace(), DefaultLabelPadding, bounds)
	for i := range 20 {
		got := placeLabels(append([]candidate(nil), in...), testFace(), DefaultLabelPadding, bounds)
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
	got := placeLabels([]candidate{
		{text: "Riverside", x: 200, y: 100, priority: 20, rank: 3, key: "tile-a"},
		{text: "Riverside", x: 201, y: 100, priority: 20, rank: 3, key: "tile-b"},
	}, testFace(), DefaultLabelPadding, bounds)

	if len(got) != 1 {
		t.Errorf("the same name from two tiles was placed %d times, want 1", len(got))
	}

	apart := placeLabels([]candidate{
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
	got := placeLabels([]candidate{
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
	got := placeLabels([]candidate{
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
