package render_test

import (
	"testing"

	"github.com/wisborg/osmbase/mvt"
)

// A road just beyond a view's edge is drawn where its width reaches into the
// view. Its centreline is in the neighbouring tile, two pixels past the
// edge, and the road is eight wide, so the view's last two columns are road.
// A view of tile 0 alone used to read only the tiles under the view itself
// and leave those columns as land -- and a picture assembled from views of
// neighbouring tiles, as a flyover's is, cut every road lying along a tile
// edge in half, lengthwise.
func TestEdge_AStrokeFromBeyondTheViewReachesIntoIt(t *testing.T) {
	src := newSource()
	src.put(t, 10, 0, 0, wholeTile("earth", ""))
	src.put(t, 10, 1, 0, wholeTile("earth", ""), lineLayer("roads", "", mvt.Point{X: 32, Y: 0}, mvt.Point{X: 32, Y: 4096}))
	img := draw(t, src, tileView(t, 10, 0, 0, 0, 0, 256)).Image
	for _, x := range []int{254, 255} {
		at(t, img, x, 128, testPalette.Road, "the road beyond the edge, where its width reaches into the view")
	}
	at(t, img, 250, 128, testPalette.Land, "the land beyond the road's reach")
}
