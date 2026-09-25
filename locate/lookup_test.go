package locate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/locate"
)

// A caller may hold boundary data and no tiles -- a store built by
// "osmbase boundaries --osm" and nothing else. That is a legitimate
// configuration for the levels those boundaries cover, and a level that needs
// tiles has to say so rather than panic on a nil source.
func TestANilTileSourceIsAnErrorAndNotAPanic(t *testing.T) {
	_, err := locate.AtEach(context.Background(), nil, []locate.Coord{{Lat: 55, Lon: 9}}, locate.Options{Levels: []locate.Level{locate.Street}})
	if err == nil {
		t.Fatal("a level needing tiles was answered with no tile source")
	}
	if !strings.Contains(err.Error(), "tiles") {
		t.Errorf("error %q does not say what is missing", err)
	}
}

// And with no level needing tiles, a nil source is fine: nothing reaches it.
func TestANilTileSourceIsFineWhenBoundariesCoverEverything(t *testing.T) {
	b := coveringSource{level: locate.Country, name: "Denmark"}
	places, err := locate.AtEach(context.Background(), nil, []locate.Coord{{Lat: 55, Lon: 9}},
		locate.Options{Levels: []locate.Level{locate.Country}, Boundaries: b})
	if err != nil {
		t.Fatalf("locate.AtEach: %v", err)
	}
	if len(places) != 1 || len(places[0].Matches) != 1 || places[0].Matches[0].Name != "Denmark" {
		t.Errorf("got %+v, want the contained answer", places)
	}
}

// A boundary-only store and a route that leaves the boundaries: the points
// they hold no data for are unanswered, as a point with no named feature near
// it would be. Failing the whole route over them would lose every answer the
// boundaries DID give.
func TestANilTileSourceLeavesAPlaceTheBoundariesDoNotKnowUnanswered(t *testing.T) {
	b := knowsOnlyNorth{}
	places, err := locate.AtEach(context.Background(), nil,
		[]locate.Coord{{Lat: 55, Lon: 9}, {Lat: -33, Lon: 151}},
		locate.Options{Levels: []locate.Level{locate.Locality}, Boundaries: b})
	if err != nil {
		t.Fatalf("locate.AtEach: %v", err)
	}
	if m, ok := places[0].Match(locate.Locality); !ok || m.Name != "North" {
		t.Errorf("the point inside the boundaries got %+v, want North", places[0].Matches)
	}
	if len(places[1].Matches) != 0 {
		t.Errorf("the point the boundaries know nothing about got %+v, want nothing", places[1].Matches)
	}
}

// knowsOnlyNorth covers locality and holds data only north of the equator.
type knowsOnlyNorth struct{}

func (knowsOnlyNorth) Covers(l locate.Level) bool { return l == locate.Locality }

func (knowsOnlyNorth) Contains(l locate.Level, lat, lon float64) (string, string, string, locate.Containment) {
	if l != locate.Locality || lat < 0 {
		return "", "", "", locate.NoData
	}
	return "North", "7", "", locate.Inside
}

// coveringSource answers one level by containment and nothing else.
type coveringSource struct {
	level locate.Level
	name  string
}

func (c coveringSource) Covers(l locate.Level) bool { return l == c.level }

func (c coveringSource) Contains(l locate.Level, lat, lon float64) (string, string, string, locate.Containment) {
	if l != c.level {
		return "", "", "", locate.NoData
	}
	return c.name, "country", "", locate.Inside
}

// countingLevels answers from a table and counts how it was asked.
type countingLevels struct {
	levels, single int
}

func (c *countingLevels) Covers(l locate.Level) bool {
	return l == locate.Locality || l == locate.Neighbourhood
}

func (c *countingLevels) Contains(l locate.Level, lat, lon float64) (string, string, string, locate.Containment) {
	c.single++
	return l.String() + " name", "9", "", locate.Inside
}

func (c *countingLevels) ContainsLevels(lat, lon float64, levels []locate.Level) []locate.Answer {
	c.levels++
	out := make([]locate.Answer, len(levels))
	for i, l := range levels {
		out[i] = locate.Answer{Name: l.String() + " name", Kind: "9", Containment: locate.Inside}
	}
	return out
}

// A source that can answer every level at once is asked once per point, and
// never level by level -- which is the whole saving -- and the answers land
// at the levels they were for.
func TestAtEachAsksALevelsSourceOncePerPoint(t *testing.T) {
	src := &countingLevels{}
	pts := []locate.Coord{{Lat: 1, Lon: 1}, {Lat: 2, Lon: 2}, {Lat: 3, Lon: 3}}
	places, err := locate.AtEach(context.Background(), nil, pts, locate.Options{
		Boundaries: src, Levels: []locate.Level{locate.Locality, locate.Neighbourhood},
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.levels != len(pts) || src.single != 0 {
		t.Errorf("ContainsLevels %d times and Contains %d, want %d and 0", src.levels, src.single, len(pts))
	}
	for _, p := range places {
		for _, l := range []locate.Level{locate.Locality, locate.Neighbourhood} {
			if m, ok := p.Match(l); !ok || m.Name != l.String()+" name" {
				t.Errorf("%s = %+v, want its own answer", l, m)
			}
		}
	}
}
