package raster_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/wisborg/osmbase/raster"
)

// The dash phase Stroke and Dash return is the whole mechanism for drawing one
// feature that arrives in pieces -- clipped at a tile edge, or cut where OSM
// started a new way -- without a phase jump at every cut. Before this file
// nothing asserted it: every call in the package's tests read the runs and
// dropped the second return value with `_`, so the phase could have been
// returned as zero, as the input, or as the wrong entry of the pattern and
// every test would still have passed.
//
// What that would look like in a map is a footpath whose dashes restart at
// every tile boundary. It is the same "invisible within one tile, obvious
// across one" failure as the coverage seam, on the same lines, and it is not
// visible in a golden image drawn inside a single tile.

// nearlyPhase compares phases. They are float32 sums of arc lengths, so the
// tolerance is there for the accumulation and not for the rule.
func nearlyPhase(got, want float32) bool {
	return math.Abs(float64(got)-float64(want)) <= 1e-4
}

// straight returns a horizontal polyline of the given arc length.
func straight(length float32) []raster.Point {
	return []raster.Point{{X: 0, Y: 0}, {X: length, Y: 0}}
}

// TestDash_EndPhaseIsTheStartPhasePlusTheArcLengthModuloThePattern derives
// every expected value from the definition of the phase rather than from the
// walk: a phase is a distance into the pattern, the line carries the cursor
// forward by its own arc length, and the pattern repeats with period `total`.
//
// The period is the sum of the pattern for an even-length one and TWICE that
// for an odd-length one, because an odd array is repeated to make it even --
// SVG's rule, which Stroke's documentation adopts. That is why [6 2 4] is
// worked modulo 24 below and not modulo 12, and the case at phase 12 is the
// one that can tell those apart.
func TestDash_EndPhaseIsTheStartPhasePlusTheArcLengthModuloThePattern(t *testing.T) {
	tests := []struct {
		name    string
		line    []raster.Point
		pattern []float32
		phase   float32
		want    float32 // (phase + arc length) mod total, worked by hand
	}{
		// total 8.
		{"whole number of cycles", straight(16), []float32{4, 4}, 0, 0},                                     // 16 = 2*8
		{"part of a cycle", straight(10), []float32{4, 4}, 0, 2},                                            // 10 - 8
		{"carried on from a phase", straight(10), []float32{4, 4}, 3, 5},                                    // 13 - 8
		{"landing exactly on the cycle", straight(5), []float32{4, 4}, 3, 0},                                // 3 + 5 = 8
		{"from a negative phase", straight(4), []float32{4, 4}, -6, 6},                                      // -6 mod 8 = 2, + 4
		{"from a phase past the cycle", straight(4), []float32{4, 4}, 802, 6},                               // 802 = 100*8 + 2
		{"across a bend", []raster.Point{{X: 0, Y: 0}, {X: 8, Y: 0}, {X: 8, Y: 8}}, []float32{10, 2}, 0, 4}, // 16 - 12
		// total 24, because [6 2 4] doubles.
		{"an odd pattern over its doubled cycle", straight(24), []float32{6, 2, 4}, 0, 0},
		{"an odd pattern part way", straight(10), []float32{6, 2, 4}, 0, 10},
		{"an odd pattern from the second half", straight(12), []float32{6, 2, 4}, 12, 0}, // 24 mod 24
		// total 10, because [5] doubles to [5 5].
		{"a one-entry pattern", straight(7), []float32{5}, 0, 7},
		// A line with no length moves the cursor not at all.
		{"a single point", []raster.Point{{X: 3, Y: 3}}, []float32{4, 4}, 1, 1},
		{"no points at all", nil, []float32{4, 4}, 5, 5},
		{"repeated points", []raster.Point{{X: 3, Y: 3}, {X: 3, Y: 3}}, []float32{4, 4}, 5, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := raster.Dash(tc.line, tc.pattern, tc.phase); !nearlyPhase(got, tc.want) {
				t.Errorf("Dash(%v, %v, phase %v) ended at phase %v, want %v", tc.line, tc.pattern, tc.phase, got, tc.want)
			}
			var p raster.Path
			got := p.Stroke(tc.line, raster.Stroke{Width: 2, Dash: tc.pattern, DashPhase: tc.phase})
			if !nearlyPhase(got, tc.want) {
				t.Errorf("Stroke(%v, %v, phase %v) ended at phase %v, want %v", tc.line, tc.pattern, tc.phase, got, tc.want)
			}
		})
	}
}

