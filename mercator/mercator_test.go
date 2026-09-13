package mercator_test

import (
	"math"
	"testing"

	"github.com/wisborg/osmbase/mercator"
)

// near reports whether two degree or unit-square values agree to within a
// tolerance. The tolerances below are stated per test: an exact-by-arithmetic
// value is compared exactly, and one that goes through a transcendental
// function is not.
func near(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

// TestProject_MapsTheWorldSquareOntoTheUnitSquare checks the four values the
// projection is defined by, each derived from the definition rather than from
// this package.
//
// x is a linear remap of longitude, so (lon+180)/360 gives 0 at -180, 0.5 at
// 0 and 0.75 at +90 exactly. y is 0.5 at the equator because ln(1) is 0, and
// the Mercator cut is the latitude atan(sinh(pi)) = 85.0511287798066 degrees,
// chosen precisely so that the projected world is square -- so y there is 0 at
// the north cut and 1 at the south one.
func TestProject_MapsTheWorldSquareOntoTheUnitSquare(t *testing.T) {
	cases := []struct {
		name     string
		lon, lat float64
		wantX    float64
		wantY    float64
	}{
		{"null island", 0, 0, 0.5, 0.5},
		{"north west corner", -180, mercator.MaxLatitude, 0, 0},
		{"south east corner", 180, -mercator.MaxLatitude, 1, 1},
		{"ninety east on the equator", 90, 0, 0.75, 0.5},
		{"beyond the cut is clamped to it", 0, 89.9, 0.5, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x, y := mercator.Project(c.lon, c.lat)
			if !near(x, c.wantX, 1e-12) || !near(y, c.wantY, 1e-9) {
				t.Errorf("Project(%g, %g) = (%v, %v), want (%v, %v)", c.lon, c.lat, x, y, c.wantX, c.wantY)
			}
		})
	}
}

// TestProject_YRunsSouth is the property the whole package's sign convention
// rests on: a point north of another has a SMALLER y.
//
// It is tested on its own, rather than left implied by the corner values,
// because a mirrored projection agrees with this package everywhere on the
// equator and at the two cuts. Getting it backwards produces a map reflected
// about the equator, which still looks like a map.
func TestProject_YRunsSouth(t *testing.T) {
	_, north := mercator.Project(0, 40)
	_, equator := mercator.Project(0, 0)
	_, south := mercator.Project(0, -40)
	if !(north < equator && equator < south) {
		t.Errorf("y at 40N, 0 and 40S is %v, %v, %v; it must increase southward", north, equator, south)
	}
	// The projection is symmetric about the equator, so the two y values for
	// +40 and -40 must sum to exactly one world.
	if !near(north+south, 1, 1e-12) {
		t.Errorf("y(40N) + y(40S) = %v, want 1", north+south)
	}
}

// TestUnproject_InvertsProject round-trips a spread of coordinates.
//
// The tolerance is 1e-9 degrees, which is about 0.1 mm on the ground and four
// orders of magnitude finer than the hundred-nanodegree precision a PMTiles
// header stores bounds at.
func TestUnproject_InvertsProject(t *testing.T) {
	lons := []float64{-180, -151.2, -0.1, 0, 12.5, 151.2153, 180}
	lats := []float64{-85, -33.8568, -0.0001, 0, 51.5, 84.9}
	for _, lon := range lons {
		for _, lat := range lats {
			x, y := mercator.Project(lon, lat)
			gotLon, gotLat := mercator.Unproject(x, y)
			if !near(gotLon, lon, 1e-9) || !near(gotLat, lat, 1e-9) {
				t.Errorf("round trip of (%g, %g) gave (%v, %v)", lon, lat, gotLon, gotLat)
			}
		}
	}
}

// TestTileAt_SydneyOperaHouse pins the conversion against a coordinate whose
// tile can be worked out on paper.
//
// At zoom 14 the grid is 2^14 = 16384 tiles square.
//
//	x: (151.2153 + 180) / 360 = 0.9200425, times 16384 = 15073.976 -> 15073
//	y: sin(-33.8568 deg) = -0.5570447, so
//	   0.5 - ln(0.4429553/1.5570447) / (4*pi) = 0.6000521,
//	   times 16384 = 9831.254 -> 9831
//
// Zoom 12 must then be the zoom-14 pair halved twice, because a tile's parent
// is exactly its index shifted right, and 15073>>2 = 3768, 9831>>2 = 2457.
func TestTileAt_SydneyOperaHouse(t *testing.T) {
	const lon, lat = 151.2153, -33.8568
	cases := []struct {
		z            uint8
		wantX, wantY uint32
	}{
		{14, 15073, 9831},
		{12, 3768, 2457},
		{0, 0, 0},
	}
	for _, c := range cases {
		x, y, err := mercator.TileAt(c.z, lon, lat)
		if err != nil {
			t.Fatalf("TileAt(%d, %g, %g): %v", c.z, lon, lat, err)
		}
		if x != c.wantX || y != c.wantY {
			t.Errorf("TileAt(%d, %g, %g) = %d/%d, want %d/%d", c.z, lon, lat, x, y, c.wantX, c.wantY)
		}
	}
}

