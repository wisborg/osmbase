package render

import (
	"cmp"
	"image"
	"image/color"
	"math"
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

	// PlaceLine names a line feature -- a river, a road -- by anchoring the
	// text to a point ON that line.
	//
	// The text stays HORIZONTAL. Setting it along the curve is what a
	// cartographer would do and it is not what this does, for two reasons
	// that both point the same way here. A basemap under a route is read at a
	// glance and horizontal text is read faster than text on a slope; and
	// rotating glyphs means rendering them to a buffer and resampling it,
	// which at the size a map label is drawn looks worse than leaving it
	// straight. If curved text is ever wanted it is a change to how a placed
	// label is DRAWN, not to how one is chosen, which is why the anchor is
	// computed separately below.
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

	// ZoomBias evaluates this rule as though the map were that many zoom
	// levels deeper (positive) or shallower (negative).
	//
	// It applies to BOTH zoom tests, which is the whole point of it existing
	// rather than callers adjusting MinZoom themselves. A label appears only
	// when the rule allows it AND the feature's own min_zoom allows it, and
	// moving one without the other produces a dial that does nothing in one
	// direction: shifting only the rule earlier lets a class of road through
	// that every individual road still vetoes. Asking to be one zoom denser
	// has to mean "pretend the map is one zoom closer" for both questions or
	// it means very little.
	//
	// Set by Style.ShiftLineLabels. Zero, and therefore nothing, by default.
	ZoomBias int

	// Priority orders labels against labels from OTHER rules when they
	// collide, higher first. Within one rule the data decides -- see
	// labelRank.
	Priority int

	// SizeScale is this rule's text size relative to the caller's base face,
	// and 0 means 1.
	//
	// It is how a reader tells a PLACE from a STREET. Without it every name
	// on the map is set identically and "Hornsby" looks exactly like "Clarke
	// Road" -- the map is full of words and none of them says what kind of
	// thing it names. Size is the conventional signal for that and the one
	// that survives being glanced at, which is how a basemap under something
	// else is read.
	//
	// A scale rather than a face, because this package cannot build one: it
	// parses no fonts, deliberately. The caller turns a scale into a face
	// through Options.LabelFaceFor, and a caller that does not bother gets
	// one size for everything, which is what happened before this existed.
	SizeScale float64

	// Minor draws this rule's labels in the quieter of the two label inks.
	//
	// The distinction is what the name is OF. A place name says where the
	// reader is; a street or river name identifies a line already drawn on
	// the map. The second is useful and secondary, and saying so in colour as
	// well as in size is what lets the eye pick the places out without
	// reading the streets first.
	//
	// A bool rather than a Role field, because a Role's zero value is
	// RoleBackground -- a rule that forgot to set it would draw its names in
	// the background colour and vanish, which is the worst kind of default.
	Minor bool

	// OncePerName draws at most one label for any given name.
	//
	// It is what separates a line from a place. A road is cut into a feature
	// per tile and often several within one tile, so a street crossing the
	// view arrives as a dozen features all called the same thing, and
	// labelling each would write the name a dozen times down one road. A
	// PLACE must not do this: two towns can share a name and a map should
	// show both, which is why this is a per-rule choice and not a property of
	// the placement pass.
	OncePerName bool
}

// labels reports whether a geometry type is the one this rule's placement
// reads. A rule looking for line names must not be handed the layer's point
// features, which in the roads layer are junctions and in the water layer are
// fountains -- named, and not what the rule asked for.
func (r *LabelRule) labels(t mvt.GeomType) bool {
	switch r.Placement {
	case PlacePoint:
		return t == mvt.GeomPoint
	case PlaceLine:
		return t == mvt.GeomLineString
	}
	return false
}

// appliesAt reports whether this rule runs at a zoom, after its bias.
func (r *LabelRule) appliesAt(z uint8) bool {
	e, ok := r.effectiveZoom(z)
	return ok && e >= r.MinZoom && e <= r.MaxZoom
}

// effectiveZoom is the zoom this rule judges by: the map's, moved by the
// rule's bias and clamped to what a zoom can be.
//
// The second result is false when the bias takes it off either end, which is
// how a shift far enough in either direction turns a rule off rather than
// wrapping it around to the other extreme.
func (r *LabelRule) effectiveZoom(z uint8) (uint8, bool) {
	e := int(z) + r.ZoomBias
	if e < 0 || e > int(MaxRuleZoom) {
		return 0, false
	}
	return uint8(e), true
}

