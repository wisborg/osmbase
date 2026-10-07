package render_test

import (
	"context"
	"testing"

	"github.com/wisborg/osmbase/mvt"
	"github.com/wisborg/osmbase/osmbasetest"
	"github.com/wisborg/osmbase/render"
)

// stackStyle is land, then green landuse, then built-up landuse: the order
// that drew a suburb over the bushland inside it.
func stackStyle() render.Style {
	all := func(layer string, kinds []string, role render.Role) render.Rule {
		return render.Rule{Layer: layer, Kinds: kinds, MinZoom: 0, MaxZoom: render.MaxRuleZoom, Paint: render.Paint{Role: role, Fill: true}}
	}
	return render.Style{Name: "stack", Schema: "test", Rules: []render.Rule{
		all("earth", nil, render.RoleLand),
		all("landuse", []string{"wood", "park"}, render.RoleGreen),
		all("landuse", []string{"residential"}, render.RoleBuilt),
	}}
}

// area is a landuse polygon from (x0, y0) to (x1, y1) in tile units, with
// an id and, when rank is not 0, a sort_rank.
func area(id uint64, kind string, rank int64, x0, y0, x1, y1 int32) osmbasetest.FeatureSpec {
	tags := []osmbasetest.Tag{{Key: "kind", Value: mvt.StringValue(kind)}}
	if rank != 0 {
		tags = append(tags, osmbasetest.Tag{Key: "sort_rank", Value: mvt.IntValue(rank)})
	}
	return osmbasetest.FeatureSpec{
		ID: id, HasID: true, Type: mvt.GeomPolygon, Tags: tags,
		Geometry: mvt.Geometry{Polygons: []mvt.Polygon{{
			Exterior: mvt.Ring{{X: x0, Y: y0}, {X: x1, Y: y0}, {X: x1, Y: y1}, {X: x0, Y: y1}},
		}}},
	}
}

func landuse(fs ...osmbasetest.FeatureSpec) osmbasetest.LayerSpec {
	return osmbasetest.LayerSpec{Name: "landuse", Features: fs}
}