// TestStroke_EndPhaseIsInsideTheCycleSoItCanBeFedStraightBack is the property
// that makes threading safe to do a thousand times down one road.
//
// The returned phase is what the next piece is given, so if it could come back
// as the whole period -- 8 rather than 0 for [4 4] -- the next piece would
// reduce it and get the same answer, but a caller comparing it against the
// pattern, or an animation adding to it, would drift by one period per piece.
func TestStroke_EndPhaseIsInsideTheCycleSoItCanBeFedStraightBack(t *testing.T) {
	pattern := []float32{4, 4}
	const total = 8
	phase := float32(0)
	for i := 0; i < 50; i++ {
		var p raster.Path
		// A length chosen to land exactly on the cycle boundary every other
		// piece, which is where a phase equal to the period would appear.
		phase = p.Stroke(straight(4), raster.Stroke{Width: 2, Dash: pattern, DashPhase: phase})
		if !(phase >= 0 && phase < total) {
			t.Fatalf("after %d pieces the phase is %v, want it inside [0, %v)", i+1, phase, total)
		}
	}
}

// TestStroke_ASolidPieceReturnsThePhaseItWasGiven covers the piece of a dashed
// feature that is drawn without a pattern -- and, more usefully, a caller that
// threads the phase through a whole layer without looking at whether each
// stroke happens to be dashed.
//
// Nothing advances, because there is no pattern for a distance to be a
// distance INTO. Returning zero instead would look identical on a solid road
// and would silently reset the dashing of whatever came next.
func TestStroke_ASolidPieceReturnsThePhaseItWasGiven(t *testing.T) {
	for _, phase := range []float32{0, 7, -3, 1000} {
		var p raster.Path
		if got := p.Stroke(straight(10), raster.Stroke{Width: 2, DashPhase: phase}); got != phase {
			t.Errorf("a solid stroke given phase %v returned %v, want it unchanged", phase, got)
		}
	}
}

// TestStroke_APieceTooThinToDrawStillAdvancesThePhase is the case the width
// guard makes easy to get wrong: the early return that skips the drawing must
// not also skip the walk.
//
// A style width is multiplied by the view's scale, so at a shallow zoom a road
// class can scale to zero and every piece of it draws nothing. If those pieces
// dropped the phase, the pieces that DO draw -- further down the same way at
// the same zoom, or the same way at the next zoom -- would restart the pattern.
//
// The expected value is the same derivation as above: 1 + 10 = 11, modulo 8,
// is 3.
func TestStroke_APieceTooThinToDrawStillAdvancesThePhase(t *testing.T) {
	for _, w := range []float32{0, -4, float32(math.NaN())} {
		var p raster.Path
		got := p.Stroke(straight(10), raster.Stroke{Width: w, Dash: []float32{4, 4}, DashPhase: 1})
		if !nearlyPhase(got, 3) {
			t.Errorf("a stroke of width %v returned phase %v, want 3", w, got)
		}
		if !p.Empty() {
			t.Errorf("a stroke of width %v appended geometry to the path", w)
		}
	}
}

// mergeAbutting joins runs that meet end to end.
//
// Dashing one line and dashing its two halves cannot produce literally the same
// runs: a dash that spans the cut comes back as one run from the whole line and
// as two abutting runs from the pieces. Joining those back up is the only
// normalisation applied, and it is done by exact coordinate equality on the cut
// point, so it cannot hide a phase that is off by anything at all.
func mergeAbutting(runs [][]raster.Point) [][]raster.Point {
	var out [][]raster.Point
	for _, r := range runs {
		if n := len(out); n > 0 {
			prev := out[n-1]
			if prev[len(prev)-1] == r[0] {
				out[n-1] = append(prev, r[1:]...)
				continue
			}
		}
		out = append(out, append([]raster.Point(nil), r...))
	}
	return out
}

