package render

import (
	"context"
	"fmt"
	"image"
	"math"

	"github.com/wisborg/osmbase/mvt"
)

// TileSource is where tiles come from, and is the whole of what this package
// asks of a store.
//
// One method, because there is only one question: have you got the tile at
// z/x/y, and if so what is in it. Everything else a store knows -- how it is
// laid out on disk, what it cost to fetch, when it was last used, how to evict
// a cell -- is a store's business and none of a renderer's.
//
// # Why the interface is declared here
//
// It is defined by the consumer rather than exported by an implementation, so
// the dependency points from the store to the renderer and not the other way.
// That is what lets this package import neither pmtiles nor acquire: a renderer
// that knew about an archive format would be a renderer that could not be given
// a different one, and a renderer that could reach acquire would be a renderer
// nobody could prove stays off the network. The on-disk store does not exist
// yet; when it does it will satisfy this without anything here changing.
//
// *pmtiles.Reader already satisfies it as it stands. That is a coincidence
// worth noticing rather than a design: the shape came from the question, and
// the fact that the format reader had independently arrived at the same one is
// the evidence that it is the right question.
//
// # The contract
//
// data is the tile's Mapbox Vector Tile bytes, DECOMPRESSED. A store that keeps
// tiles compressed on disk -- which is the right thing to do, and what the
// design says -- decompresses them here.
//
// ok reports whether the source holds that tile, and it is not the same
// question as err. An archive legitimately has no tile at most coordinates:
// outside its bounds, outside its zoom range, or over ocean a writer chose to
// omit. That is an answer. A source that returned an error for a tile it simply
// does not have would turn the ordinary edge of a map slice into a failed
// render.
//
// There is no context. Cancellation is checked by the renderer between tiles,
// because a source is meant to be local: reaching the network from here is the
// exact thing this library exists to remove, and a method signature that
// invited a deadline would be an invitation to put a request behind it.
type TileSource interface {
	Tile(z uint8, x, y uint32) (data []byte, ok bool, err error)
}

// tileRef names one tile.
type tileRef struct {
	z    uint8
	x, y uint32
}

func (t tileRef) String() string { return fmt.Sprintf("%d/%d/%d", t.z, t.x, t.y) }

// parent is the tile one zoom shallower that covers this one.
func (t tileRef) parent() tileRef {
	return tileRef{z: t.z - 1, x: t.x >> 1, y: t.y >> 1}
}

// drawTile is one decoded tile together with the part of the surface it is
// responsible for.
//
// The clip box is NOT always the tile's own ground. When a tile is missing and
// an ancestor stands in for it, the ancestor covers four or sixteen times the
// area, and drawing all of it would paint its coarser geometry over the
// neighbouring tiles that were not missing -- two generalisations of the same
// road, a fraction of a pixel apart, along every shared edge. So an ancestor is
// clipped to the square of the child it is standing in for, and the neighbours
// keep their own detail.
type drawTile struct {
	ref  tileRef
	tile *mvt.Tile
	clip box
	// overzoom records that this tile was read from a shallower zoom than the
	// view asked for. It is reported rather than hidden: overzoomed vector data
	// is sharp, so it looks complete, and the only way a caller can tell that
	// the detail it is not seeing exists somewhere is to be told. See
	// docs/architecture.md, trap T4.
	overzoom bool
}

// coverage is the honest account of what the tiles covered, measured in
// surface area and never in ink.
//
// Every number here comes from which tiles the source HAS. None of it is
// measured from the rendered image, and it must stay that way: an ocean tile
// draws almost nothing and is fully covered, a tile of empty desert draws
// nothing at all, and a coverage figure derived from pixels would report both
// as missing data. See docs/architecture.md, trap T9.
type coverage struct {
	// total is the surface area the requested tiles account for, which is the
	// surface itself except for rounding.
	total float64
	// covered is how much of it a tile was found for, at any zoom, and over how
	// much of it that tile came from a shallower zoom.
	covered, over float64

	requested, resolved int

	// gaps are the squares no tile was found for, at any zoom, in surface
	// pixels, rounded outward so that a gap is never under-painted.
	gaps []image.Rectangle
}

func (c coverage) fraction(v float64) float64 {
	if c.total <= 0 {
		return 0
	}
	return v / c.total
}

