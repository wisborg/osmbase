package raster_test

import (
	"math"
	"testing"

	"github.com/wisborg/osmbase/raster"
)

// samePoints compares two sets of dash runs.
//
// The comparison is within a tolerance rather than exact because a split point
// is computed as a fraction along a segment, and the fixtures below choose
// lengths that are exact in binary only so that a failure is a real
// disagreement and not a last-bit one. A tenth of a thousandth of a pixel is
// far below anything the rasterizer can express.
func samePoints(got, want [][]raster.Point) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if len(got[i]) != len(want[i]) {
			return false
		}
		for j := range got[i] {
			if math.Abs(float64(got[i][j].X-want[i][j].X)) > 1e-4 ||
				math.Abs(float64(got[i][j].Y-want[i][j].Y)) > 1e-4 {
				return false
			}
		}
	}
	return true
}

// TestDash_RunCrossesASegmentBoundaryWithoutBreaking is the case a dasher that
// works segment by segment gets wrong.
//
// The polyline is 8 across then 8 down, 16 long in total, and the pattern is
// 10 on and 2 off. The first dash therefore starts at the beginning, runs the
// whole first segment, turns the corner and continues 2 into the second: one
// run of three points, with the corner in the middle of it. The gap ends at 12
// and the second dash runs from there to the end of the line.
//
// A dasher that restarted the pattern at each vertex would produce two
// separate dashes here, with a cap and a gap at the corner, and on a footpath
// that shows as a dash boundary at every bend -- which is where a reader's eye
// already is.
func TestDash_RunCrossesASegmentBoundaryWithoutBreaking(t *testing.T) {
	line := []raster.Point{{X: 0, Y: 0}, {X: 8, Y: 0}, {X: 8, Y: 8}}
	want := [][]raster.Point{
		{{X: 0, Y: 0}, {X: 8, Y: 0}, {X: 8, Y: 2}},
		{{X: 8, Y: 4}, {X: 8, Y: 8}},
	}
	if got := raster.Dash(line, []float32{10, 2}, 0); !samePoints(got, want) {
		t.Errorf("dashing 10 on 2 off gave\n got  %v\n want %v", got, want)
	}
}

// TestDash_PhaseStartsThePatternPartWayThrough checks the phase against a
// derivation rather than against the code.
//
// The line is 16 long, the pattern is 4 on and 4 off, and the phase is 2. A
// phase of 2 means the line begins 2 units into the pattern, so the first dash
// has 2 of its 4 units left: ink from 0 to 2, gap from 2 to 6, ink from 6 to
// 10, gap from 10 to 14, ink from 14 to the end at 16.
func TestDash_PhaseStartsThePatternPartWayThrough(t *testing.T) {
	line := []raster.Point{{X: 0, Y: 0}, {X: 16, Y: 0}}
	want := [][]raster.Point{
		{{X: 0, Y: 0}, {X: 2, Y: 0}},
		{{X: 6, Y: 0}, {X: 10, Y: 0}},
		{{X: 14, Y: 0}, {X: 16, Y: 0}},
	}
	if got := raster.Dash(line, []float32{4, 4}, 2); !samePoints(got, want) {
		t.Errorf("dashing 4 on 4 off at phase 2 gave\n got  %v\n want %v", got, want)
	}
}

// TestDash_PhaseIsModuloThePatternAndAcceptsAnyValue covers a caller animating
// a dash, who advances the phase without bound and should not have to reduce
// it, and a style that expresses an offset backwards.
//
// Every phase here names the same point in the cycle as 2 does: 10 is one full
// period further on, -6 is one period back.
func TestDash_PhaseIsModuloThePatternAndAcceptsAnyValue(t *testing.T) {
	line := []raster.Point{{X: 0, Y: 0}, {X: 16, Y: 0}}
	want := raster.Dash(line, []float32{4, 4}, 2)
	for _, phase := range []float32{10, 802, -6, -14} {
		if got := raster.Dash(line, []float32{4, 4}, phase); !samePoints(got, want) {
			t.Errorf("phase %v gave\n got  %v\n want %v (the same as phase 2)", phase, got, want)
		}
	}
}

// TestDash_OddLengthPatternSwapsInkAndGapOnTheSecondCycle pins the borrowed
// convention.
//
// SVG and PostScript both repeat an odd-length array to make it even, so
// [6 2 4] is on 6, off 2, on 4, then off 6, on 2, off 4, a cycle of 24 rather
// than 12. Following it matters because a style is likely to be transcribed
// from a web map, where the same array means this.
func TestDash_OddLengthPatternSwapsInkAndGapOnTheSecondCycle(t *testing.T) {
	line := []raster.Point{{X: 0, Y: 0}, {X: 24, Y: 0}}
	want := [][]raster.Point{
		{{X: 0, Y: 0}, {X: 6, Y: 0}},
		{{X: 8, Y: 0}, {X: 12, Y: 0}},
		{{X: 18, Y: 0}, {X: 20, Y: 0}},
	}
	if got := raster.Dash(line, []float32{6, 2, 4}, 0); !samePoints(got, want) {
		t.Errorf("dashing [6 2 4] gave\n got  %v\n want %v", got, want)
	}
}

