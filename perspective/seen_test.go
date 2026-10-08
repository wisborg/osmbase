package perspective

import (
	"context"
	"testing"
)

// The tiles seen are those a frame drawn in full is drawn from, near enough
// -- the frames are seen small, and a tile at the edge of a zoom's band can
// fall either side -- and none is drawn to find them.
func TestTilesSeenAreTheTilesAFrameIsDrawnFrom(t *testing.T) {
	tiles, _ := flatTiles(t)
	s, cam := pyramidScene(tiles)
	o := Options{Width: 400, Height: 300}
	full, err := Render(s, cam, o)
	if err != nil {
		t.Fatal(err)
	}
	seen, err := TilesSeen(context.Background(), []int{0}, func(int) (Scene, Camera, Options, error) {
		return Scene{View: s.View, Heights: s.Heights}, cam, o, nil
	}, 13, 16)
	if err != nil {
		t.Fatal(err)
	}
	got := map[tileKey]bool{}
	for _, id := range seen {
		got[tileKey{z: id.Z, x: id.X, y: id.Y}] = true
	}
	missing := 0
	for k := range full.used {
		if !got[k] {
			missing++
		}
	}
	if missing*10 > len(full.used) {
		t.Errorf("%d of the %d tiles the frame is drawn from were not seen", missing, len(full.used))
	}
	if len(got) > 2*len(full.used) {
		t.Errorf("%d tiles seen, for a frame drawn from %d", len(got), len(full.used))
	}
}
