package render_test

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/render"
)

// lockedSource is mapSource safe for renders on several goroutines, counting
// the reads of each tile.
type lockedSource struct {
	mu    sync.Mutex
	src   *mapSource
	reads map[tileID]int
}

func (s *lockedSource) Tile(z uint8, x, y uint32) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads[tileID{z, x, y}]++
	return s.src.Tile(z, x, y)
}

// A map of four zoom-2 tiles, one held only at zoom 1 so it is walked up to.
func cacheFixture(t *testing.T) *lockedSource {
	src := newSource()
	src.put(t, 2, 0, 0, wholeTile("earth", ""), lineLayer("roads", "minor_road", mvt.Point{X: 0, Y: testExtent / 2}, mvt.Point{X: testExtent, Y: testExtent / 2}))
	src.put(t, 2, 1, 0, wholeTile("earth", ""))
	src.put(t, 2, 0, 1, wholeTile("water", "ocean"))
	src.put(t, 1, 0, 0, wholeTile("earth", ""))
	return &lockedSource{src: src, reads: map[tileID]int{}}
}

// Renders sharing a cache read each tile once, and remember a tile the source
// lacks rather than asking for it again; and the picture is the one drawn
// without a cache, pixel for pixel.
func TestTileCacheReadsEachTileOnce(t *testing.T) {
	src := cacheFixture(t)
	plain, err := render.New(src, render.Options{Style: testStyle(), Palette: testPalette})
	if err != nil {
		t.Fatal(err)
	}
	cached, err := render.New(src, render.Options{Style: testStyle(), Palette: testPalette, TileCache: render.NewTileCache(0)})
	if err != nil {
		t.Fatal(err)
	}
	v := tileView(t, 2, 0, 0, 1, 1, 256)
	want, err := plain.Render(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	src.reads = map[tileID]int{}
	for range 3 {
		got, err := cached.Render(context.Background(), v)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Image.Pix, want.Image.Pix) {
			t.Fatal("a render through the cache differs from one without it")
		}
	}
	for id, n := range src.reads {
		if n != 1 {
			t.Errorf("tile %v read %d times over three renders, want once", id, n)
		}
	}
	if src.reads[tileID{2, 1, 1}] != 1 {
		t.Error("the tile the source lacks was not asked about, or asked again")
	}
}

// Renders on many goroutines at once read a tile once, and draw what one
// alone draws; run under the race detector, this is what says the decoded
// tiles they share are only read.
func TestTileCacheUnderConcurrentRenders(t *testing.T) {
	src := cacheFixture(t)
	r, err := render.New(src, render.Options{Style: testStyle(), Palette: testPalette, TileCache: render.NewTileCache(0)})
	if err != nil {
		t.Fatal(err)
	}
	v := tileView(t, 2, 0, 0, 1, 1, 256)
	want, err := r.Render(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := r.Render(context.Background(), v)
			if err != nil {
				t.Error(err)
				return
			}
			if !bytes.Equal(got.Image.Pix, want.Image.Pix) {
				t.Error("a concurrent render differs")
			}
		}()
	}
	wg.Wait()
	for id, n := range src.reads {
		if n != 1 {
			t.Errorf("tile %v read %d times, want once", id, n)
		}
	}
}

// A cache too small for the tiles keeps few, reading the rest again: the
// budget is kept, and renders still come out the same.
func TestTileCacheKeepsToItsBudget(t *testing.T) {
	src := cacheFixture(t)
	r, err := render.New(src, render.Options{Style: testStyle(), Palette: testPalette, TileCache: render.NewTileCache(1)})
	if err != nil {
		t.Fatal(err)
	}
	v := tileView(t, 2, 0, 0, 1, 1, 256)
	first, err := r.Render(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Render(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Image.Pix, second.Image.Pix) {
		t.Error("renders through a full cache differ")
	}
	if src.reads[tileID{2, 0, 0}] < 2 {
		t.Error("a one-byte cache kept a tile between renders")
	}
}