// gather finds a tile for every square of the view, walking up the pyramid
// where the requested zoom is not held, and decodes each one once.
//
// The scan is row by row and column by column, and the resulting slice is what
// everything downstream iterates. That order is load bearing rather than
// incidental: coverage accumulates as a float32 sum inside the rasterizer, so
// appending the same features in a different order can move a pixel by one
// step of alpha. Ranging over the decode cache instead -- a map -- would put
// Go's randomised iteration order into the image, and two renders of one
// activity would differ.
func (r *Renderer) gather(ctx context.Context, p projection) ([]drawTile, coverage, error) {
	x0, y0, x1, y1 := p.tileRange(p.tileZoom)

	// How far outside the surface geometry is still worth keeping: a road whose
	// centreline is just off the edge still paints half its width onto the
	// image, so culling on the surface alone would leave a ragged strip along
	// every border. The whole width rather than the half is slack, and the
	// extra pixel covers the antialiased rim.
	pad := float64(r.style.maxStrokeWidth(p.tileZoom))*p.tileScale + 1
	keep := p.surface().inflate(pad)

	var (
		cov     coverage
		tiles   []drawTile
		decoded = map[tileRef]*mvt.Tile{}
	)
	for ty := y0; ty <= y1; ty++ {
		for tx := x0; tx <= x1; tx++ {
			if err := ctx.Err(); err != nil {
				return nil, coverage{}, fmt.Errorf("render: gathering the tiles for a %d by %d view at zoom %d: %w", p.width, p.height, p.tileZoom, err)
			}
			want := tileRef{z: p.tileZoom, x: tx, y: ty}
			square := p.tileBox(want.z, want.x, want.y)
			onSurface := square.intersect(p.surface())

			cov.requested++
			cov.total += onSurface.area()

			ref, data, ok, err := r.walkUp(want)
			if err != nil {
				return nil, coverage{}, err
			}
			if !ok {
				cov.gaps = append(cov.gaps, outward(onSurface))
				continue
			}
			cov.resolved++
			cov.covered += onSurface.area()
			if ref != want {
				cov.over += onSurface.area()
			}

			tile, seen := decoded[ref]
			if !seen {
				t, err := mvt.Decode(data)
				if err != nil {
					// A tile that will not decode is not absence. Absence is a
					// tile the store has not got, and it has a picture: the
					// hatch. This is a tile the store HAS and cannot read,
					// which means either the bytes are damaged or they are not
					// vector tiles at all, and hatching over it would hide the
					// one fact worth reporting.
					return nil, coverage{}, fmt.Errorf("render: decoding tile %s: %w", ref, err)
				}
				tile = &t
				decoded[ref] = tile
			}

			// Clipped to the tile's own square on the seams, and to the padded
			// surface on the outside: intersecting with the surface itself
			// would throw away the geometry just beyond the edge whose ink
			// belongs on the image.
			clip := square.intersect(keep)
			if clip.empty() {
				continue
			}
			tiles = append(tiles, drawTile{ref: ref, tile: tile, clip: clip, overzoom: ref != want})
		}
	}
	return tiles, cov, nil
}

// walkUp returns the deepest tile at or above want that the source holds.
//
// Vector geometry drawn from a shallower tile is a real, sharp, less detailed
// map rather than a blurred one, which is why this is the first answer to a
// miss and not the last. A miss NEVER fetches anything: working offline at
// render time is the whole product claim, and a render that quietly reached the
// network because a cell had been evicted would be the data exfiltration this
// library exists to remove, happening at the moment the user least expected it.
// The TileSource interface is what makes that structural -- there is nothing
// here to fetch with.
func (r *Renderer) walkUp(want tileRef) (tileRef, []byte, bool, error) {
	for ref := want; ; ref = ref.parent() {
		data, ok, err := r.src.Tile(ref.z, ref.x, ref.y)
		if err != nil {
			return tileRef{}, nil, false, fmt.Errorf("render: reading tile %s: %w", ref, err)
		}
		if ok {
			return ref, data, true, nil
		}
		if ref.z == 0 {
			return tileRef{}, nil, false, nil
		}
	}
}

// outward rounds a surface rectangle to whole pixels, never inward.
//
// A gap rounded inward would leave a fringe of un-hatched background around
// every hole in the map, which reads as ocean and is the one thing the hatch
// exists to rule out.
func outward(b box) image.Rectangle {
	if b.empty() {
		return image.Rectangle{}
	}
	return image.Rect(
		int(math.Floor(b.MinX)), int(math.Floor(b.MinY)),
		int(math.Ceil(b.MaxX)), int(math.Ceil(b.MaxY)),
	)
}