// allowsFeature reports whether this rule labels a feature at a map zoom,
// applying BOTH zoom tests: its own threshold and the feature's declared
// min_zoom, each judged at the rule's effective zoom.
//
// One function rather than two tests spread across the collection loop,
// because the two have to move together. Biasing only the rule was the first
// version of this and it produced a dial that did nothing in the denser
// direction: a whole class of road let through by the rule, and every
// individual road vetoing itself.
func (r *LabelRule) allowsFeature(f *mvt.Feature, at uint8) bool {
	e, ok := r.effectiveZoom(at)
	if !ok || e < r.MinZoom || e > r.MaxZoom {
		return false
	}
	if v, ok := f.Tags["min_zoom"]; ok {
		if z, isNum := v.Float64(); isNum && float64(e) < z {
			return false
		}
	}
	return true
}

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

	// once carries the rule's OncePerName to the placement pass.
	once bool

	// minor selects the quieter label ink. Carried per candidate for the same
	// reason the face is: two labels competing for one space can come from
	// rules that differ in both.
	minor bool

	// face is the resolved face for this label. Carried per candidate rather
	// than passed alongside, because two labels competing for the same space
	// can now be different sizes, and the box that decides the collision has
	// to be measured in the face the label will actually be drawn in.
	face font.Face
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
func placeLabels(cands []candidate, pad int, bounds image.Rectangle) []placed {
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
	// Names already drawn for rules that ask for one label each. Not a
	// property of the pass: see LabelRule.OncePerName.
	drawn := map[string]bool{}

	var out []placed
	for _, c := range cands {
		if c.once && drawn[c.text] {
			continue
		}
		box := labelBox(c, c.face, pad)
		if !box.In(bounds) {
			// Partly off the edge. Dropped rather than nudged inward: a label
			// pulled to fit no longer sits on the thing it names, and a name
			// in the wrong place is worse than a missing one.
			continue
		}
		if slices.ContainsFunc(out, func(p placed) bool { return p.box.Overlaps(box) }) {
			continue
		}
		if c.once {
			drawn[c.text] = true
		}
		out = append(out, placed{text: c.text, box: box, face: c.face, minor: c.minor})
	}
	return out
}

// placed is a label that will be drawn, with the space it occupies.
type placed struct {
	text  string
	box   image.Rectangle
	face  font.Face
	minor bool
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
func drawLabel(dst *image.RGBA, l placed, p Palette, pad int) {
	d := font.Drawer{Dst: dst, Src: image.NewUniform(labelInk(p, l.minor)), Face: l.face}
	d.Dot = fixed.P(l.box.Min.X+pad, l.box.Min.Y+pad+l.face.Metrics().Ascent.Ceil())
	d.DrawString(l.text)
}

// lineAnchor is the point on a line feature where its name is written.
//
// The midpoint of the LONGEST part, which is where a name has the most room
// either side of it and the least chance of landing on a bend or on a stub
// that barely enters the view. A feature arrives as several parts when the
// tile cut it, so taking the longest is also what stops a name being pinned
// to the two-pixel fragment of a road that clipped the corner of a tile.
//
// Length is measured in the tile's own coordinates rather than in surface
// pixels, which costs a comparison and saves transforming every point of
// every line that will not be labelled.
func lineAnchor(lines [][]mvt.Point) (x, y int32, ok bool) {
	var best []mvt.Point
	var bestLen float64
	for _, line := range lines {
		if len(line) < 2 {
			continue
		}
		var n float64
		for i := 1; i < len(line); i++ {
			dx := float64(line[i].X - line[i-1].X)
			dy := float64(line[i].Y - line[i-1].Y)
			n += math.Hypot(dx, dy)
		}
		if n > bestLen {
			best, bestLen = line, n
		}
	}
	if best == nil {
		return 0, 0, false
	}
	// Walked rather than indexed: the midpoint by DISTANCE, not the middle
	// vertex. A line whose points bunch at one end -- which is every line
	// that follows a curve then runs straight -- has its middle vertex a long
	// way from its middle.
	half := bestLen / 2
	var run float64
	for i := 1; i < len(best); i++ {
		dx := float64(best[i].X - best[i-1].X)
		dy := float64(best[i].Y - best[i-1].Y)
		seg := math.Hypot(dx, dy)
		if run+seg >= half {
			t := 0.0
			if seg > 0 {
				t = (half - run) / seg
			}
			return best[i-1].X + int32(t*dx), best[i-1].Y + int32(t*dy), true
		}
		run += seg
	}
	return best[len(best)-1].X, best[len(best)-1].Y, true
}

// labelInk is the colour a label is drawn in.
//
// A palette that names no minor ink draws every name in the one it has, which
// is what every palette did before there were two and is a choice a palette
// can still make: both built-in dark palettes decline the second ink, because
// on a dark ground the readable floor and the place ink are close enough
// together that two label colours barely separate. Without this fallback an
// unset LabelMinor would be a zero RGBA -- transparent black -- and every
// street name on those palettes would vanish.
func labelInk(p Palette, minor bool) color.RGBA {
	if minor && p.LabelMinor != (color.RGBA{}) {
		return p.LabelMinor
	}
	return p.Label
}
