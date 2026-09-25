package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/wisborg/osmbase/boundary"
	osmlocate "github.com/wisborg/osmbase/locate"
	"github.com/wisborg/osmbase/slice"
)

// placeFlags are --place and --place-level, which render and fetch share.
type placeFlags struct {
	name  string
	level string
}

func (p *placeFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&p.name, "place", "", "a country or region by name, instead of a coordinate: \"Denmark\", \"DK\", "+
		"or \"Luxembourg, Belgium\" to say which of several; read from the Natural Earth outlines in the store, "+
		"so nothing is sent anywhere to look it up")
	fs.StringVar(&p.level, "place-level", "", "narrow --place to one level: country or region")
}

// resolvePlace turns --place into the one area it means.
//
// Resolved from the store's own boundary files and nothing else. A geocoding
// service would answer more names, and it would also be told where the user
// is going -- the same leak this program refuses for tiles, arrived at by the
// back door.
//
// Exactly one match is used. More than one is refused with every candidate
// listed and a way to say which, because the first match is a guess and a
// map of the wrong Luxembourg looks exactly like a map of the right one. A
// name that only begins a place's name is offered and never taken.
func (p placeFlags) resolve(root string) (boundary.Candidate, error) {
	var levels []osmlocate.Level
	switch p.level {
	case "":
	case "country":
		levels = []osmlocate.Level{osmlocate.Country}
	case "region":
		levels = []osmlocate.Level{osmlocate.Region}
	default:
		return boundary.Candidate{}, usageErrorf("--place-level %q is not one this command knows; it has country and region", p.level)
	}
	if strings.TrimSpace(p.name) == "" {
		return boundary.Candidate{}, usageErrorf("--place was given no name")
	}

	src := boundary.Open(root, "")
	if !src.Covers(osmlocate.Country) && !src.Covers(osmlocate.Region) {
		return boundary.Candidate{}, fmt.Errorf("--place looks names up in the Natural Earth outlines, and %s has none; run \"osmbase boundaries --store %s\" first, once -- it downloads them",
			root, root)
	}
	exact, near := src.Find(p.name, levels...)

	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) > 1:
		return boundary.Candidate{}, usageErrorf("%q is %d places:\n%s\nsay which, for example %s",
			p.name, len(exact), listCandidates(exact), howToNarrow(p, exact))
	case len(near) > 0:
		return boundary.Candidate{}, usageErrorf("no country or region is named %q exactly; did you mean\n%s\nNothing was drawn: the %s you mean may be one these outlines do not hold -- they stop at regions, and a town or suburb needs --bbox or --lat/--lon for now.",
			p.name, listCandidates(near), p.name)
	}
	return boundary.Candidate{}, usageErrorf("no country or region in %s is named %q; the names are Natural Earth's, in English, or ISO codes like DK and DNK. A town or suburb needs --bbox or --lat/--lon for now.",
		root, p.name)
}

// listCandidates prints candidates one to a line, at most a screenful.
func listCandidates(cs []boundary.Candidate) string {
	const most = 12
	var b strings.Builder
	for i, c := range cs {
		if i == most {
			fmt.Fprintf(&b, "  ... and %d more\n", len(cs)-most)
			break
		}
		fmt.Fprintf(&b, "  %s\n", safeForTerminal(c.Describe()))
	}
	return strings.TrimRight(b.String(), "\n")
}

// howToNarrow suggests a flag that would pick out one of the candidates,
// built from the candidates themselves so the suggestion is one that works.
func howToNarrow(p placeFlags, cs []boundary.Candidate) string {
	var hints []string
	name, _, _ := strings.Cut(p.name, ",")
	name = strings.TrimSpace(name)
	for _, c := range cs {
		if c.In != "" {
			hints = append(hints, fmt.Sprintf("--place %q", name+", "+c.In))
			break
		}
	}
	levels := map[osmlocate.Level]int{}
	for _, c := range cs {
		levels[c.Level]++
	}
	if levels[osmlocate.Country] == 1 && p.level == "" {
		hints = append(hints, "--place-level country")
	}
	if len(hints) == 0 {
		return "--bbox with the corners of the one you mean"
	}
	return strings.Join(hints, " or ")
}

// placeBounds is a candidate's extent in the form fetch and render take.
func placeBounds(c boundary.Candidate) slice.Bounds {
	return slice.Bounds{West: c.Extent.West, South: c.Extent.South, East: c.Extent.East, North: c.Extent.North}
}

// placeReport is the line a command prints about the place it resolved,
// saying what of it is shown.
func placeReport(c boundary.Candidate) string {
	s := safeForTerminal(c.Describe())
	if c.Shown < c.Parts {
		s += fmt.Sprintf(", %d of its %d parts -- the rest are too far from the main one to share a map with it", c.Shown, c.Parts)
	}
	return s
}
