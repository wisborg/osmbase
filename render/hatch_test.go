package render

import (
	"image"
	"testing"
)

// TestHatchBox_AbuttingGapsAreNotInsetFromEachOther is a regression for a bug
// that was visible in the first picture and invisible in every number.
//
// Gap rectangles are rounded OUTWARD from tile squares, and a tile boundary
// falls at a fraction of a pixel, so two gaps that share an edge come back as
// Max.X 215 and Min.X 214 -- overlapping by one pixel, never equal. An
// adjacency test written as equality therefore found no shared sides at all,
// inset every edge of every gap, and drew the hole in the map as a grid of
// separate patches with a stripe of background between them. The coverage
// figures were correct throughout.
//
// The two gaps here are the left and right halves of a 512 by 256 image,
// overlapping by a pixel the way the rounding produces. The shared sides must
// come back at the rectangles' own edges and the outer sides inset by the
// stroke width.
func TestHatchBox_AbuttingGapsAreNotInsetFromEachOther(t *testing.T) {
	const width = 2.0
	left := image.Rect(0, 0, 257, 256)
	right := image.Rect(256, 0, 512, 256)
	gaps := []image.Rectangle{left, right}

	got := hatchBox(left, gaps, width)
	want := box{MinX: 0 + width, MinY: 0 + width, MaxX: 257, MaxY: 256 - width}
	if got != want {
		t.Errorf("hatchBox for the left gap = %+v, want %+v; its right edge is shared and must not be inset", got, want)
	}

	got = hatchBox(right, gaps, width)
	want = box{MinX: 256, MinY: 0 + width, MaxX: 512 - width, MaxY: 256 - width}
	if got != want {
		t.Errorf("hatchBox for the right gap = %+v, want %+v; its left edge is shared", got, want)
	}
}

// TestHatchBox_ALoneGapIsInsetOnEverySide is the other half of the same
// decision.
//
// A gap with covered ground or the image edge on all four sides must keep its
// hatch inside itself: ink spilling out is this library drawing "nothing is
// known here" across ground it has a tile for, one pixel wide, along every
// edge of every hole.
func TestHatchBox_ALoneGapIsInsetOnEverySide(t *testing.T) {
	const width = 2.0
	g := image.Rect(100, 50, 300, 200)
	got := hatchBox(g, []image.Rectangle{g}, width)
	want := box{MinX: 102, MinY: 52, MaxX: 298, MaxY: 198}
	if got != want {
		t.Errorf("hatchBox = %+v, want %+v", got, want)
	}
}

// TestHatchBox_AGapNarrowerThanTheInsetIsDropped keeps a sliver of a tile at
// the edge of a view from producing an inside-out rectangle, which would clip
// every line away and cost a pass for nothing.
func TestHatchBox_AGapNarrowerThanTheInsetIsDropped(t *testing.T) {
	g := image.Rect(0, 0, 3, 200)
	if b := hatchBox(g, []image.Rectangle{g}, 4); !b.empty() {
		t.Errorf("hatchBox for a 3-pixel gap inset by 4 = %+v, want an empty box", b)
	}
}
