package render

import (
	"cmp"
	"image"
	"image/color"
	"slices"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/wisborg/osmbase/mvt"
)

// Placement is how a label sits relative to the feature it names.
type Placement uint8

const (
	// PlacePoint centres the text on a point feature. Every label this
	// package currently draws is one of these: the places layer is points.
	PlacePoint Placement = iota

	// PlaceLine runs the text along a line feature, for river and road names.
	//
	// Declared but not yet drawn. It is here because it changes the shape of
	// everything around it -- a line label needs a path to follow, a decision
	// about which of several segments to sit on, and repetition along a long
	// way -- and a LabelRule that could only ever mean "point" would have to
	// be widened later in a way that touched every rule written against it.
	PlaceLine
)

// LabelRule selects features to write names for.
//
// The density control is mostly NOT here. Every feature in these tile schemas
// carries its own min_zoom -- the zoom at which the producer thinks it should
// start being shown -- and honouring that is what keeps a city visible from
// far out while a hamlet appears only when the map is close. MinZoom below is
// the RULE's own floor on top of that, for a class of label that should not
// appear at a given zoom however the data feels about an individual feature:
// road names at anything but the deepest zooms are the case this exists for.
type LabelRule struct {
	Layer string

	// Kinds selects by the kind attribute. Empty means every kind in the
	// layer.
	Kinds []string

	// Field is the tag holding the text. "name" in every schema this reads.
	Field string

	Placement Placement

	// MinZoom and MaxZoom bound the rule itself, inclusive.
	MinZoom, MaxZoom uint8

	// Priority orders labels against labels from OTHER rules when they
	// collide, higher first. Within one rule the data decides -- see
	// labelRank.
	Priority int
}

// appliesAt reports whether this rule runs at a zoom.
func (r *LabelRule) appliesAt(z uint8) bool { return z >= r.MinZoom && z <= r.MaxZoom }

// matches reports whether a feature is one this rule labels.
func (r *LabelRule) matches(f *mvt.Feature) bool {
	if len(r.Kinds) == 0 {
		return true
	}
	kind, ok := f.Tags["kind"]
	if !ok {
		return false
	}
	// Text rather than String: String formats a value of any kind, so a
	// numeric kind attribute would stringify into something that could match
	// a listed name by accident. Text says whether it was a string at all.
	name, ok := kind.Text()
	if !ok {
		return false
	}
	return slices.Contains(r.Kinds, name)
}

// candidate is one label that could be drawn, before anything has decided
// whether it fits.
type candidate struct {
	text string
	// x and y are where the feature is, in surface pixels.
	x, y float64

	// priority comes from the rule and rank from the feature's own data.
	// Compared in that order, so a place always outranks a road name however
	// important the road.
	priority int
	rank     int

	// key breaks a tie that priority and rank cannot, and exists only so that
	// the same input draws the same picture every run. Two features with the
	// same name and importance are otherwise ordered by whatever sequence the
	// tiles happened to be walked in.
	key string
}

// labelRank is how important a feature is among others from the same rule.
//
// Read from the data rather than invented. min_zoom is the producer's own
// statement of importance -- a thing shown from further out is a bigger thing
// -- and it is inverted here so that smaller min_zoom sorts first.
// population_rank separates places that appear at the same zoom.
//
// A feature carrying neither ranks last rather than first: an unranked label
// competing with a ranked one should lose, because the ranked one is known to
// matter and the unranked one is merely not known not to.
func labelRank(f *mvt.Feature) int {
	rank := 0
	if v, ok := f.Tags["min_zoom"]; ok {
		if z, isNum := v.Float64(); isNum {
			rank += (32 - int(z)) * 100
		}
	}
	if v, ok := f.Tags["population_rank"]; ok {
		if pr, isNum := v.Float64(); isNum {
			rank += int(pr)
		}
	}
	return rank
}

// placeLabels chooses which of the candidates to draw and returns them in
// draw order.
//
// Greedy, in priority order, rejecting anything that overlaps something
// already placed. Greedy is the right shape here rather than a compromise:
// the alternative is an optimisation over which SET of labels to show, and
// the thing being optimised -- how useful a map is -- is not something this
// package can score. Taking the most important label every time and dropping
// what will not fit around it is a rule a reader can predict.
//
// Deterministic throughout. Candidates are sorted on a total order before
// placement, so the same tiles produce the same labels in the same positions
// every run, which the whole renderer is required to do.
func placeLabels(cands []candidate, face font.Face, pad int, bounds image.Rectangle) []placed {
	slices.SortFunc(cands, func(a, b candidate) int {
		if c := cmp.Compare(b.priority, a.priority); c != 0 {
			return c
		}
		if c := cmp.Compare(b.rank, a.rank); c != 0 {
			return c
		}
		return cmp.Compare(a.key, b.key)
	})

	// There is deliberately no dedup by name. The case it would be for -- the
	// same place arriving twice because it sits in a neighbouring tile's
	// buffer as well as its own -- puts both copies at the same coordinate,
	// where the overlap test below already drops the second. Matching on text
	// instead would additionally suppress two genuinely different places that
	// happen to share a name, which is common and which the map should show
	// both of.
	var out []placed
	for _, c := range cands {
		box := labelBox(c, face, pad)
		if !box.In(bounds) {
			// Partly off the edge. Dropped rather than nudged inward: a label
			// pulled to fit no longer sits on the thing it names, and a name
			// in the wrong place is worse than a missing one.
			continue
		}
		if slices.ContainsFunc(out, func(p placed) bool { return p.box.Overlaps(box) }) {
			continue
		}
		out = append(out, placed{text: c.text, box: box})
	}
	return out
}

// placed is a label that will be drawn, with the space it occupies.
type placed struct {
	text string
	box  image.Rectangle
}

// labelBox is the space a candidate's text would occupy, padded.
//
// The padding is what stops labels from merely not overlapping: text whose
// boxes touch is unreadable, and a map wants air between names.
func labelBox(c candidate, face font.Face, pad int) image.Rectangle {
	adv := font.MeasureString(face, c.text)
	m := face.Metrics()
	w := adv.Ceil()
	ascent, descent := m.Ascent.Ceil(), m.Descent.Ceil()

	x, y := int(c.x), int(c.y)
	return image.Rect(
		x-w/2-pad, y-ascent-pad,
		x+w/2+pad, y+descent+pad,
	)
}

// DefaultLabelPadding is the space kept clear around a label when none is
// given, in pixels.
//
// Four is about a third of the height of the default face, which is enough
// for two names to read as two names. It is small because the padding also
// decides how many labels survive a crowded view, and a value chosen to make
// one dense city look calm would empty the map everywhere else.
const DefaultLabelPadding = 4

// drawLabel writes one placed label onto the image.
func drawLabel(dst *image.RGBA, l placed, face font.Face, ink color.RGBA, pad int) {
	d := font.Drawer{Dst: dst, Src: image.NewUniform(ink), Face: face}
	d.Dot = fixed.P(l.box.Min.X+pad, l.box.Min.Y+pad+face.Metrics().Ascent.Ceil())
	d.DrawString(l.text)
}
