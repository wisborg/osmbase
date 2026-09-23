package osm

import "testing"

// A relation naming the same way twice must not cost the outline. The
// duplicate is a loop out along the way and back, enclosing nothing, so it is
// not a ring -- but neither is it a reason for the ring it sits inside to
// stop closing.
//
// Diagnosis: at the junction the duplicate creates, next() prefers "the fork
// that closes the loop" and the duplicate IS that fork. The closure it offers
// is degenerate -- three points, which closed() then refuses -- so the chain
// takes the fork, fails to close on it, and walks on poisoned. Preferring a
// fork only when the closure it makes is a real ring (len(c.points)+len(nodes)-1 > 3)
// is the condition that is missing.
//
// Not observed in the Sydney or Denmark extracts: 0 relations name a way
// twice at any level. It is one mapper edit away, and it is silent -- the
// suburb simply stops having a polygon.
func TestAWayNamedTwiceDoesNotCostTheOutline(t *testing.T) {
	for _, tc := range []struct {
		name string
		ways []Way
	}{
		{"the duplicate next to its twin", []Way{
			seg(10, "", 1, 2), seg(10, "", 1, 2), seg(11, "", 2, 3), seg(12, "", 3, 1)}},
		{"the duplicate listed last", []Way{
			seg(10, "", 1, 2), seg(11, "", 2, 3), seg(12, "", 3, 1), seg(10, "", 1, 2)}},
		// This one already passes: the way is long enough that the loop it
		// makes with its twin has four points and so reads as a ring, which
		// is why the bug is specific to short members.
		{"a longer way duplicated", []Way{
			seg(10, "", 1, 2, 3, 4), seg(11, "", 4, 5, 6, 1), seg(10, "", 1, 2, 3, 4)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Assemble(tc.ways)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if len(got.Outer) != 1 {
				t.Errorf("got %d rings, want the triangle still closed: %+v", len(got.Outer), got)
			}
		})
	}
}