// TestDash_PiecesThreadedByPhaseReproduceTheUnsplitPattern is the property the
// returned phase exists for, stated as the thing a renderer actually does.
//
// One way is cut into pieces by a tile edge. The caller dashes the pieces in
// order, feeding each the phase the last one returned, and the picture must be
// the one the uncut way would have produced -- not approximately, but the same
// dashes in the same places.
//
// The cut is placed at three different points, including one in the middle of
// a dash and one in the middle of a gap, because those are the two cases and a
// fixture that only cuts between dashes would pass with the phase thrown away.
// The paired assertion at the end is what makes this test discriminating: it
// checks that restarting the pattern at the cut, which is what a caller that
// ignored the returned phase would do, produces a DIFFERENT answer. Without
// that, an implementation whose pattern happened to be periodic at the cut
// would pass and prove nothing.
func TestDash_PiecesThreadedByPhaseReproduceTheUnsplitPattern(t *testing.T) {
	// A way with two bends, 30 long: 12 across, 9 down, 9 across.
	whole := []raster.Point{{X: 0, Y: 0}, {X: 12, Y: 0}, {X: 12, Y: 9}, {X: 21, Y: 9}}
	pattern := []float32{5, 3}

	// Cuts at arc length 7 (inside the first dash: 5 on, 3 off, so 7 is in a
	// gap), 13 (inside a dash: the cycle is 8, so 13 is 5 into the third
	// entry) and 21 (exactly on a vertex of the polyline).
	cuts := []struct {
		name   string
		at     raster.Point
		before []raster.Point
		after  []raster.Point
	}{
		{"in a gap", raster.Point{X: 7, Y: 0},
			[]raster.Point{{X: 0, Y: 0}, {X: 7, Y: 0}},
			[]raster.Point{{X: 7, Y: 0}, {X: 12, Y: 0}, {X: 12, Y: 9}, {X: 21, Y: 9}}},
		{"in a dash", raster.Point{X: 12, Y: 1},
			[]raster.Point{{X: 0, Y: 0}, {X: 12, Y: 0}, {X: 12, Y: 1}},
			[]raster.Point{{X: 12, Y: 1}, {X: 12, Y: 9}, {X: 21, Y: 9}}},
		{"on a vertex", raster.Point{X: 12, Y: 9},
			[]raster.Point{{X: 0, Y: 0}, {X: 12, Y: 0}, {X: 12, Y: 9}},
			[]raster.Point{{X: 12, Y: 9}, {X: 21, Y: 9}}},
	}

	want, _ := raster.Dash(whole, pattern, 0)
	for _, cut := range cuts {
		t.Run(cut.name, func(t *testing.T) {
			first, phase := raster.Dash(cut.before, pattern, 0)
			second, _ := raster.Dash(cut.after, pattern, phase)
			got := mergeAbutting(append(append([][]raster.Point(nil), first...), second...))
			if !samePoints(got, want) {
				t.Errorf("cutting at %v and threading the phase gave\n got  %v\n want %v", cut.at, got, want)
			}

			restarted, _ := raster.Dash(cut.after, pattern, 0)
			naive := mergeAbutting(append(append([][]raster.Point(nil), first...), restarted...))
			if samePoints(naive, want) {
				t.Fatalf("the fixture is wrong: restarting the pattern at %v gives the same picture, so this cut cannot detect a dropped phase", cut.at)
			}
		})
	}
}

// TestStroke_PiecesThreadedByPhaseDrawTheSameInkAsTheUnsplitLine says the same
// thing in pixels, because Path.Stroke dashes without going through Dash.
//
// The comparison is ink and gap at sampled points rather than pixel for pixel:
// a dash that spans the cut is stroked as two runs, so it gets a round cap at
// each side of the cut, and two half-covered rims summing to one is the
// documented hardening rather than a difference in the pattern. Every probe
// therefore sits two pixels clear of any dash boundary, where the two pictures
// must agree exactly.
func TestStroke_PiecesThreadedByPhaseDrawTheSameInkAsTheUnsplitLine(t *testing.T) {
	const y = 8
	pattern := []float32{6, 6}
	line := func(x0, x1 float32) []raster.Point {
		return []raster.Point{{X: x0, Y: y}, {X: x1, Y: y}}
	}

	// 6 on from 0, 6 off from 6, 6 on from 12, 6 off from 18, 6 on from 24.
	probes := []struct {
		x    int
		want float64
	}{{3, 1}, {9, 0}, {15, 1}, {21, 0}, {27, 1}}

	whole := raster.NewSurface(32, 16)
	var p raster.Path
	p.Stroke(line(0, 30), raster.Stroke{Width: 4, Dash: pattern})
	whole.Fill(&p, ink)

	// Cut at x = 14, two units into the second dash.
	pieces := raster.NewSurface(32, 16)
	p.Reset()
	phase := p.Stroke(line(0, 14), raster.Stroke{Width: 4, Dash: pattern})
	p.Stroke(line(14, 30), raster.Stroke{Width: 4, Dash: pattern, DashPhase: phase})
	pieces.Fill(&p, ink)

	// And the mistake, for the pair: the second piece restarting at phase 0.
	restarted := raster.NewSurface(32, 16)
	p.Reset()
	p.Stroke(line(0, 14), raster.Stroke{Width: 4, Dash: pattern})
	p.Stroke(line(14, 30), raster.Stroke{Width: 4, Dash: pattern})
	restarted.Fill(&p, ink)

	agrees := true
	for _, probe := range probes {
		if got := coverageAt(whole, probe.x, y); !nearly(got, probe.want, 0.004) {
			t.Fatalf("the fixture is wrong: the unsplit line has coverage %.4f at x=%d, want %v", got, probe.x, probe.want)
		}
		if got := coverageAt(pieces, probe.x, y); !nearly(got, probe.want, 0.004) {
			t.Errorf("the threaded pieces have coverage %.4f at x=%d, want %v", got, probe.x, probe.want)
		}
		if got := coverageAt(restarted, probe.x, y); !nearly(got, probe.want, 0.004) {
			agrees = false
		}
	}
	if agrees {
		t.Fatal("the fixture is wrong: restarting the pattern at the cut draws the same ink, so this test cannot detect a dropped phase")
	}
}

