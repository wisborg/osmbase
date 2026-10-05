// Package dem reads elevation tiles: images whose colours encode the height
// of the ground, decoded into grids of metres.
//
// It is the decoding half of terrain and nothing else. Copying the tiles onto
// this machine is the terrain package's, and drawing with the heights is the
// renderer's; this package sits between them and reaches neither the network
// nor an archive. A Source reads through a TileSource, the same one-method
// interface the renderer draws vector tiles from, so a store's elevation
// source is read exactly as its map is.
//
// # Terrarium
//
// The encoding is Terrarium's, which is Mapterhorn's and Mapzen's before it:
// height = R*256 + G + B/256 - 32768 metres, so a pixel spans -32768 to
// +32767.996 m in steps of 1/256 m. The tiles must be stored losslessly for
// that to mean anything -- a lossy WebP blurs R and G into each other and a
// rounding of one in R is 256 m -- and Mapterhorn's are lossless VP8L. Both
// WebP and PNG are read; which one a tile is comes from its own first bytes,
// not from what the archive claimed.
package dem

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"sync"

	"golang.org/x/image/webp"
)

// Grid is one tile's heights: Size by Size samples in metres, row by row
// from the north-west corner. Sample (i, j) is the height at the centre of
// the tile's pixel in column i, row j.
type Grid struct {
	Size    int
	Heights []float32
}

// At is the height at column i, row j.
func (g Grid) At(i, j int) float32 { return g.Heights[j*g.Size+i] }

// ErrNotSquare is returned for a tile whose image is not square, which no
// tile pyramid produces and which would make a grid's rows and columns mean
// different distances.
var ErrNotSquare = errors.New("dem: elevation tile is not square")

// Decode reads a Terrarium-encoded WebP or PNG tile into a Grid.
func Decode(data []byte) (Grid, error) {
	var (
		img image.Image
		err error
	)
	switch {
	case bytes.HasPrefix(data, []byte("RIFF")) && len(data) >= 12 && string(data[8:12]) == "WEBP":
		img, err = webp.Decode(bytes.NewReader(data))
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		img, err = png.Decode(bytes.NewReader(data))
	default:
		return Grid{}, fmt.Errorf("dem: elevation tile is neither WebP nor PNG (starts %q)", head(data))
	}
	if err != nil {
		return Grid{}, fmt.Errorf("dem: decoding elevation tile: %w", err)
	}
	b := img.Bounds()
	if b.Dx() != b.Dy() || b.Dx() == 0 {
		return Grid{}, fmt.Errorf("%w: %d by %d", ErrNotSquare, b.Dx(), b.Dy())
	}
	n := b.Dx()
	g := Grid{Size: n, Heights: make([]float32, n*n)}
	// The two concrete types the decoders return are read directly: through
	// At, a 512-pixel tile is a quarter of a million interface calls and an
	// allocation each.
	switch m := img.(type) {
	case *image.NRGBA:
		for j := 0; j < n; j++ {
			row := m.Pix[j*m.Stride:]
			for i := 0; i < n; i++ {
				g.Heights[j*n+i] = terrarium(row[4*i], row[4*i+1], row[4*i+2])
			}
		}
	case *image.RGBA:
		// Premultiplied, but an elevation tile is opaque, so the colour
		// is the colour.
		for j := 0; j < n; j++ {
			row := m.Pix[j*m.Stride:]
			for i := 0; i < n; i++ {
				g.Heights[j*n+i] = terrarium(row[4*i], row[4*i+1], row[4*i+2])
			}
		}
	default:
		for j := 0; j < n; j++ {
			for i := 0; i < n; i++ {
				r, gg, bb, _ := img.At(b.Min.X+i, b.Min.Y+j).RGBA()
				g.Heights[j*n+i] = terrarium(uint8(r>>8), uint8(gg>>8), uint8(bb>>8))
			}
		}
	}
	return g, nil
}

// terrarium is the height one pixel encodes.
func terrarium(r, g, b uint8) float32 {
	return float32(r)*256 + float32(g) + float32(b)/256 - 32768
}

func head(b []byte) []byte {
	if len(b) > 8 {
		return b[:8]
	}
	return b
}

// TileSource is where elevation tiles come from: the renderer's interface,
// declared again here so that this package need not import the renderer.
// A slice.Source satisfies both.
type TileSource interface {
	Tile(z uint8, x, y uint32) (data []byte, ok bool, err error)
}

// Source decodes elevation tiles from a TileSource, keeping the most recent
// ones decoded.
//
// The cache is what makes it usable from a renderer, which asks for each
// tile once per output pixel row rather than once per render. It holds a
// fixed number of grids -- a 512-pixel grid is a megabyte -- and forgets the
// oldest first. A Source is safe for concurrent use when its TileSource is.
type Source struct {
	tiles TileSource
	limit int

	mu    sync.Mutex
	grids map[tileKey]cached
	order []tileKey
}

type tileKey struct {
	z    uint8
	x, y uint32
}

type cached struct {
	grid Grid
	ok   bool
}

// DefaultCache is how many decoded tiles a Source keeps: enough for every
// elevation tile under a 4K view, with its margin, twice over.
const DefaultCache = 64

// NewSource returns a Source reading tiles and keeping DefaultCache of them
// decoded.
func NewSource(tiles TileSource) *Source {
	return &Source{tiles: tiles, limit: DefaultCache, grids: map[tileKey]cached{}}
}

// Heights is the grid of tile z/x/y. ok is false when the source holds no
// such tile, which is an answer and not an error: the caller walks up to a
// shallower zoom, as the renderer does for the map. A tile that is held and
// cannot be decoded is an error.
func (s *Source) Heights(z uint8, x, y uint32) (heights []float32, size int, ok bool, err error) {
	k := tileKey{z, x, y}
	s.mu.Lock()
	c, hit := s.grids[k]
	s.mu.Unlock()
	if hit {
		return c.grid.Heights, c.grid.Size, c.ok, nil
	}
	data, ok, err := s.tiles.Tile(z, x, y)
	if err != nil {
		return nil, 0, false, fmt.Errorf("dem: reading elevation tile %d/%d/%d: %w", z, x, y, err)
	}
	var g Grid
	if ok {
		if g, err = Decode(data); err != nil {
			return nil, 0, false, fmt.Errorf("elevation tile %d/%d/%d: %w", z, x, y, err)
		}
	}
	s.mu.Lock()
	if _, again := s.grids[k]; !again {
		s.grids[k] = cached{grid: g, ok: ok}
		s.order = append(s.order, k)
		for len(s.order) > s.limit {
			delete(s.grids, s.order[0])
			s.order = s.order[1:]
		}
	}
	s.mu.Unlock()
	return g.Heights, g.Size, ok, nil
}
