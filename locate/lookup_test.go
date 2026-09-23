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

// coveringSource answers one level by containment and nothing else.
type coveringSource struct {
	level locate.Level
	name  string
}

func (c coveringSource) Covers(l locate.Level) bool { return l == c.level }

func (c coveringSource) Contains(l locate.Level, lat, lon float64) (string, string, string, bool) {
	if l != c.level {
		return "", "", "", false
	}
	return c.name, "country", "", true
}
