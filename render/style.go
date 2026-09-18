package render

import (
	"fmt"
	"image/color"
	"slices"

	"github.com/wisborg/osmbase/mercator"
)

// MaxRuleZoom is the deepest zoom a rule can name, and is what a rule with no
// deepest zoom says.
//
// It is spelled out rather than left as a zero-value default because zoom 0 is
// a legal zoom and a rule drawing only there is a legal rule -- coastlines and
// administrative boundaries on a world view are exactly that. A MaxZoom whose
// zero value meant "forever" would make "draw this only on the world view"
// inexpressible, and would turn a forgotten field into a rule that silently
// drew everywhere. Style.Validate catches the forgotten field instead.
const MaxRuleZoom = mercator.MaxZoom

// Role is what a colour is FOR, rather than what it is.
//
// A style names roles and a caller supplies the colours, which is what lets a
// consumer with a theme hand over a palette without knowing what a landcover
// class is or which OSM tag a park has. It is also what makes the separation
// between the map and whatever is drawn over it testable: every map ink comes
// from this short list, so "no map ink is within N of the route colour" is a
// statement about eight colours rather than about a style sheet.
type Role uint8

const (
	// RoleBackground is what shows where nothing else draws.
	//
	// In a tile schema whose land is a POLYGON -- which is every schema this
	// library reads -- the absence of that polygon is the sea, so this role is
	// the ocean whatever else it is called. A palette whose background does
	// not read as water will put paper in the Pacific.
	RoleBackground Role = iota
	// RoleLand is the land polygon drawn over the background.
	RoleLand
	// RoleWater is lakes, rivers and bays.
	RoleWater
	// RoleGreen is vegetated and open landcover: parks, forest, grass, farmland.
	RoleGreen
	// RoleBuilt is the built surface: buildings, and built-up landuse.
	RoleBuilt
	// RoleRoad is the road network.
	RoleRoad
	// RoleInk is the darkest map ink: rail, boundaries, anything drawn as a
	// line over everything else.
	RoleInk
	// RoleNoData is the hatch painted where no tile covers the view.
	//
	// It is its own role and not a shade of one of the others on purpose. Its
	// whole job is to be unmistakable for map ink: a gap painted in background
	// is indistinguishable from ocean and from a crash, which is the thing the
	// hatch exists to prevent. See docs/architecture.md, "A cache miss at
	// render time".
	RoleNoData

	// RoleLabel is text drawn on the map: place names now, and the water and
	// road names a later style will ask for.
	//
	// Its own role rather than a shade of RoleInk because it is judged
	// differently. Every other map colour fills an area or draws a line, and
	// the rule for those is that they must stay quiet -- a map is background.
	// Text is not readable at the brightness an area can be: a fill at 1.8:1
	// against its background reads as a region, and eight-pixel glyphs at
	// 1.8:1 read as a smudge. A label that cannot be read is worse than no
	// label, because it is clutter that also fails to inform.
	RoleLabel

	// RoleLabelMinor is a label for something the map draws rather than
	// something it is OF: a street name, a river name.
	//
	// Its own role because size alone turned out not to be enough. With one
	// ink, a large "Hornsby" and a small "Clarke Road" are the same colour
	// and the eye still has to read both to sort them; dropping the minor
	// names toward the map lets it skip them until it wants them. It is
	// still text and still held to MinLabelRatio -- quieter than a place
	// name, never quiet enough to be unreadable.
	RoleLabelMinor
)

