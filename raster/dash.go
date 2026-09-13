package raster

import "math"

// Dash splits a polyline into the sub-polylines a dash pattern leaves visible.
//
// The pattern is alternating on and off lengths in the same units as the
// points, starting with an on length, and phase is how far into the pattern
// the line begins. Both are described in full on Stroke, whose Dash and
// DashPhase fields are handed straight to this.
//
// A pattern that dashes nothing -- empty, or summing to zero, or carrying a
// negative or NaN length -- returns the polyline unchanged, as one run. A
// footpath whose style is malformed should be a solid footpath rather than a
// missing one: the map is still readable and the mistake is still visible.
//
// Path.Stroke does its own dashing without going through this, because it
// consumes each run as the walk produces it and never holds them all. This is
// for a caller that wants the geometry -- one that measures it, or hands it to
// something else -- and it allocates one slice per run to give it away safely.
func Dash(pts []Point, pattern []float32, phase float32) [][]Point {
	pat, ok := dashPattern(pattern)
	if !ok {
		if len(pts) == 0 {
			return nil
		}
		return [][]Point{append([]Point(nil), pts...)}
	}
	var out [][]Point
	dashWalk(pts, pat, phase, func(run []Point) {
		out = append(out, append([]Point(nil), run...))
	})
	return out
}

// dashPattern validates a dash array and returns the pattern to walk, or false
// if the line should be drawn solid.
//
// An odd-length pattern is doubled here rather than handled by arithmetic in
// the walk. Doubling is what makes the on and off roles swap on the second
// cycle -- index parity is the only thing that decides which is which -- and
// it costs one small allocation per call against a modulo in the inner loop
// and a paragraph explaining it.
func dashPattern(dash []float32) ([]float32, bool) {
	if len(dash) == 0 {
		return nil, false
	}
	total := float32(0)
	for _, d := range dash {
		if !(d >= 0) || math.IsInf(float64(d), 0) { // the ! also rejects NaN
			return nil, false
		}
		total += d
	}
	if total <= 0 {
		return nil, false
	}
	if len(dash)%2 == 0 {
		return dash, true
	}
	return append(append(make([]float32, 0, 2*len(dash)), dash...), dash...), true
}

// dashWalk walks pts by arc length and calls emit for each on run.
//
// The slice passed to emit is REUSED between calls. Every consumer here either
// strokes it immediately or copies it, and the alternative -- a fresh slice per
// dash -- is one allocation per dash on a path that may have hundreds of dashes
// on every one of hundreds of footpaths.
//
// pat must be even-length, non-negative and sum to more than zero; dashPattern
// is what guarantees that, and the loop below relies on all three. In
// particular a zero-length entry is allowed and must not spin: it emits a
// zero-length run, which under round caps is a dot, and advances to the next
// entry, so a full cycle always consumes the pattern's positive total.
func dashWalk(pts []Point, pat []float32, phase float32, emit func([]Point)) {
	if len(pts) == 0 {
		return
	}

	total := float32(0)
	for _, d := range pat {
		total += d
	}

	// Reduce the phase into one cycle. A caller animating a dash advances the
	// phase without bound, and Go's Mod keeps the sign of its argument, so a
	// negative phase needs the extra turn.
	if math.IsNaN(float64(phase)) || math.IsInf(float64(phase), 0) {
		phase = 0
	}
	phase = float32(math.Mod(float64(phase), float64(total)))
	if phase < 0 {
		phase += total
	}

	idx, rem := 0, pat[0]
	for phase > 0 {
		if phase < rem {
			rem -= phase
			break
		}
		phase -= rem
		idx = (idx + 1) % len(pat)
		rem = pat[idx]
	}
	on := idx%2 == 0

	var run []Point
	if on {
		run = append(run, pts[0])
	}
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		dx, dy := b.X-a.X, b.Y-a.Y
		length := float32(math.Hypot(float64(dx), float64(dy)))
		if length == 0 {
			// No arc length, so no pattern advances across it and there is
			// nothing to interpolate along. Skipping it also keeps a repeated
			// point out of the emitted run.
			continue
		}
		walked := float32(0)
		for length-walked > rem {
			walked += rem
			t := walked / length
			q := Point{a.X + dx*t, a.Y + dy*t}
			if on {
				run = appendPoint(run, q)
				emit(run)
				run = run[:0]
			} else {
				run = appendPoint(run[:0], q)
			}
			on = !on
			idx = (idx + 1) % len(pat)
			rem = pat[idx]
		}
		rem -= length - walked
		if on {
			run = appendPoint(run, b)
		}
	}
	if on && len(run) > 0 {
		emit(run)
	}
}

// appendPoint appends pt unless it repeats the last point.
//
// A pattern boundary that falls exactly on a vertex would otherwise put that
// vertex in the run twice -- once as the end of the segment before it and once
// as the split point on the segment after -- which is harmless to draw and
// wrong to hand to a caller of Dash who is measuring what came back.
func appendPoint(run []Point, pt Point) []Point {
	if n := len(run); n > 0 && run[n-1] == pt {
		return run
	}
	return append(run, pt)
}
