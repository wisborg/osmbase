package mvt

import (
	"math/big"
	"math/rand"
	"testing"
)

// These are internal tests because they assert which of Winding's two
// accumulators ran. Checking the answer alone would pass on a build where the
// 128-bit path was unreachable, which is the state this file was written to
// leave behind.

// exactWinding computes the winding independently, in arbitrary precision and
// with no translation.
//
// It is the reference the tests below compare against, and it is deliberately
// not a second copy of the implementation: math/big cannot overflow, the
// surveyor's formula is written out term by term, and the translation -- whose
// only defence is an algebraic identity -- is absent, so agreement is evidence
// about both.
//
// math/big is standard library and this is a test, so it costs the module
// nothing. It has no business anywhere near the decoder itself.
func exactWinding(r Ring) int {
	if len(r) < 3 {
		return 0
	}
	sum, term := new(big.Int), new(big.Int)
	for i := range r {
		a, b := r[i], r[(i+1)%len(r)]
		sum.Add(sum, term.Mul(big.NewInt(int64(a.X)), big.NewInt(int64(b.Y))))
		sum.Sub(sum, term.Mul(big.NewInt(int64(b.X)), big.NewInt(int64(a.Y))))
	}
	return sum.Sign()
}

// TestRing_WindingAtTheExtremesOfInt32 is the case that used to lie.
//
// The square below spans the whole int32 range and is wound clockwise on
// screen -- right along the top, down the right side, left along the bottom --
// which is the specification's exterior. Twice its signed area is
// (2^32-1)^2 * 2, about 3.4e19, which does not fit in an int64: accumulated in
// one it wrapped negative and Winding answered HoleWinding, so a rasterizer
// filling absolute winding would have filled the hole solid.
//
// Both orientations are listed, because a sign error that inverted everything
// would satisfy either one alone.
func TestRing_WindingAtTheExtremesOfInt32(t *testing.T) {
	const lo, hi = -1 << 31, 1<<31 - 1

	clockwise := Ring{{lo, lo}, {hi, lo}, {hi, hi}, {lo, hi}}
	anticlockwise := Ring{{lo, hi}, {hi, hi}, {hi, lo}, {lo, lo}}

	cases := []struct {
		name string
		ring Ring
		want int
	}{
		{"the whole int32 plane, wound as an exterior", clockwise, ExteriorWinding},
		{"the whole int32 plane, wound as a hole", anticlockwise, HoleWinding},
		{"a square at the top corner", Ring{{hi - 10, hi - 10}, {hi, hi - 10}, {hi, hi}, {hi - 10, hi}}, ExteriorWinding},
		{"a square straddling the origin at half scale", Ring{{-1 << 30, -1 << 30}, {1 << 30, -1 << 30}, {1 << 30, 1 << 30}, {-1 << 30, 1 << 30}}, ExteriorWinding},
		{"a degenerate ring along the full diagonal", Ring{{lo, lo}, {0, 0}, {hi, hi}}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exactWinding(c.ring); got != c.want {
				t.Fatalf("the reference says %d, and this test claims %d; the fixture is wrong, not the code", got, c.want)
			}
			if got := c.ring.Winding(); got != c.want {
				t.Errorf("Winding() = %d, want %d", got, c.want)
			}
		})
	}

	// The point of the whole exercise: this ring must NOT take the int64 path,
	// and taking it must be why it used to be wrong.
	if fitsInt64Path(clockwise) {
		t.Error("the full-int32 square claims to fit an int64 accumulator, which is what made it answer HoleWinding")
	}
	if got := signOf(shoelace64(clockwise, int64(clockwise[0].X), int64(clockwise[0].Y))); got != HoleWinding {
		t.Errorf("the int64 accumulator gives %d for the full-int32 square; the historical wrong answer was %d, so this test no longer reproduces the defect it guards", got, HoleWinding)
	}
}