// Palette is the colour for each role.
//
// The fields are color.RGBA rather than color.Color because a Palette has to be
// complete: a nil interface in a role is a panic in the middle of a render,
// after the tiles have been read and before anything has been returned, and
// there is no useful thing this package could substitute. A zero RGBA is
// transparent black, which is wrong but is a picture.
type Palette struct {
	Background color.RGBA
	Land       color.RGBA
	Water      color.RGBA
	Green      color.RGBA
	Built      color.RGBA
	Road       color.RGBA
	Ink        color.RGBA
	NoData     color.RGBA

	// LabelMinor is the text for streets and water -- names of things the map
	// draws, as against names of the places it is of. A zero value falls back
	// to Label, so a palette written before this field draws every name in
	// one colour, as it did.
	LabelMinor color.RGBA

	// Label is the text drawn on the map. See RoleLabel for why it is held to
	// a different standard than the rest of the palette.
	//
	// A zero value here is transparent black, which is invisible rather than
	// wrong-looking -- so a palette written before this field existed draws no
	// labels, which is exactly what it did before. Styles that want labels
	// set it and say so in their own label rules.
	Label color.RGBA

	// Omitted names roles this palette does not draw at all.
	//
	// It exists because "invisible" and "absent" are different things and
	// only one of them is expressible in a colour. A style can hide a role by
	// painting it the background colour, and the result looks right -- but
	// nothing downstream can tell that apart from a palette that collapsed by
	// accident, which is a real failure this package's own contrast check was
	// written to catch. Saying it here puts the intent in the data: an
	// omitted role is not drawn and is not checked, and a role that merely
	// happens to match the background is still a fault.
	//
	// It also costs less. An omitted role's rules are skipped rather than
	// filled in a colour that changes no pixel, so a style that drops the
	// landuse polygons does not pay for them.
	//
	// The zero value omits nothing, which is every palette written before
	// this field existed.
	//
	// A SET rather than a slice, so that a Palette stays comparable. It was a
	// slice for one version and that was a mistake: Go makes a struct
	// containing a slice uncomparable, which silently withdraws "==" from a
	// value type that had it. The first thing that broke was a consumer's
	// test asserting that the same inks derive the same palette every run --
	// exactly the kind of property a palette should be able to state about
	// itself -- and a render cache keyed on a palette would have been next.
	Omitted RoleSet
}

// RoleSet is a set of roles, held as a bitmask.
//
// The zero value is the empty set. It is a set and not a list because the
// order of "which roles are left out" means nothing, and because a value type
// that holds it should stay comparable -- see Palette.Omitted.
type RoleSet uint16

// Roles builds a RoleSet.
func Roles(rs ...Role) RoleSet {
	var s RoleSet
	for _, r := range rs {
		s |= 1 << r
	}
	return s
}

// Has reports whether r is in the set.
func (s RoleSet) Has(r Role) bool { return s&(1<<r) != 0 }

// Omits reports whether this palette leaves a role undrawn.
func (p Palette) Omits(r Role) bool { return p.Omitted.Has(r) }

// colour returns the colour for a role.
//
// A switch and not a map or an array: map iteration order is the classic way a
// render stops being reproducible, and an array indexed by an untrusted Role
// would be a panic on a value from a caller's own constant. An unknown role
// draws in the ink colour, which is visible and wrong rather than invisible and
// wrong -- a style with a bad role should be obvious in the picture.
func (p Palette) colour(r Role) color.RGBA {
	switch r {
	case RoleBackground:
		return p.Background
	case RoleLand:
		return p.Land
	case RoleWater:
		return p.Water
	case RoleGreen:
		return p.Green
	case RoleBuilt:
		return p.Built
	case RoleRoad:
		return p.Road
	case RoleNoData:
		return p.NoData
	case RoleLabel:
		return p.Label
	case RoleLabelMinor:
		return p.LabelMinor
	}
	return p.Ink
}

// Paint is how one rule draws.
//
// Fill applies to POLYGON features and Width to LINE features, and a rule may
// set both -- a layer holding rivers as both areas and centrelines wants
// exactly that. Point features draw nothing at all: this library renders no
// text, and a place point without its name is a dot nobody can read.
//
// One Paint is one pass of the rasterizer, so a road with a casing is two
// rules, the casing first and wider. That is more verbose than a Paint with a
// casing field and it is the shape the fill rule wants: everything of one
// colour has to go into one path and be filled once, and a casing is a
// different colour.
type Paint struct {
	// Role chooses the colour from the caller's palette.
	Role Role

	// Fill fills polygon features.
	Fill bool

	// Width strokes line features, in TILE PIXELS at a 256-pixel tile, never
	// in output pixels. The renderer multiplies by the scale the view resolves
	// to. A width written as an output-pixel constant is the same number of
	// pixels on a 4K frame and a 1080p one, which is two different maps; see
	// docs/architecture.md, "Styling".
	//
	// A width of 0 draws no line.
	Width float32

	// Dash is an alternating on/off pattern, also in tile pixels, scaled the
	// same way as Width. Empty is a solid line. See raster.Stroke.Dash for what
	// an odd-length pattern means.
	Dash []float32
}

