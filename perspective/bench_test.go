package perspective

import (
	"context"
	"math"
	"sync"
	"testing"

	"github.com/wisborg/osmbase/render"
)

// cachedHeights is a HeightSource answering each tile once, as a real
// store's decoded tiles are kept: a benchmark of drawing, not of the
// formula a synthetic hill is made from.
type cachedHeights struct {
	src   render.HeightSource
	mu    sync.Mutex
	tiles map[tileKey][]float32
	n     int
}

func (c *cachedHeights) Heights(z uint8, x, y uint32) ([]float32, int, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := tileKey{z, x, y}
	if t, ok := c.tiles[k]; ok {
		return t, c.n, true, nil
	}
	t, n, ok, err := c.src.Heights(z, x, y)
	if err != nil || !ok {
		return t, n, ok, err
	}
	c.tiles[k], c.n = t, n
	return t, n, true, nil
}

// hillyFlight is a pyramid over rolling hills and the camera of each of
// frames frames of a flight along it.
func hillyFlight(tb testing.TB, frames int) (Scene, func(i int) Camera) {
	tiles, _ := flatTiles(tb)
	target := render.Coord{Lat: -33.70, Lon: 151.10}
	hills := shape{centre: target, h: func(e, n float64) float64 {
		return 200 + 80*math.Sin(e/700)*math.Cos(n/900)
	}}
	heights := &cachedHeights{src: hills, tiles: map[tileKey][]float32{}}
	cam := func(i int) Camera {
		t := float64(i) / float64(max(1, frames))
		return Camera{
			Target:   render.Coord{Lat: target.Lat + 0.01*t, Lon: target.Lon + 0.01*t},
			Distance: 800, Heading: 45 + 20*t, Pitch: 35,
		}
	}
	b0 := cam(0).MapBounds(16.0 / 9)
	return Scene{Tiles: tiles, View: cam(0).MapView(b0, 300, 1024), Heights: heights}, cam
}

// BenchmarkRenderFrame is one 1920x1080 frame of a flyover over hills,
// drawn from a pyramid whose tiles are already drawn.
func BenchmarkRenderFrame(b *testing.B) {
	s, cam := hillyFlight(b, 1)
	o := Options{Width: 1920, Height: 1080}
	if _, err := Render(s, cam(0), o); err != nil { // the tiles, drawn once
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := Render(s, cam(0), o); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPlanNames plans the names of a short flight's frames.
func BenchmarkPlanNames(b *testing.B) {
	const frames = 24
	s, cam := hillyFlight(b, frames)
	o := Options{Width: 1920, Height: 1080}
	frame := func(i int) (Scene, Camera, Options, error) { return s, cam(i), o, nil }
	if _, err := PlanNames(context.Background(), frames, frame, 2, nil); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := PlanNames(context.Background(), frames, frame, 2, nil); err != nil {
			b.Fatal(err)
		}
	}
}
