// Package mercator converts between geographic degrees, the Web Mercator
// projection every tile scheme in this library is built on, and the tile grid
// laid over it.
//
// It is arithmetic and nothing else: no I/O, no tiles, no archive. What it
// exists for is the step between the two representations this module already
// holds -- an mvt feature's tile-local integers and the degrees a caller
// speaks -- which is needed by anything that turns a decoded tile back into
// coordinates, starting with the GeoJSON dump in cmd/osmbase and ending with
// the renderer's view transform.
//
// # Why it is not in mvt, and not in pmtiles
//
// mvt decodes and deliberately does not project: its coordinates come out in
// the tile's own integer space exactly as the producer wrote them, and a
// decoder that guessed at a projection would be a decoder that had to be
// undone. pmtiles is byte-level and addresses tiles by Hilbert ID. Neither
// knows what a degree is, and neither should: the projection is a third thing
// that both of them are inputs to.
package mercator

import (
	"fmt"
	"math"
)

// MaxLatitude is the northernmost and southernmost latitude Web Mercator can
// represent: the projection sends the poles to infinity, so the square world
// is cut at the latitude whose projected height equals its width.
//
// It is atan(sinh(pi)) in degrees. A coordinate beyond it is a real place with
// no tile, which is why Project CLAMPS to it rather than failing -- see there.
const MaxLatitude = 85.05112877980659

// MaxZoom is the deepest zoom this package addresses.
//
// The grid is 2^z tiles square and an index is a uint32, so zoom 31 is the
// last zoom whose indices fit. It coincides with pmtiles.MaxZoom, which is
// bounded by a different thing -- the uint64 tile ID -- and the two are left
// as separate constants because they are separate limits that happen to agree.
const MaxZoom = 31

// Project converts degrees to normalised Web Mercator coordinates, where the
// whole world is the unit square: x runs east from -180 degrees, y runs SOUTH
// from the northern cut.
//
// # The direction of y, and the flip that is not there
//
// y increasing southward is not a convention this package invented to be
// convenient. It is Web Mercator's own: the projection is defined downward
// from the north edge, tile row 0 is the top row in every slippy-map scheme,
// and an mvt tile's own integer y also runs down from the tile's top left. All
// three agree, so converting a tile-local y to a normalised world y is a scale
// and an offset with NO sign change anywhere.
//
// This is worth stating because the absence of a flip looks like an omission.
// Anyone who adds one will get a map mirrored about the equator, which at a
// glance -- coastlines still coastlines, roads still roads -- looks like a
// map.
//
// The one place the direction does reverse is the last step of Unproject, from
// normalised y to LATITUDE, because latitude is measured northward. That is
// the projection formula itself and not a correction applied to it.
//
// Latitude beyond the Mercator cut is clamped to it. Such a latitude is a
// place that exists and has no tile, so the honest answer is the edge of the
// grid; failing would push a check into every caller for somewhere nothing in
// this library holds data anyway. Longitude is NOT clamped or wrapped: a
// longitude outside [-180, 180] produces an x outside [0, 1], which is what
// its caller asked for. TileAt is where a coordinate is validated.
func Project(lon, lat float64) (x, y float64) {
	if lat > MaxLatitude {
		lat = MaxLatitude
	}
	if lat < -MaxLatitude {
		lat = -MaxLatitude
	}
	x = (lon + 180) / 360
	sin := math.Sin(lat * math.Pi / 180)
	y = 0.5 - math.Log((1+sin)/(1-sin))/(4*math.Pi)
	return x, y
}

// Unproject is Project's inverse: normalised Web Mercator coordinates back to
// degrees.
//
// It does not clamp. A y slightly outside [0, 1] is ordinary -- a tile carries
// a buffer of geometry from beyond its own edge, and near the top or bottom
// row that buffer projects past the cut -- and the latitude it returns there
// is the correct one for that point. Round-tripping a latitude past the cut
// through Project and back gives the cut, because that information is gone at
// the clamp and not here.
func Unproject(x, y float64) (lon, lat float64) {
	lon = x*360 - 180
	lat = math.Atan(math.Sinh(math.Pi*(1-2*y))) * 180 / math.Pi
	return lon, lat
}