func drawStackStyle(t *testing.T, src render.TileSource, v render.View) *render.Result {
	t.Helper()
	r, err := render.New(src, render.Options{Style: stackStyle(), Palette: testPalette})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Render(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Where two areas overlap at one sort_rank, the smaller is drawn on top,
// whichever rule draws it: a wood inside a suburb is wood, and a suburb
// inside a park is suburb. A smaller one at a lower sort_rank stays under.
func TestStack_TheSmallerAreaIsOnTop(t *testing.T) {
	src := newSource()
	// Tile 10/0/0: a wood inside a residential area. Tile 10/1/0: a
	// residential area inside a park. Tile 10/2/0: a small wood at rank 1
	// under a large residential area at rank 2.
	src.put(t, 10, 0, 0, wholeTile("earth", ""), landuse(area(1, "residential", 0, 0, 0, 4096, 4096), area(2, "wood", 0, 1024, 1024, 3072, 3072)))
	src.put(t, 10, 1, 0, wholeTile("earth", ""), landuse(area(3, "residential", 0, 1024, 1024, 3072, 3072), area(4, "park", 0, 0, 0, 4096, 4096)))
	src.put(t, 10, 2, 0, wholeTile("earth", ""), landuse(area(5, "wood", 1, 1024, 1024, 3072, 3072), area(6, "residential", 2, 0, 0, 4096, 4096)))
	img := drawStackStyle(t, src, tileView(t, 10, 0, 0, 2, 0, 256)).Image

	at(t, img, 128, 128, testPalette.Green, "a wood inside a suburb")
	at(t, img, 20, 20, testPalette.Built, "the suburb round the wood")
	at(t, img, 256+128, 128, testPalette.Built, "a suburb inside a park")
	at(t, img, 256+20, 20, testPalette.Green, "the park round the suburb")
	at(t, img, 512+128, 128, testPalette.Built, "a smaller wood at a lower sort_rank, under a larger area at a higher one")
}

// A polygon is ranked by all of it in view, not by the piece one tile
// holds, so the order cannot change from one tile to the next. Here the
// wood is cut in two by a tile edge, and each half is smaller than the
// residential area it overlaps in the left tile, but the whole of it is
// larger: the residential area is on top, in both tiles alike.
func TestStack_APolygonIsRankedByAllOfItInView(t *testing.T) {
	src := newSource()
	// The wood: the right 60% of tile 0 and the left 60% of tile 1, 0.6 of a
	// tile in each, 1.2 in all. The residential area: the top three quarters
	// of tile 0, 0.75 of a tile -- larger than either half of the wood and
	// smaller than all of it -- overlapping the wood's left half.
	src.put(t, 10, 0, 0, wholeTile("earth", ""), landuse(area(7, "wood", 0, 1638, 0, 4096, 4096), area(8, "residential", 0, 0, 0, 4096, 3072)))
	src.put(t, 10, 1, 0, wholeTile("earth", ""), landuse(area(7, "wood", 0, 0, 0, 2458, 4096)))
	img := drawStackStyle(t, src, tileView(t, 10, 0, 0, 1, 0, 256)).Image

	at(t, img, 200, 100, testPalette.Built, "the residential area over the larger wood's left half")
	at(t, img, 200, 230, testPalette.Green, "the wood where nothing overlaps it")
	at(t, img, 256+60, 100, testPalette.Green, "the wood's right half")
}

// Two halves of one stacked polygon meeting at a tile edge are filled in one
// pass, so the edge is not a hairline: the pixels either side of it are the
// polygon's colour exactly. Trap T1, through the stack.
func TestStack_NoSeamWhereAPolygonCrossesATileEdge(t *testing.T) {
	src := newSource()
	src.put(t, 10, 0, 0, wholeTile("earth", ""), landuse(area(9, "residential", 0, 0, 0, 4096, 4096), area(10, "wood", 0, 2048, 1024, 4096, 3072)))
	src.put(t, 10, 1, 0, wholeTile("earth", ""), landuse(area(9, "residential", 0, 0, 0, 4096, 4096), area(10, "wood", 0, 0, 1024, 2048, 3072)))
	img := drawStackStyle(t, src, tileView(t, 10, 0, 0, 1, 0, 256)).Image
	for _, x := range []int{254, 255, 256, 257} {
		at(t, img, x, 128, testPalette.Green, "the wood either side of the tile edge")
	}
}

// Measured once over a region, the areas rank a polygon the same in every
// view of it, however little of it the view holds. Without them, a view of
// tile 0 alone sees only the wood's left half -- smaller than the
// residential area, so on top -- while a view of both tiles sees all of it
// and puts the residential area on top: two neighbouring views of one
// ground disagree, and a flyover drawn from fixed tiles shows the seam.
func TestStack_MeasuredAreasRankAlikeInEveryView(t *testing.T) {
	src := newSource()
	src.put(t, 10, 0, 0, wholeTile("earth", ""), landuse(area(7, "wood", 0, 1638, 0, 4096, 4096), area(8, "residential", 0, 0, 0, 4096, 3072)))
	src.put(t, 10, 1, 0, wholeTile("earth", ""), landuse(area(7, "wood", 0, 0, 0, 2458, 4096)))
	both := tileView(t, 10, 0, 0, 1, 0, 256)

	alone := drawStackStyle(t, src, tileView(t, 10, 0, 0, 0, 0, 256)).Image
	at(t, alone, 200, 100, testPalette.Green, "unmeasured, tile 0 alone ranks the wood by its half")

	o := render.Options{Style: stackStyle(), Palette: testPalette}
	areas, err := render.MeasureAreas(context.Background(), src, o, both.Bounds, 10)
	if err != nil {
		t.Fatal(err)
	}
	o.Areas = areas
	r, err := render.New(src, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []render.View{tileView(t, 10, 0, 0, 0, 0, 256), both} {
		res, err := r.Render(context.Background(), v)
		if err != nil {
			t.Fatal(err)
		}
		at(t, res.Image, 200, 100, testPalette.Built, "the residential area over the larger wood, whatever the view holds of it")
		at(t, res.Image, 200, 230, testPalette.Green, "the wood where nothing overlaps it")
	}
}
