package protobuf

import (
	"math"
	"testing"
)

// TestUnzigzag32 and TestUnzigzag64 pin the two widths at
// the edges of their ranges, which is the only place a shift or a cast at the
// wrong width shows.
//
// Every expected value is derived from the encoding rule and not from the
// code. Zigzag interleaves the signs onto the naturals, so an encoded 2n is +n
// and an encoded 2n-1 is -n; the largest even value therefore decodes to the
// largest positive number of the width, and the largest odd value to the most
// negative.
//
// They are worth having at this level rather than only through a decoder: the
// 32-bit path is bounded by the vector tile's geometry decoding long before
// its extremes are reachable, so nothing above this can reach the values that
// matter.
func TestUnzigzag32(t *testing.T) {
	cases := []struct {
		in   uint32
		want int32
	}{
		{0, 0},
		{1, -1},
		{2, 1},
		{3, -2},
		{4, 2},
		{0xfffffffd, -2147483647},
		{0xfffffffe, math.MaxInt32}, // 2n with n = 2147483647
		{0xffffffff, math.MinInt32}, // 2n-1 with n = 2147483648
	}
	for _, c := range cases {
		if got := Unzigzag32(c.in); got != c.want {
			t.Errorf("Unzigzag32(%#x) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestUnzigzag64(t *testing.T) {
	cases := []struct {
		in   uint64
		want int64
	}{
		{0, 0},
		{1, -1},
		{2, 1},
		{3, -2},
		{4, 2},
		{0xfffffffffffffffd, -9223372036854775807},
		{0xfffffffffffffffe, math.MaxInt64},
		{0xffffffffffffffff, math.MinInt64},
	}
	for _, c := range cases {
		if got := Unzigzag64(c.in); got != c.want {
			t.Errorf("Unzigzag64(%#x) = %d, want %d", c.in, got, c.want)
		}
	}
}