// TestTileAt_TheEdgesOfTheWorldLandOnTheLastTile checks the clamp.
//
// Longitude 180 projects to exactly x = 1 and latitude -90 clamps to the
// Mercator cut, which projects to exactly y = 1. At zoom 2 the grid is four
// tiles square, so both belong in index 3; without the clamp the floor gives 4
// and names a tile that does not exist.
func TestTileAt_TheEdgesOfTheWorldLandOnTheLastTile(t *testing.T) {
	x, y, err := mercator.TileAt(2, 180, -90)
	if err != nil {
		t.Fatalf("TileAt(2, 180, -90): %v", err)
	}
	if x != 3 || y != 3 {
		t.Errorf("TileAt(2, 180, -90) = %d/%d, want 3/3", x, y)
	}
	x, y, err = mercator.TileAt(2, -180, 90)
	if err != nil {
		t.Fatalf("TileAt(2, -180, 90): %v", err)
	}
	if x != 0 || y != 0 {
		t.Errorf("TileAt(2, -180, 90) = %d/%d, want 0/0", x, y)
	}
}

// TestTileAt_RefusesWhatIsNotACoordinate covers the mistakes a person makes at
// a command line, the swapped pair above all: latitude 151 is not a latitude,
// and accepting it would put Sydney in the Arctic without a word.
func TestTileAt_RefusesWhatIsNotACoordinate(t *testing.T) {
	cases := []struct {
		name     string
		z        uint8
		lon, lat float64
	}{
		{"latitude and longitude swapped", 14, -33.8568, 151.2153},
		{"latitude past the pole", 14, 0, 90.5},
		{"longitude past the antimeridian", 14, 180.5, 0},
		{"not a number", 14, math.NaN(), 0},
		{"zoom past the deepest", mercator.MaxZoom + 1, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := mercator.TileAt(c.z, c.lon, c.lat); err == nil {
				t.Errorf("TileAt(%d, %g, %g) succeeded; want an error", c.z, c.lon, c.lat)
			}
		})
	}
}

// TestTileBounds_QuadrantsOfZoomOne checks the four zoom-1 tiles, whose
// corners are the world's corners, the equator and the prime meridian.
func TestTileBounds_QuadrantsOfZoomOne(t *testing.T) {
	cases := []struct {
		x, y                     uint32
		west, south, east, north float64
	}{
		{0, 0, -180, 0, 0, mercator.MaxLatitude},
		{1, 0, 0, 0, 180, mercator.MaxLatitude},
		{0, 1, -180, -mercator.MaxLatitude, 0, 0},
		{1, 1, 0, -mercator.MaxLatitude, 180, 0},
	}
	for _, c := range cases {
		w, s, e, n, err := mercator.TileBounds(1, c.x, c.y)
		if err != nil {
			t.Fatalf("TileBounds(1, %d, %d): %v", c.x, c.y, err)
		}
		if !near(w, c.west, 1e-9) || !near(s, c.south, 1e-9) || !near(e, c.east, 1e-9) || !near(n, c.north, 1e-9) {
			t.Errorf("TileBounds(1, %d, %d) = W%v S%v E%v N%v, want W%v S%v E%v N%v",
				c.x, c.y, w, s, e, n, c.west, c.south, c.east, c.north)
		}
	}
}

// TestTileTransform_CornersOfATileAreItsOwnBounds converts the two corners of
// tile 1/1/0, the north-east quadrant of the world, from tile-local integers.
//
// Local (0, 0) is the tile's north-west corner, which for this tile is the
// prime meridian at the Mercator cut; local (extent, extent) is its south-east
// corner, which is the antimeridian on the equator. A transform that flipped y
// would return the equator for the first and the cut for the second, and the
// two are far enough apart that no tolerance hides it.
func TestTileTransform_CornersOfATileAreItsOwnBounds(t *testing.T) {
	const extent = 4096
	tr, err := mercator.NewTileTransform(1, 1, 0, extent)
	if err != nil {
		t.Fatalf("NewTileTransform: %v", err)
	}
	lon, lat := tr.LonLat(0, 0)
	if !near(lon, 0, 1e-9) || !near(lat, mercator.MaxLatitude, 1e-9) {
		t.Errorf("LonLat(0, 0) = (%v, %v), want (0, %v)", lon, lat, mercator.MaxLatitude)
	}
	lon, lat = tr.LonLat(extent, extent)
	if !near(lon, 180, 1e-9) || !near(lat, 0, 1e-9) {
		t.Errorf("LonLat(extent, extent) = (%v, %v), want (180, 0)", lon, lat)
	}
	lon, lat = tr.LonLat(extent/2, extent/2)
	if !near(lon, 90, 1e-9) {
		t.Errorf("LonLat at the tile centre = (%v, %v), want longitude 90", lon, lat)
	}
	// The centre of the tile in y is halfway down the PROJECTION, not halfway
	// in latitude, so the value is atan(sinh(pi/2)) = 66.5132 degrees. Pinning
	// it is what distinguishes the projection from a linear scale of latitude,
	// which agrees with it at both corners.
	if want := math.Atan(math.Sinh(math.Pi/2)) * 180 / math.Pi; !near(lat, want, 1e-9) {
		t.Errorf("LonLat at the tile centre gave latitude %v, want %v", lat, want)
	}
}

