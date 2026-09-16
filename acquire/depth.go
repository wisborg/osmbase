package acquire

import (
	"fmt"
	"math"

	"github.com/wisborg/osmbase/mercator"
	"github.com/wisborg/osmbase/slice"
)

// A slice is a zoom RANGE over an area, and the range comes from the area's
// own extent rather than from a constant. A run wants zoom 12 to 15 over eight
// kilometres; a flight wants zoom 0 to 5 over the whole world. Neither is the
// special case, which is why this is a function and not a default. See
// docs/architecture.md, "Depth follows the extent, and a global track is the
// cheap case".

// Depth is how deep, and over what shape of ground, a fetch goes.
type Depth struct {
	// Max is the deepest zoom to take.
	Max uint8

	// World says the cell grid is the wrong structure for this area and the
	// whole planet at Max is both cheaper and more useful.
	//
	// It is not a fallback for "too big". Zoom 0 to 5 is 1,365 tiles and
	// 19.5 MB -- every tile on earth at flight detail, for what one city cell
	// costs at street detail -- so a track crossing an ocean wants no bounding
	// box, no cell arithmetic and no corridor. Applying the cell model to it
	// would be absurd in both directions, fetching a sub-pyramid to zoom 15
	// for ground nobody will ever see at that scale.
	World bool

	// Why is a phrase for the report, so a user can see that the depth was
	// reasoned about rather than guessed. Empty when the caller named the
	// depth itself.
	Why string
}

// WorldMaxZoom is the depth a global slice is taken to.
//
// Measured against the real planet build: zoom 0 to 4 is 341 tiles and 8.0 MB,
// zoom 0 to 5 is 1,365 tiles and 19.5 MB. Below zoom 3 the schema carries no
// roads at all -- only coastlines, administrative boundaries, water, landcover
// and place names -- which is exactly what a route across an ocean wants.
const WorldMaxZoom = 5

// kmPerDegree is a degree of latitude in kilometres, on a sphere of the
// authalic radius. It is used only to choose a zoom band, where a percent is
// far below what would move the answer, so the ellipsoid is not worth carrying
// here.
const kmPerDegree = 111.32

// The bands DepthFor chooses between.
//
// The spans are set so that every band costs the same order of magnitude,
// which is the only defensible way to draw them: the cell count grows as the
// SQUARE of the span, and dropping one zoom divides a cell's deepest level by
// four, which is most of its bytes. The measured per-cell figures are 8.9 MB
// for the City of London, 3.7 MB for central Sydney, 2.0 MB for the Blue
// Mountains, 21 KB for outback New South Wales and 6 KB mid-Pacific, all at
// zoom 12 to 15.
//
//	25 km   ~ 3x3 = 9 cells at 12-15     -> 33 MB at Sydney density
//	80 km   ~ 9x9 = 81 cells at 12-14    -> 75 MB, a cell without its deepest
//	                                        level being roughly a quarter of
//	                                        the 12-15 figure
//	250 km  ~ 26x26 = 676 cells at 12-13 -> 40 MB
//	beyond  the world at 0-5             -> 19.5 MB, flat, whatever the span
//
// So the cost of asking for more ground is that the ground arrives less
// detailed, rather than that the download grows without limit. The numbers
// above are the reasoning; the number a user is shown is the exact one
// PlanFor computes from the archive's own directories.
//
// The 250 km edge has a second job. Past it the cell count runs into
// MaxPlanCells, and a limit that is reached by a rule rather than by a user's
// typo is a limit nobody meets.
var depthBands = []struct {
	spanKM float64
	max    uint8
}{
	{25, 15},
	{80, 14},
	{250, 13},
}

// DepthFor chooses a zoom range for an area, capped at what the archive holds.
//
// sourceMax is the archive's own deepest zoom, from its header, and it is a
// cap rather than a suggestion: public builds stop at zoom 15 and there is
// nothing deeper to fetch. See docs/architecture.md, trap T4.
func DepthFor(b slice.Bounds, sourceMax uint8) (Depth, error) {
	if err := validBounds(b); err != nil {
		return Depth{}, err
	}
	widthKM, heightKM := ExtentKM(b)
	span := math.Max(widthKM, heightKM)

	for _, band := range depthBands {
		if span <= band.spanKM {
			d := Depth{Max: band.max, Why: fmt.Sprintf("chosen from a %s area", formatKM(span))}
			if d.Max > sourceMax {
				d.Max = sourceMax
				d.Why += fmt.Sprintf(", capped at the zoom %d this archive holds", sourceMax)
			}
			return d, nil
		}
	}

	d := Depth{Max: WorldMaxZoom, World: true, Why: fmt.Sprintf("a %s area is wider than the cell grid is for, so the whole world at flight detail is cheaper", formatKM(span))}
	if d.Max > sourceMax {
		d.Max = sourceMax
	}
	return d, nil
}

