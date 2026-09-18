package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/wisborg/osmbase/render"
)

// labelNames are the shorthands --labels accepts, and what each one means as
// a shift in zoom levels.
//
// Names rather than numbers because the numbers are meaningless on their own:
// "-1" does not say which direction is quieter, and a reader of somebody
// else's command line should not have to work it out. The numbers are still
// accepted -- see labelStyle -- for the case the names do not cover, which is
// anybody who wants a shift larger than these.
//
// Ordered from fewest labels to most, which is the order the help prints them
// in and the order somebody reaches for them in.
var labelNames = []struct {
	name  string
	shift int
	why   string
}{
	{"places", 0, "no street or water names at all"},
	{"extra-sparse", -2, "street and water names two zoom levels later than usual"},
	{"sparse", -1, "one zoom level later"},
	{"normal", 0, "the default"},
	{"dense", 1, "one zoom level earlier"},
	{"extra-dense", 2, "two zoom levels earlier"},
}

// labelHelp is the --labels flag's description, built from the table so the
// two cannot disagree.
func labelHelp() string {
	var b strings.Builder
	b.WriteString("how much of the map is named: ")
	for i, n := range labelNames {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q (%s)", n.name, n.why)
	}
	b.WriteString(". A signed number is also accepted and is what the names are shorthand for: " +
		"negative moves street and water names later, positive earlier. Place names are never shifted -- " +
		"there are few of them, they are what a reader is looking for, and thinning them is what makes a " +
		"busy map unreadable rather than what fixes it")
	return b.String()
}

// labelStyle applies a --labels value to a style.
func labelStyle(s render.Style, value string) (render.Style, error) {
	if value == "places" {
		return s.WithoutLineLabels(), nil
	}
	for _, n := range labelNames {
		if n.name == value {
			return s.ShiftLineLabels(n.shift), nil
		}
	}
	// A number, for the shift the names do not cover. Parsed after the names
	// so that a name can never be shadowed by one.
	if z, err := strconv.Atoi(value); err == nil {
		return s.ShiftLineLabels(z), nil
	}
	var names []string
	for _, n := range labelNames {
		names = append(names, n.name)
	}
	return s, usageErrorf("--labels %q is not one this command knows; it has %s, or a signed number",
		value, strings.Join(names, ", "))
}