// Rule is one drawing pass: which features, at which zooms, painted how.
//
// Rules are ORDERED and the order is the z-order of the whole map, across every
// tile. It is a flat list rather than a per-layer structure because that is
// what the picture needs: a road has to draw over a neighbouring tile's
// landuse, so the unit of ordering cannot be the tile, and a casing has to draw
// under every road rather than under its own, so the unit cannot be the
// feature either. See docs/architecture.md, trap T1.
type Rule struct {
	// Layer is the vector tile layer's name.
	Layer string

	// Kinds are the values of the feature's "kind" attribute this rule draws.
	// An empty list draws every feature in the layer.
	//
	// It is a slice and not a map so that the style is comparable by eye in a
	// diff and so that nothing here can iterate a map. The lists are a handful
	// of strings long and are searched linearly, which at that length beats
	// hashing anyway.
	Kinds []string

	// MinZoom and MaxZoom bound the zooms this rule draws at, both inclusive
	// and both required. A rule with no deepest zoom says MaxRuleZoom.
	MinZoom, MaxZoom uint8

	Paint Paint
}

// appliesAt reports whether the rule draws at the view's tile zoom.
//
// The test is against the zoom the VIEW resolved to, not the zoom a tile was
// read at. When an ancestor stands in for a missing tile, the map being drawn
// is still the map for the requested zoom -- buildings should not vanish from
// one square of it because the data underneath came from two zooms up.
func (r Rule) appliesAt(z uint8) bool { return z >= r.MinZoom && z <= r.MaxZoom }

// matches reports whether the rule draws a feature with this kind. present says
// whether the feature carried a kind attribute at all.
//
// A feature with no kind is drawn only by a rule that asked for no kinds. The
// alternative -- treating a missing kind as the empty string and letting a rule
// list "" -- would make a feature whose kind attribute is genuinely empty and
// one that has no kind attribute the same feature, which they are not. mvt is
// careful to keep those apart and this is where that care would be thrown away.
func (r Rule) matches(kind string, present bool) bool {
	if len(r.Kinds) == 0 {
		return true
	}
	if !present {
		return false
	}
	return slices.Contains(r.Kinds, kind)
}

// Style is an ordered list of rules, written against one tile schema.
//
// The palette is NOT part of it. A style says which features matter and how
// they are stacked, which is cartography and belongs to this library; the
// colours say what the map should look like beside everything else on the
// screen, which belongs to whatever is drawing the rest of that screen.
type Style struct {
	// Name identifies the style in a diagnostic.
	Name string

	// Schema names the tile schema and version the rules are written against.
	// It is informational and is not checked against anything: a slice records
	// the schema its tiles came from, and comparing the two is the caller's
	// decision to make when there is something to compare against. A style
	// written for one schema version against another draws an empty map, and
	// this field is what makes that diagnosable rather than mysterious.
	Schema string

	Rules []Rule

	// Labels are the names drawn on top of the geometry, in a pass of their
	// own after every rule above has run.
	//
	// A separate list rather than a field on Rule, because the two are not
	// the same kind of thing. A Rule draws where its features are, in the
	// order the list gives; a label competes for space and most of them are
	// dropped. Folding them together would imply that label order is paint
	// order, when what actually decides a label is priority and what is
	// already on the map.
	//
	// Empty draws no labels, which is every style written before this field.
	Labels []LabelRule
}