// ExtentKM is how wide and how tall a rectangle is on the ground, in
// kilometres.
//
// The width is measured at the latitude nearest the equator, which is the
// WIDEST edge of the rectangle: a box reaching from 50 to 60 degrees north is
// wider along its southern edge, and taking the middle or the far edge would
// understate the ground the box covers and choose a depth a band too deep.
func ExtentKM(b slice.Bounds) (width, height float64) {
	nearest := math.Min(math.Abs(b.South), math.Abs(b.North))
	if b.South <= 0 && b.North >= 0 {
		// The box straddles the equator, so its widest latitude is zero.
		nearest = 0
	}
	width = (b.East - b.West) * kmPerDegree * math.Cos(nearest*math.Pi/180)
	height = (b.North - b.South) * kmPerDegree
	return width, height
}

// BoundsAround is the square reaching radiusKM north, south, east and west of
// a coordinate.
//
// A square rather than a circle, and said so in the name, because the cell
// grid rounds outward to whole cells anyway: a circle inscribed in this square
// would touch the same cells at every radius that matters and would only make
// the flag's meaning harder to state.
func BoundsAround(lat, lon, radiusKM float64) (slice.Bounds, error) {
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return slice.Bounds{}, fmt.Errorf("acquire: latitude %g, longitude %g is not a coordinate", lat, lon)
	}
	if !(radiusKM > 0) || math.IsInf(radiusKM, 0) {
		return slice.Bounds{}, fmt.Errorf("acquire: a radius of %g km covers no ground", radiusKM)
	}
	dLat := radiusKM / kmPerDegree
	// The cosine is taken at the latitude the box reaches FURTHEST from the
	// equator, which gives the largest longitude span and so a box that
	// contains the intended circle everywhere rather than only at its centre.
	// Clamped short of the pole because the cosine there is zero and the span
	// would be the whole world.
	far := math.Min(math.Abs(lat)+dLat, 89.0)
	dLon := radiusKM / (kmPerDegree * math.Cos(far*math.Pi/180))

	b := slice.Bounds{
		West:  lon - dLon,
		East:  lon + dLon,
		South: math.Max(lat-dLat, -90),
		North: math.Min(lat+dLat, 90),
	}
	if b.West < -180 || b.East > 180 {
		return slice.Bounds{}, fmt.Errorf("acquire: a %g km radius around longitude %g reaches %s the antimeridian, and an area crossing it has to be fetched as two",
			radiusKM, lon, map[bool]string{true: "west past", false: "east past"}[b.West < -180])
	}
	return b, nil
}

// validBounds refuses a rectangle that names no area, before anything
// downstream divides by its width.
//
// slice.Bounds does its own validation when cells are worked out, but that
// happens after the depth has been chosen, and a depth chosen from a negative
// span would be reported to the user before the real error appeared.
func validBounds(b slice.Bounds) error {
	for _, c := range []struct {
		name  string
		v     float64
		limit float64
	}{
		{"west", b.West, 180}, {"east", b.East, 180},
		{"south", b.South, 90}, {"north", b.North, 90},
	} {
		if math.IsNaN(c.v) || math.IsInf(c.v, 0) {
			return fmt.Errorf("acquire: the %s edge is %v, which is not a coordinate", c.name, c.v)
		}
		if c.v < -c.limit || c.v > c.limit {
			return fmt.Errorf("acquire: the %s edge is %g, outside -%g to %g", c.name, c.v, c.limit, c.limit)
		}
	}
	if b.East <= b.West {
		return fmt.Errorf("acquire: the east edge (%g) is not east of the west edge (%g); an area crossing the antimeridian has to be fetched as two", b.East, b.West)
	}
	if b.North <= b.South {
		return fmt.Errorf("acquire: the north edge (%g) is not north of the south edge (%g)", b.North, b.South)
	}
	return nil
}

// formatKM renders a distance for the one-line explanation of a depth.
func formatKM(km float64) string {
	if km < 10 {
		return fmt.Sprintf("%.1f km", km)
	}
	return fmt.Sprintf("%.0f km", km)
}

// WorldBounds is the whole of the ground a global slice covers, which is the
// whole Web Mercator square and not the whole sphere.
//
// The projection cuts off at 85.05 degrees, so a rectangle reaching 90 would
// name ground that has no tile at any zoom. Using the cut is what makes a
// global request's coverage report read as complete rather than as missing two
// polar strips that were never there.
func WorldBounds() slice.Bounds {
	return slice.Bounds{West: -180, South: -mercator.MaxLatitude, East: 180, North: mercator.MaxLatitude}
}