// TestDash_APatternThatCannotDashDrawsSolid pins the policy for a broken
// pattern.
//
// A style is data, it can be wrong, and the two candidate answers are a solid
// line or no line. Solid wins: the footpath is still on the map and still
// readable, the mistake is still visible to whoever looks at the style, and no
// road disappears because somebody typed a minus sign.
func TestDash_APatternThatCannotDashDrawsSolid(t *testing.T) {
	line := []raster.Point{{X: 0, Y: 0}, {X: 16, Y: 0}}
	solid := [][]raster.Point{{{X: 0, Y: 0}, {X: 16, Y: 0}}}
	tests := []struct {
		name    string
		pattern []float32
	}{
		{"nil", nil},
		{"empty", []float32{}},
		{"all zero", []float32{0, 0}},
		{"negative", []float32{4, -4}},
		{"NaN", []float32{4, float32(math.NaN())}},
		{"infinite", []float32{4, float32(math.Inf(1))}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := raster.Dash(line, tc.pattern, 0); !samePoints(got, solid) {
				t.Errorf("pattern %v gave\n got  %v\n want %v", tc.pattern, got, solid)
			}
		})
	}
}

// TestDash_DegenerateInputDoesNotPanicOrSpin covers the shapes that arrive
// from a generalised tile, and one -- a zero length inside an otherwise valid
// pattern -- that a naive walk loops forever on.
func TestDash_DegenerateInputDoesNotPanicOrSpin(t *testing.T) {
	tests := []struct {
		name    string
		line    []raster.Point
		pattern []float32
		want    [][]raster.Point
	}{
		{"no points", nil, []float32{4, 4}, nil},
		{"one point", []raster.Point{{X: 1, Y: 1}}, []float32{4, 4},
			[][]raster.Point{{{X: 1, Y: 1}}}},
		{"repeated point", []raster.Point{{X: 1, Y: 1}, {X: 1, Y: 1}}, []float32{4, 4},
			[][]raster.Point{{{X: 1, Y: 1}}}},
		// Zero length ink is a dot, at 0 and at 4. There is no third dot at
		// 8: an ink run that begins exactly where the line ends has no line
		// left to cover, and the walk stops at the line's length rather than
		// completing the cycle past it.
		{"zero length ink", []raster.Point{{X: 0, Y: 0}, {X: 8, Y: 0}}, []float32{0, 4},
			[][]raster.Point{{{X: 0, Y: 0}}, {{X: 4, Y: 0}}}},
		{"zero length gap", []raster.Point{{X: 0, Y: 0}, {X: 8, Y: 0}}, []float32{4, 0},
			[][]raster.Point{{{X: 0, Y: 0}, {X: 4, Y: 0}}, {{X: 4, Y: 0}, {X: 8, Y: 0}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := raster.Dash(tc.line, tc.pattern, 0); !samePoints(got, tc.want) {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
		})
	}
}

// TestStroke_DashedLineLeavesInkAndGapWhereThePatternSaysIs the same property
// measured in pixels rather than in points, because Path.Stroke dashes without
// going through Dash and the two could drift apart.
//
// The line runs from x = 0 to x = 32 at 8 on and 8 off, so there is ink over
// [0, 8) and [16, 24) and none over [8, 16) and [24, 32). The probes sit at
// the middle of each of those, two pixels clear of any boundary, so the round
// caps -- which extend each dash by half the width -- do not reach them.
func TestStroke_DashedLineLeavesInkAndGapWhereThePatternSays(t *testing.T) {
	s := raster.NewSurface(32, 16)
	var p raster.Path
	p.Stroke([]raster.Point{{X: 0, Y: 8}, {X: 32, Y: 8}}, raster.Stroke{
		Width: 4,
		Dash:  []float32{8, 8},
	})
	s.Fill(&p, ink)

	for _, probe := range []struct {
		x    int
		want float64
	}{{4, 1}, {12, 0}, {20, 1}, {28, 0}} {
		if got := coverageAt(s, probe.x, 8); !nearly(got, probe.want, 0.004) {
			t.Errorf("at x=%d the dashed line has coverage %.4f, want %v", probe.x, got, probe.want)
		}
	}
}
