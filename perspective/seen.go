package perspective

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
)

// TileID names a tile of a pyramid: the slippy-map tile z/x/y.
type TileID struct {
	Z    uint8
	X, Y uint32
}

// TilesSeen is every tile of a pyramid of zooms minZoom to maxZoom that the
// frames would be drawn from, at each zoom, without drawing any: the ground
// a flight sees and the detail it sees it at, for a program to fetch the map
// those tiles are drawn from before it draws them. frame gives each frame's
// scene and camera as PlanNames's does, Tiles aside -- the scene's Tiles are
// not consulted, and need not be able to draw. The frames are drawn small, as
// a plan's are, with the zooms of the full size.
func TilesSeen(ctx context.Context, frames []int, frame PlanFrame, minZoom, maxZoom uint8) ([]TileID, error) {
	none, err := NewTiles(func(context.Context, uint8, uint32, uint32) (*Tile, error) {
		return nil, fmt.Errorf("perspective: TilesSeen draws no tile")
	}, minZoom, maxZoom, 0)
	if err != nil {
		return nil, err
	}
	seen := map[tileKey]bool{}
	for _, i := range frames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s, c, o, err := frame(i)
		if err != nil {
			return nil, err
		}
		s.Tiles, s.Map = none, nil
		small := o
		small.Width = max(1, int(math.Round(float64(o.Width)/planScale)))
		small.Height = max(1, int(math.Round(float64(o.Height)/planScale)))
		pic, err := renderScene(ctx, s, c, small, &planning{scale: float64(o.Width) / float64(small.Width), noted: true})
		if err != nil {
			return nil, err
		}
		maps.Copy(seen, pic.noted)
	}
	out := make([]TileID, 0, len(seen))
	for k := range seen {
		out = append(out, TileID{Z: k.z, X: k.x, Y: k.y})
	}
	slices.SortFunc(out, func(a, b TileID) int {
		return cmp.Or(cmp.Compare(a.Z, b.Z), cmp.Compare(a.Y, b.Y), cmp.Compare(a.X, b.X))
	})
	return out, nil
}