// TileAt returns the x and y of the tile at zoom z that contains lon/lat.
//
// It validates the coordinate rather than projecting whatever it is handed,
// because this is where a user's typo arrives. A swapped pair -- latitude
// 151, longitude -33 -- is the single most common way to ask for the wrong
// place, and an unvalidated one lands silently in the Arctic instead of
// Sydney. A longitude of exactly 180 and a latitude of exactly ±90 are legal
// coordinates and are accepted; both land on the last tile of their row or
// column rather than one past it.
func TileAt(z uint8, lon, lat float64) (x, y uint32, err error) {
	if z > MaxZoom {
		return 0, 0, fmt.Errorf("mercator: zoom %d is deeper than zoom %d, the deepest this package addresses", z, MaxZoom)
	}
	if math.IsNaN(lon) || math.IsNaN(lat) {
		return 0, 0, fmt.Errorf("mercator: latitude %v, longitude %v is not a coordinate", lat, lon)
	}
	if lat < -90 || lat > 90 {
		return 0, 0, fmt.Errorf("mercator: latitude %g is outside -90 to 90; if this is a longitude, the arguments are the wrong way round", lat)
	}
	if lon < -180 || lon > 180 {
		return 0, 0, fmt.Errorf("mercator: longitude %g is outside -180 to 180", lon)
	}
	n := float64(uint64(1) << z)
	wx, wy := Project(lon, lat)
	return gridIndex(wx, n), gridIndex(wy, n), nil
}

// gridIndex turns a normalised world coordinate into a tile index at a grid n
// tiles across, clamping to the grid.
//
// The clamp is for the two ends. Longitude 180 projects to exactly 1.0 and
// floor(1.0*n) is n, one past the last column; a latitude at the Mercator cut
// does the same in y, and a latitude past it has already been clamped to the
// cut by Project. Without this an entirely legal coordinate names a tile that
// does not exist.
func gridIndex(v, n float64) uint32 {
	i := math.Floor(v * n)
	if i < 0 {
		return 0
	}
	if i > n-1 {
		return uint32(n - 1)
	}
	return uint32(i)
}

// TileBounds returns the geographic rectangle tile z/x/y covers, in degrees.
func TileBounds(z uint8, x, y uint32) (west, south, east, north float64, err error) {
	if err := checkTile(z, x, y); err != nil {
		return 0, 0, 0, 0, err
	}
	n := float64(uint64(1) << z)
	// y+1 is the SOUTH edge, because y runs south. See Project.
	west, north = Unproject(float64(x)/n, float64(y)/n)
	east, south = Unproject(float64(x+1)/n, float64(y+1)/n)
	return west, south, east, north, nil
}

// TileTransform converts one tile's local integer coordinates into degrees.
//
// It is a value rather than a function with six arguments because a caller
// converting a whole tile's geometry has one tile and one extent for thousands
// of points, and because the extent is per LAYER: a tile whose building layer
// is at extent 4096 and whose landcover layer is at 512 needs two of these,
// and a shape that made that easy to forget would silently place one layer at
// an eighth of its true size.
type TileTransform struct {
	// scale is how many world units one tile-local unit is worth, and originX
	// and originY are the tile's north-west corner in world units.
	scale            float64
	originX, originY float64
}

// NewTileTransform returns the transform for tile z/x/y whose layer has the
// given extent.
func NewTileTransform(z uint8, x, y uint32, extent uint32) (TileTransform, error) {
	if err := checkTile(z, x, y); err != nil {
		return TileTransform{}, err
	}
	if extent == 0 {
		return TileTransform{}, fmt.Errorf("mercator: a layer extent of 0 gives every point in the tile the same coordinate")
	}
	n := float64(uint64(1) << z)
	return TileTransform{
		scale:   1 / (n * float64(extent)),
		originX: float64(x) / n,
		originY: float64(y) / n,
	}, nil
}

// LonLat converts a tile-local integer coordinate to degrees.
//
// Coordinates outside [0, extent) are expected rather than exceptional: a tile
// carries a buffer of geometry from beyond its own edge so that a line
// crossing the edge can be drawn without a notch. They convert to the
// coordinates of the neighbouring tile's ground, which is where they are.
//
// Both axes scale the same way with no sign change: tile y and world y both
// run south. The turn from a southward measure to a northward one happens in
// Unproject, where latitude is computed. See Project.
func (t TileTransform) LonLat(x, y int32) (lon, lat float64) {
	return Unproject(t.originX+float64(x)*t.scale, t.originY+float64(y)*t.scale)
}

func checkTile(z uint8, x, y uint32) error {
	if z > MaxZoom {
		return fmt.Errorf("mercator: zoom %d is deeper than zoom %d, the deepest this package addresses", z, MaxZoom)
	}
	n := uint64(1) << z
	if uint64(x) >= n || uint64(y) >= n {
		return fmt.Errorf("mercator: tile %d/%d/%d is outside zoom %d, which is %d tiles square", z, x, y, z, n)
	}
	return nil
}
