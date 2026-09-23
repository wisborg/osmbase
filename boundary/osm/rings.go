package osm

import (
	"fmt"
	"slices"
)

// Ring is a closed loop of coordinates.
//
// The first point is NOT repeated at the end. A ring of n corners holds n
// points and the closing edge is implied, which is what boundary's own
// point-in-ring test already assumes -- it walks the edges with a wraparound
// rather than looking for a repeated vertex. Anything writing GeoJSON has to
// repeat the first point itself, because that format requires it.
type Ring []Point

// Rings is what a boundary's ways assemble into.
type Rings struct {
	// Outer and Inner are closed rings: the areas enclosed, and the holes in
	// them. Outer rings wind counterclockwise and inner rings clockwise, as
	// RFC 7946 specifies for GeoJSON.
	Outer []Ring
	Inner []Ring

	// Open are the chains that did not close.
	//
	// A field rather than an error because an unclosed chain is ORDINARY
	// input, not a failure: 56% of the ways Denmark's boundary relations name
	// lie outside a Denmark extract, because its boundaries run along
	// borders and coastlines that continue past the cut. A caller drawing a
	// map may want to draw them; a caller testing containment must not.
	// Either way the decision is theirs, and dropping them silently would
	// take it away.
	Open []Chain
}

// Chain is a run of ways that joined to each other but not into a loop.
type Chain struct {
	Role   string
	Points []Point

	// From and To are the node ids at the two loose ends, which is what says
	// WHERE the outline is broken -- and usually that the way carrying it was
	// outside the extract.
	From, To int64

	// Ways is how many ways joined before the chain ran out.
	Ways int
}

// Assemble joins a boundary's ways end to end into rings.
//
// This is not tidying up. Not one of the 46 ways making up Hornsby's outline
// is closed on its own, and the same holds across a Sydney extract: a
// relation's members are fragments, cut at every junction with a neighbour,
// arriving in no particular order and in no particular direction. Assembly is
// the only thing that turns them into a polygon.
//
// Ways are joined on NODE ID rather than on coordinates. Two distinct nodes
// at identical coordinates are ordinary where an extract has been cut, and
// joining those would weld together outlines that merely touch.
func Assemble(ways []Way) (Rings, error) {
	var out Rings
	for _, w := range ways {
		if len(w.Nodes) != len(w.Points) {
			return Rings{}, fmt.Errorf("osm: way %d has %d node ids and %d points; they must correspond",
				w.ID, len(w.Nodes), len(w.Points))
		}
	}

	// An empty role means outer. That is the convention on boundary
	// relations and it is common -- an editor writing an outline does not
	// always spell it out -- so treating it as its own kind would leave most
	// of a country's outlines in neither bucket.
	outer, inner := split(ways)

	out.Outer, out.Open = assembleRole(outer, "outer", out.Open)
	out.Inner, out.Open = assembleRole(inner, "inner", out.Open)

	for i, r := range out.Outer {
		out.Outer[i] = orient(r, counterclockwise)
	}
	for i, r := range out.Inner {
		out.Inner[i] = orient(r, clockwise)
	}
	return out, nil
}

func split(ways []Way) (outer, inner []Way) {
	for _, w := range ways {
		if w.Role == "inner" {
			inner = append(inner, w)
			continue
		}
		outer = append(outer, w)
	}
	return outer, inner
}

// assembleRole joins one role's ways, returning the rings that closed and
// appending the chains that did not.
func assembleRole(ways []Way, role string, open []Chain) ([]Ring, []Chain) {
	// Every endpoint, to the ways that have it. A boundary node joins exactly
	// two ways, so this is a short list per entry -- but it is a list rather
	// than a single index because a file may say otherwise, and finding out
	// by overwriting would be silent.
	ends := map[int64][]int{}
	for i, w := range ways {
		if len(w.Nodes) < 2 {
			continue
		}
		ends[w.Nodes[0]] = append(ends[w.Nodes[0]], i)
		ends[w.Nodes[len(w.Nodes)-1]] = append(ends[w.Nodes[len(w.Nodes)-1]], i)
	}

	var rings []Ring
	used := make([]bool, len(ways))

	for i := range ways {
		if used[i] {
			continue
		}
		used[i] = true

		if len(ways[i].Nodes) < 2 {
			// A way of one node joins to nothing, and a way of none has no
			// ends to report -- which Read can legitimately produce, because
			// the format permits a way with no references at all. Reported
			// rather than dropped: it is malformed data, and discarding it
			// silently would leave an outline short with nothing to say why.
			open = append(open, degenerate(ways[i], role))
			continue
		}

		c := start(ways[i])
		c.extend(ways, ends, used)
		if !c.closed() {
			// The run may have started in the middle of the outline, so the
			// other direction is still open. Reversing and extending again
			// costs one more walk and finds it.
			before := c.ways
			c.reverse()
			c.extend(ways, ends, used)
			if c.ways == before {
				// Nothing was behind it either, so put it back the way the
				// file had it. Handing back a reversed copy of an untouched
				// way would be a difference a caller can see and nothing can
				// explain.
				c.reverse()
			}
		}

		if c.closed() {
			rings = append(rings, c.ring())
			continue
		}
		open = append(open, c.chain(role))
	}
	return rings, open
}

