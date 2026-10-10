package dem

import (
	"slices"
	"strings"
	"testing"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
)

// square is a polygon feature from (x0, y0) to (x1, y1) in tile units,
// naming source.
func square(source string, x0, y0, x1, y1 int32) osmbasetest.FeatureSpec {
	return osmbasetest.FeatureSpec{
		Type: mvt.GeomPolygon,
		Tags: []osmbasetest.Tag{{Key: CoverageTag, Value: mvt.StringValue(source)}},
		Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{
			Exterior: mvt.Ring{{X: x0, Y: y0}, {X: x1, Y: y0}, {X: x1, Y: y1}, {X: x0, Y: y1}},
		}}},
	}
}

// coverageTiles is a coverage tile at 1/0/0 -- the north-west quarter of the
// world -- covered by glo30, with a finer source in its north-west corner,
// longitude -180 to -90.
func coverageTiles(t *testing.T) tiles {
	t.Helper()
	b, err := osmbasetest.BuildTile(osmbasetest.TileSpec{Layers: []osmbasetest.LayerSpec{{
		Name: CoverageLayer, Extent: 4096,
		Features: []osmbasetest.FeatureSpec{
			square("glo30", 0, 0, 4096, 4096),
			square("fine", 0, 0, 2048, 1024),
			// A ring with a hole over longitude -90 to -45 at the
			// equator's edge of the tile, wound as the hole it is.
			{
				Type: mvt.GeomPolygon,
				Tags: []osmbasetest.Tag{{Key: CoverageTag, Value: mvt.StringValue("ring")}},
				Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{
					Exterior: mvt.Ring{{X: 1024, Y: 2048}, {X: 4096, Y: 2048}, {X: 4096, Y: 4096}, {X: 1024, Y: 4096}},
					Holes:    []mvt.Ring{{{X: 2048, Y: 3072}, {X: 2048, Y: 4000}, {X: 3072, Y: 4000}, {X: 3072, Y: 3072}}},
				}}},
			},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return tiles{{1, 0, 0}: b}
}

func TestSourcesIn_NamesWhatReachesTheView(t *testing.T) {
	cov := coverageTiles(t)
	for _, c := range []struct {
		name       string
		w, s, e, n float64
		want       string
	}{
		{"inside the fine source", -170, 80, -160, 84, "fine glo30"},
		{"outside it", -170, 10, -160, 20, "glo30"},
		{"inside a ring", -40, 10, -10, 15, "ring glo30"},
		{"in the ring's hole", -85, 10, -50, 20, "glo30"},
		{"across the hole's edge", -95, 10, -85, 15, "ring glo30"},
		{"where there is no coverage tile", 10, -20, 20, -10, ""},
	} {
		// Zoom 3: the tile is only held at 1, so this walks up.
		got, err := SourcesIn(cov, 3, c.w, c.s, c.e, c.n)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCredit_QuotesTheCopernicusNoticeAndNamesTheRest(t *testing.T) {
	attrs := []Attribution{{Source: "fine", Producer: " Survey  Office ", License: "CC BY 4.0"}, {Source: "glo30", Producer: "DLR"}}
	got := Credit([]string{"fine", "unlisted", "glo30"}, attrs)
	want := "Elevation: Mapterhorn; Survey Office (CC BY 4.0); unlisted; " + GLO30Notice
	if got != want {
		t.Errorf("Credit:\n got %q\nwant %q", got, want)
	}
	if Credit(nil, attrs) != "" || ShortCredit(nil) != "" {
		t.Error("no sources credited somebody")
	}
	if got := ShortCredit([]string{"glo30"}); got != "Elevation: © Mapterhorn, mapterhorn.com/attribution" {
		t.Errorf("ShortCredit %q", got)
	}
}

func TestReadAttributions(t *testing.T) {
	a, err := ReadAttributions(strings.NewReader(`[{"source":"dk","producer":"Klimadatastyrelsen","license":"CC BY 4.0","resolution":0.4}]`))
	if err != nil || len(a) != 1 || a[0].Source != "dk" || a[0].Producer != "Klimadatastyrelsen" {
		t.Fatalf("%+v, %v", a, err)
	}
}

// Sources are named by id, with the worldwide fallback last.
func TestSortSources(t *testing.T) {
	got := SortSources([]string{"glo30", "zz", "aa", "mm"})
	want := []string{"aa", "mm", "zz", "glo30"}
	if !slices.Equal(got, want) {
		t.Errorf("SortSources = %v, want %v", got, want)
	}
}

// Two datasets of one producer under one licence are credited once.
func TestCreditNamesAProducerOnce(t *testing.T) {
	attrs := []Attribution{
		{Source: "usgs-a", Producer: "U.S. Geological Survey", License: "Public Domain"},
		{Source: "usgs-b", Producer: "U.S. Geological Survey", License: "Public Domain"},
		{Source: "ign", Producer: "IGN", License: "Licence Ouverte"},
	}
	got := Credit([]string{"ign", "usgs-a", "usgs-b"}, attrs)
	if n := strings.Count(got, "U.S. Geological Survey"); n != 1 {
		t.Errorf("credit %q names the survey %d times, want once", got, n)
	}
	if !strings.Contains(got, "IGN (Licence Ouverte)") {
		t.Errorf("credit %q lost a producer", got)
	}
}