// Validate refuses a style that cannot draw what it appears to say.
//
// The mistake it exists for is a rule that sets MinZoom and forgets MaxZoom.
// That rule draws at no zoom at all, silently, and the symptom is a missing
// layer in a map that otherwise looks fine -- which reads as a schema problem
// or a data problem and sends the reader looking anywhere but at the style.
func (s Style) Validate() error {
	for i, r := range s.Rules {
		if r.Layer == "" {
			return fmt.Errorf("render: style %q rule %d names no layer, and a rule selects features by layer name", s.Name, i)
		}
		if r.MaxZoom < r.MinZoom {
			return fmt.Errorf("render: style %q rule %d (layer %q) has MaxZoom %d below MinZoom %d, so it draws at no zoom at all; a rule with no deepest zoom says MaxZoom: render.MaxRuleZoom", s.Name, i, r.Layer, r.MaxZoom, r.MinZoom)
		}
		if r.MaxZoom > MaxRuleZoom {
			return fmt.Errorf("render: style %q rule %d (layer %q) has MaxZoom %d, deeper than zoom %d", s.Name, i, r.Layer, r.MaxZoom, MaxRuleZoom)
		}
		if !r.Paint.Fill && !(r.Paint.Width > 0) {
			return fmt.Errorf("render: style %q rule %d (layer %q) neither fills nor strokes, so it draws nothing", s.Name, i, r.Layer)
		}
	}
	return nil
}

// maxStrokeWidth is the widest stroke the style can draw at this zoom, in tile
// pixels.
//
// The renderer needs it to decide how far outside the surface geometry still
// has to be kept: a road whose centreline is just off the top of the image
// still paints half its width into it, and culling it because its coordinates
// are outside would leave a gap along the edge of every view.
func (s Style) maxStrokeWidth(z uint8) float32 {
	var w float32
	for _, r := range s.Rules {
		if r.appliesAt(z) && r.Paint.Width > w {
			w = r.Paint.Width
		}
	}
	return w
}

// ShiftLineLabels returns a copy of the style with its street and water names
// appearing later (a negative shift) or earlier (a positive one).
//
// # Why a zoom shift and not a list of road classes
//
// The obvious way to thin street names is to name fewer kinds of road, and it
// was tried. It behaves very differently from place to place: dropping
// major_road at zoom 14 left a large Sydney suburb with its motorways and
// left a Danish town centre with no street names whatever. A zoom shift
// composes with the per-feature min_zoom already doing the work, so "one
// zoom quieter" means the same thing everywhere rather than something that
// has to be re-tuned for each kind of place.
//
// # Why only the line labels
//
// Places are not what crowds a map. There are few of them, they are the names
// a reader is actually looking for, and shifting them would take away the
// thing that makes a busy map legible. The rules this touches are exactly
// those marked Minor -- the names of things the map DRAWS, as against the
// names of the places it is of.
//
// It sets a bias rather than editing each rule's MinZoom, because a label
// has to clear the rule's threshold AND the feature's own min_zoom, and
// moving only the first gives a dial that does nothing in one direction --
// letting a class of road through that every individual road still vetoes.
// See LabelRule.ZoomBias.
//
// The copy is shallow except for the label rules, which is the only part
// changed. A style is a value and a caller that keeps the original expects it
// to stay as it was.
func (s Style) ShiftLineLabels(zooms int) Style {
	if zooms == 0 {
		return s
	}
	out := s
	out.Labels = make([]LabelRule, len(s.Labels))
	copy(out.Labels, s.Labels)
	for i := range out.Labels {
		r := &out.Labels[i]
		if !r.Minor {
			continue
		}
		r.ZoomBias += zooms
	}
	return out
}

// WithoutLineLabels returns a copy of the style that names places and nothing
// else.
//
// Separate from ShiftLineLabels rather than a large shift, because it is a
// different statement. A shift says "later"; this says "not at all", and a
// caller asking for it should not have to know how many zoom levels count as
// infinity.
func (s Style) WithoutLineLabels() Style {
	out := s
	out.Labels = nil
	for _, r := range s.Labels {
		if !r.Minor {
			out.Labels = append(out.Labels, r)
		}
	}
	return out
}