// degenerate reports a way too short to join to anything.
func degenerate(w Way, role string) Chain {
	c := Chain{Role: role, Points: slices.Clone(w.Points), Ways: 1}
	if len(w.Nodes) > 0 {
		c.From, c.To = w.Nodes[0], w.Nodes[len(w.Nodes)-1]
	}
	return c
}

// chain is a run of ways joined end to end, under construction.
type chain struct {
	nodes  []int64
	points []Point
	ways   int
}

func start(w Way) *chain {
	return &chain{nodes: slices.Clone(w.Nodes), points: slices.Clone(w.Points), ways: 1}
}

func (c *chain) tail() int64 { return c.nodes[len(c.nodes)-1] }
func (c *chain) head() int64 { return c.nodes[0] }

// closed reports whether the chain has come back to where it started.
//
// Three nodes minimum, because two points joined to themselves enclose no
// area: a way that runs out and back along itself would otherwise read as a
// ring, and a degenerate one is worse than a reported gap.
func (c *chain) closed() bool {
	return len(c.nodes) > 3 && c.head() == c.tail()
}

func (c *chain) reverse() {
	slices.Reverse(c.nodes)
	slices.Reverse(c.points)
}

// extend appends ways onto the tail for as long as one fits.
func (c *chain) extend(ways []Way, ends map[int64][]int, used []bool) {
	for {
		i, reversed, ok := next(c.tail(), ways, ends, used)
		if !ok {
			return
		}
		used[i] = true
		c.append(ways[i], reversed)
		c.ways++
		if c.closed() {
			return
		}
	}
}

// next finds an unused way with an endpoint at node, and says whether it has
// to be reversed to run away from it.
func next(node int64, ways []Way, ends map[int64][]int, used []bool) (i int, reversed, ok bool) {
	for _, j := range ends[node] {
		if used[j] {
			continue
		}
		switch {
		case ways[j].Nodes[0] == node:
			return j, false, true
		case ways[j].Nodes[len(ways[j].Nodes)-1] == node:
			return j, true, true
		}
	}
	return 0, false, false
}

// append adds a way to the tail, dropping its first point because the chain
// already ends on that node.
func (c *chain) append(w Way, reversed bool) {
	nodes, points := w.Nodes, w.Points
	if reversed {
		nodes = slices.Clone(nodes)
		points = slices.Clone(points)
		slices.Reverse(nodes)
		slices.Reverse(points)
	}
	c.nodes = append(c.nodes, nodes[1:]...)
	c.points = append(c.points, points[1:]...)
}

// ring returns the closed loop, dropping the repeated closing vertex.
func (c *chain) ring() Ring {
	return Ring(slices.Clone(c.points[:len(c.points)-1]))
}

func (c *chain) chain(role string) Chain {
	return Chain{
		Role:   role,
		Points: slices.Clone(c.points),
		From:   c.head(),
		To:     c.tail(),
		Ways:   c.ways,
	}
}

// Winding directions, as RFC 7946 uses them.
const (
	counterclockwise = 1
	clockwise        = -1
)

// orient returns the ring wound the way asked for, reversing it if it is not.
func orient(r Ring, want int) Ring {
	if r.winding() == want {
		return r
	}
	slices.Reverse(r)
	return r
}

// winding is +1 for counterclockwise, -1 for clockwise, 0 for a ring that
// encloses nothing.
//
// The shoelace sum over longitude and latitude as though they were plane
// coordinates. That is not the area of anything -- a degree of longitude is
// not a degree of latitude anywhere but the equator -- but the SIGN is what
// is wanted, and scaling one axis cannot change it. An administrative
// boundary that spanned the antimeridian would still break this, and none
// does: OpenStreetMap cuts them there.
func (r Ring) winding() int {
	if len(r) < 3 {
		return 0
	}
	var sum float64
	for i := range r {
		j := (i + 1) % len(r)
		sum += r[i].Lon*r[j].Lat - r[j].Lon*r[i].Lat
	}
	switch {
	case sum > 0:
		return counterclockwise
	case sum < 0:
		return clockwise
	}
	return 0
}