// TestRing_WindingAgreesWithArbitraryPrecision sweeps rings across the whole
// int32 range and compares every answer with the arbitrary-precision
// reference.
//
// The generator is seeded, so the sweep is the same set of rings on every run
// and a failure is reproducible. It deliberately mixes scales and offsets: a
// small ring far from the origin is the case the translation exists to keep on
// the fast path, and a ring spanning the plane is the case the 128-bit
// accumulator exists for. The test fails if either path goes unused, because
// a sweep that only ever exercised one of them would prove nothing about the
// other.
func TestRing_WindingAgreesWithArbitraryPrecision(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var narrow, wide int

	for n := 0; n < 20000; n++ {
		ring := make(Ring, 3+rng.Intn(6))
		scale := []int64{1 << 31, 1 << 20, 100}[rng.Intn(3)]
		origin := []int64{0, 1 << 30, -(1 << 30)}[rng.Intn(3)]
		for i := range ring {
			ring[i] = Point{
				X: clampToInt32(origin + rng.Int63n(2*scale) - scale),
				Y: clampToInt32(origin + rng.Int63n(2*scale) - scale),
			}
		}

		if fitsInt64Path(ring) {
			narrow++
		} else {
			wide++
		}
		if got, want := ring.Winding(), exactWinding(ring); got != want {
			t.Fatalf("Winding() = %d, want %d, for %v", got, want, ring)
		}
	}

	if narrow == 0 || wide == 0 {
		t.Errorf("the sweep took the int64 path %d times and the 128-bit path %d times; it must exercise both", narrow, wide)
	}
	t.Logf("%d rings on the int64 path, %d on the 128-bit path", narrow, wide)
}

// TestRing_WindingTranslationIsAValueIdentity pins the claim the doc comment
// now makes, rather than the one it used to make.
//
// Subtracting the first point leaves the int64 sum bit-identical, because the
// extra terms telescope to zero around a closed ring and int64 arithmetic is
// that same arithmetic modulo 2^64. The translation buys a smaller multiplicand
// and nothing else -- in particular it buys no exactness, which is what the
// comment wrongly said for as long as the 128-bit path did not exist.
func TestRing_WindingTranslationIsAValueIdentity(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for n := 0; n < 20000; n++ {
		ring := make(Ring, 3+rng.Intn(6))
		for i := range ring {
			ring[i] = Point{X: int32(rng.Uint32()), Y: int32(rng.Uint32())}
		}
		translated := shoelace64(ring, int64(ring[0].X), int64(ring[0].Y))
		plain := shoelace64(ring, 0, 0)
		if translated != plain {
			t.Fatalf("translated sum %d and untranslated sum %d differ for %v", translated, plain, ring)
		}
	}
}

// TestShoelaceFitsInt64 checks the guard at its own boundary, from the
// inequality rather than from the code: n terms of at most 2*extent^2 fit when
// 2*n*extent^2 does not exceed math.MaxInt64.
func TestShoelaceFitsInt64(t *testing.T) {
	cases := []struct {
		name   string
		extent int64
		n      int
		want   bool
	}{
		{"a degenerate ring of no extent", 0, 4, true},
		{"a tile-sized ring", 4096, 4, true},
		{"the decoder's own coordinate bound at a million points", 1 << 20, 1 << 20, true},
		{"the whole int32 range", 1 << 32, 4, false},
		{"one past what four terms can take", 1 << 31, 4, false},
		{"a huge extent with a short ring", 1 << 30, 3, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shoelaceFitsInt64(c.extent, c.n); got != c.want {
				t.Errorf("shoelaceFitsInt64(%d, %d) = %v, want %v", c.extent, c.n, got, c.want)
			}
		})
	}
}

// fitsInt64Path reports which accumulator Winding would choose for a ring,
// repeating the extent measurement so the tests can assert on the choice.
func fitsInt64Path(r Ring) bool {
	if len(r) < 3 {
		return true
	}
	ox, oy := int64(r[0].X), int64(r[0].Y)
	var extent int64
	for _, p := range r {
		if d := abs64(int64(p.X) - ox); d > extent {
			extent = d
		}
		if d := abs64(int64(p.Y) - oy); d > extent {
			extent = d
		}
	}
	return shoelaceFitsInt64(extent, len(r))
}

func clampToInt32(v int64) int32 {
	switch {
	case v > 1<<31-1:
		return 1<<31 - 1
	case v < -1<<31:
		return -1 << 31
	}
	return int32(v)
}