// TestDash_APhaseLandingExactlyOnAPatternBoundaryStartsTheNextEntry is the
// boundary of the phase reduction, and the one a threaded caller hits
// constantly: a piece whose length is a whole number of cycles returns a phase
// of exactly 0, and any piece can end exactly on an entry boundary.
//
// With [4 4] and a phase of 4, the first entry is used up and the line begins
// in the GAP: ink over [4, 8), [12, 16), and nothing before 4. An
// implementation that treated "phase equal to the remaining length" as still
// inside the entry would start in ink instead, emitting a zero-length dash at
// the very start -- which under round caps is a dot at the end of the previous
// piece, in the middle of a gap.
func TestDash_APhaseLandingExactlyOnAPatternBoundaryStartsTheNextEntry(t *testing.T) {
	line := straight(16)
	tests := []struct {
		phase float32
		want  [][]raster.Point
	}{
		{0, [][]raster.Point{{{X: 0, Y: 0}, {X: 4, Y: 0}}, {{X: 8, Y: 0}, {X: 12, Y: 0}}}},
		{4, [][]raster.Point{{{X: 4, Y: 0}, {X: 8, Y: 0}}, {{X: 12, Y: 0}, {X: 16, Y: 0}}}},
		{8, [][]raster.Point{{{X: 0, Y: 0}, {X: 4, Y: 0}}, {{X: 8, Y: 0}, {X: 12, Y: 0}}}},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("phase%v", tc.phase), func(t *testing.T) {
			if got, _ := raster.Dash(line, []float32{4, 4}, tc.phase); !samePoints(got, tc.want) {
				t.Errorf("phase %v gave\n got  %v\n want %v", tc.phase, got, tc.want)
			}
		})
	}
}

// TestDash_AnOddPatternPhaseIsMeasuredAgainstTheDoubledCycle is the test the
// existing odd-length test looks like it already is, and is not.
//
// [6 2 4] walked from phase 0 produces the same runs whether the array is
// doubled or not, because the walk toggles ink and gap as it goes rather than
// reading the parity of the index at each step. So the existing test passes
// with the doubling removed, and the SVG convention it claims to pin is
// unpinned. What actually depends on the doubling is the phase: the cycle is
// 24 rather than 12, and a phase of 12 is the start of the second half, where
// the roles are swapped.
//
// At phase 12 the pattern reads off 6, on 2, off 4. So a line 12 long draws
// nothing for its first 6 units, ink from 6 to 8, and nothing after. With an
// undoubled [6 2 4] a phase of 12 would reduce to 0 and the line would start
// with 6 units of ink -- the inverse picture.
func TestDash_AnOddPatternPhaseIsMeasuredAgainstTheDoubledCycle(t *testing.T) {
	line := straight(12)
	want := [][]raster.Point{{{X: 6, Y: 0}, {X: 8, Y: 0}}}
	got, end := raster.Dash(line, []float32{6, 2, 4}, 12)
	if !samePoints(got, want) {
		t.Errorf("[6 2 4] at phase 12 gave\n got  %v\n want %v", got, want)
	}
	if !nearlyPhase(end, 0) {
		t.Errorf("[6 2 4] at phase 12 over 12 units ended at phase %v, want 0 (24 modulo the 24-long cycle)", end)
	}
}
