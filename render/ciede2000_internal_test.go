package render

import (
	"math"
	"testing"
)

// TestCIEDE2000AgainstTheReferencePairs checks the formula against the dataset
// published by Sharma, Wu and Dalal alongside their analysis of it.
//
// That dataset exists because CIEDE2000 is easy to implement plausibly and
// wrongly: the hue difference takes the short way round a circle, the mean hue
// has its own wraparound with a different rule, and the rotation term only
// bites near 275 degrees. An implementation that muddles any of those is
// correct for most colours and wrong for a few, which is the shape of error no
// amount of eyeballing a map will find.
//
// The pairs below are the ones that exercise exactly those corners: near the
// blue rotation, and straddling the hue discontinuity where the mean of 90 and
// 300 is 15 rather than 195.
func TestCIEDE2000AgainstTheReferencePairs(t *testing.T) {
	cases := []struct {
		l1, a1, b1 float64
		l2, a2, b2 float64
		want       float64
	}{
		// The blue-rotation group: identical lightness, a* and b* chosen so
		// R_T does the work.
		{50.0000, 2.6772, -79.7751, 50.0000, 0.0000, -82.7485, 2.0425},
		{50.0000, 3.1571, -77.2803, 50.0000, 0.0000, -82.7485, 2.8615},
		{50.0000, 2.8361, -74.0200, 50.0000, 0.0000, -82.7485, 3.4412},
		{50.0000, -1.3802, -84.2814, 50.0000, 0.0000, -82.7485, 1.0000},
		{50.0000, -1.1848, -84.8006, 50.0000, 0.0000, -82.7485, 1.0000},
		{50.0000, -0.9009, -85.5211, 50.0000, 0.0000, -82.7485, 1.0000},

		// Near-neutral, which is the regime this package's own palettes live
		// in, and where the G term matters most.
		{50.0000, 0.0000, 0.0000, 50.0000, -1.0000, 2.0000, 2.3669},
		{50.0000, -1.0000, 2.0000, 50.0000, 0.0000, 0.0000, 2.3669},

		// Lightness-only differences at the ends of the scale, where S_L is
		// furthest from 1 -- the term that made CIE76 flatter this project's
		// palettes.
		{50.0000, 2.5000, 0.0000, 73.0000, 25.0000, -18.0000, 27.1492},
		{50.0000, 2.5000, 0.0000, 61.0000, -5.0000, 29.0000, 22.8977},
		{50.0000, 2.5000, 0.0000, 56.0000, -27.0000, -3.0000, 31.9030},
	}
	for _, c := range cases {
		got := ciede2000(c.l1, c.a1, c.b1, c.l2, c.a2, c.b2)
		if math.Abs(got-c.want) > 0.0002 {
			t.Errorf("ciede2000(L*a*b* %.4f/%.4f/%.4f, %.4f/%.4f/%.4f) = %.4f, want %.4f",
				c.l1, c.a1, c.b1, c.l2, c.a2, c.b2, got, c.want)
		}
	}
}

// TestCIEDE2000IsSymmetric is a property the reference pairs only spot-check.
//
// It matters here because the mean-hue rule is not obviously symmetric to read,
// and CheckContrast compares every pair in one direction only -- so an
// asymmetry would make a palette pass or fail depending on the order the roles
// happen to be listed in.
func TestCIEDE2000IsSymmetric(t *testing.T) {
	samples := []struct{ l, a, b float64 }{
		{50, 2.5, 0}, {73, 25, -18}, {10, -3, 8}, {95, 1, -2}, {50, 0, -82.7},
	}
	for _, p := range samples {
		for _, q := range samples {
			ab := ciede2000(p.l, p.a, p.b, q.l, q.a, q.b)
			ba := ciede2000(q.l, q.a, q.b, p.l, p.a, p.b)
			if math.Abs(ab-ba) > 1e-9 {
				t.Errorf("asymmetric: %.6f one way, %.6f the other", ab, ba)
			}
		}
	}
}