// TestTileTransform_TileLocalYRunsSouth is the tile-space half of the sign
// convention: a larger tile-local y is further SOUTH, so the latitude falls.
//
// Tile space, the Web Mercator unit square and a screen all measure y
// downward, so this conversion applies no flip at all. That absence is the
// thing a later reader is most likely to "correct", and this test is what
// stops them.
func TestTileTransform_TileLocalYRunsSouth(t *testing.T) {
	tr, err := mercator.NewTileTransform(14, 15073, 9831, 4096)
	if err != nil {
		t.Fatalf("NewTileTransform: %v", err)
	}
	_, top := tr.LonLat(2048, 0)
	_, middle := tr.LonLat(2048, 2048)
	_, bottom := tr.LonLat(2048, 4096)
	if !(top > middle && middle > bottom) {
		t.Errorf("latitudes down the tile are %v, %v, %v; they must decrease", top, middle, bottom)
	}
	west, _ := tr.LonLat(0, 2048)
	east, _ := tr.LonLat(4096, 2048)
	if !(west < east) {
		t.Errorf("longitudes across the tile are %v then %v; they must increase eastward", west, east)
	}
}

// TestTileTransform_AgreesWithTileAt closes the loop: a point taken from
// inside a tile must convert to a coordinate that TileAt puts back in the same
// tile.
//
// This is the property the GeoJSON dump depends on, and it is the one that
// catches an origin off by one tile -- an error that leaves every other test
// here passing because each of them checks only one direction.
func TestTileTransform_AgreesWithTileAt(t *testing.T) {
	const z = 14
	for _, tile := range [][2]uint32{{15073, 9831}, {0, 0}, {16383, 16383}, {8192, 4096}} {
		tr, err := mercator.NewTileTransform(z, tile[0], tile[1], 4096)
		if err != nil {
			t.Fatalf("NewTileTransform: %v", err)
		}
		for _, local := range [][2]int32{{1, 1}, {2048, 2048}, {4095, 4095}} {
			lon, lat := tr.LonLat(local[0], local[1])
			x, y, err := mercator.TileAt(z, lon, lat)
			if err != nil {
				t.Fatalf("TileAt(%d, %v, %v): %v", z, lon, lat, err)
			}
			if x != tile[0] || y != tile[1] {
				t.Errorf("local (%d, %d) of tile %d/%d/%d is (%v, %v), which TileAt puts in %d/%d/%d",
					local[0], local[1], z, tile[0], tile[1], lon, lat, z, x, y)
			}
		}
	}
}

// TestTileTransform_ExtentScalesTheSameTile checks that the extent is the
// layer's own denominator: the midpoint of a layer at extent 512 and the
// midpoint of one at extent 4096 are the same place on the ground.
//
// The extent is per layer rather than per tile, and a converter that used one
// layer's extent for another would place that layer's geometry at a fraction
// of its true size, in the right tile, which looks like a detail level rather
// than a bug.
func TestTileTransform_ExtentScalesTheSameTile(t *testing.T) {
	coarse, err := mercator.NewTileTransform(14, 15073, 9831, 512)
	if err != nil {
		t.Fatalf("NewTileTransform: %v", err)
	}
	fine, err := mercator.NewTileTransform(14, 15073, 9831, 4096)
	if err != nil {
		t.Fatalf("NewTileTransform: %v", err)
	}
	cLon, cLat := coarse.LonLat(256, 256)
	fLon, fLat := fine.LonLat(2048, 2048)
	if !near(cLon, fLon, 1e-12) || !near(cLat, fLat, 1e-12) {
		t.Errorf("the middle of the tile is (%v, %v) at extent 512 and (%v, %v) at extent 4096", cLon, cLat, fLon, fLat)
	}
}

// TestNewTileTransform_RefusesATileThatDoesNotExist covers the two ways the
// arguments can fail to describe a tile. An extent of 0 is the interesting
// one: it is a legal-looking uint32 that would collapse every point in the
// layer onto the tile's corner.
func TestNewTileTransform_RefusesATileThatDoesNotExist(t *testing.T) {
	cases := []struct {
		name   string
		z      uint8
		x, y   uint32
		extent uint32
	}{
		{"x past the edge of the zoom", 1, 2, 0, 4096},
		{"y past the edge of the zoom", 1, 0, 2, 4096},
		{"zoom past the deepest", mercator.MaxZoom + 1, 0, 0, 4096},
		{"zero extent", 14, 15073, 9831, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := mercator.NewTileTransform(c.z, c.x, c.y, c.extent); err == nil {
				t.Errorf("NewTileTransform(%d, %d, %d, %d) succeeded; want an error", c.z, c.x, c.y, c.extent)
			}
		})
	}
}
