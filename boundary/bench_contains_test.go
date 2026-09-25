package boundary

import (
	"context"
	"math/rand"
	"testing"

	"github.com/wisborg/osmbase/locate"
)

// benchStore is a derived file of a 20 by 20 grid of suburbs inside five
// councils inside a state, which is the shape of a real extract's stack.
func benchStore(b *testing.B) *Source {
	b.Helper()
	root := b.TempDir()
	var areas []Area
	areas = append(areas, NewArea("State", "4", []Polygon{{Outer: square(0, 0, 10)}}))
	for i := range 5 {
		areas = append(areas, NewArea("Council", "6", []Polygon{{Outer: square(float64(i)*2, 0, 2)}}))
	}
	for i := range 20 {
		for j := range 20 {
			areas = append(areas, NewArea("Suburb", "9", []Polygon{{Outer: square(float64(i)*0.5, float64(j)*0.5, 0.5)}}))
		}
	}
	writeDerived(b, root, "bench", osmProv(), areas...)
	return Open(root, DefaultDetail)
}

// BenchmarkAtEachOverDerivedLevels is a route of a thousand points asked for
// the three levels a derived file answers.
func BenchmarkAtEachOverDerivedLevels(b *testing.B) {
	src := benchStore(b)
	r := rand.New(rand.NewSource(1))
	pts := make([]locate.Coord, 1000)
	for i := range pts {
		pts[i] = locate.Coord{Lat: r.Float64() * 10, Lon: r.Float64() * 10}
	}
	opts := locate.Options{Boundaries: src, Levels: []locate.Level{locate.Locality, locate.Macrohood, locate.Neighbourhood}}
	b.ResetTimer()
	for range b.N {
		if _, err := locate.AtEach(context.Background(), nil, pts, opts); err != nil {
			b.Fatal(err)
		}
	}
}
