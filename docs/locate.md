# Locating a coordinate

The design for answering "where is this point" — country, region, locality, suburb,
street — from data held on the user's own disk.

This is the detailed plan for the section
[Place names: containment, with an honest fallback](architecture.md#place-names-containment-with-an-honest-fallback)
in the architecture. That section states the shape and the reasoning; this one states what
gets built, in what order, and what each stage can and cannot answer. Where the two differ
the architecture wins and this file is wrong.

## What the tile data can and cannot say

Worth stating first, because it is the constraint the whole design is arranged around and
it is not obvious from the schema's names.

The only NAMED features in the basemap tiles are:

| layer | geometry | named |
|---|---|---|
| `places` | points | yes — `country`, `region`, `locality`, `macrohood`, `neighbourhood` |
| `roads` | lines | yes — 432 of 466 features in one central London tile |
| `water` | lines and polygons | yes |
| `pois` | points | yes |
| `boundaries` | **lines** | **no** — `kind` and `admin_level` only |
| `landuse` | **polygons** | **no** — `kind` only |

So the two layers that describe AREAS carry no names, and every named feature is a point or
a line. A question of the form "which suburb contains this coordinate" therefore cannot be
answered from these tiles at all. The nearest named point can be found, and that is a
different claim: a `locality` point is a label anchor near the middle of a town, so nearest
answers "which town is closest", not "which town is this".

Measured coverage of the levels, from sampling real tiles:

- `neighbourhood` and `macrohood` appear from zoom 13, in cities. Rural areas have none.
- `locality` is dense and reliable — 68 features in one zoom-7 tile over New South Wales.
- `region` exists in the schema and its coverage is **uneven**: present over the United
  States at zoom 4 (`kind_detail: state`), absent from the tiles sampled over Denmark and
  over New South Wales.
- `country` appears from zoom 3.
- Postcode does not exist in this schema at any zoom.

Names carry translations — `name:da`, `name:ja`, and so on — so a language preference costs
almost nothing and should be in the API from the start.

## Two sources, and the field that keeps them apart

Containment from boundary polygons; nearest from the tile points above. `Place.Source` says
which answered, and the architecture is emphatic about why: a contained result may be
rendered as `Newtown`, a nearest result must be rendered as `near Newtown` or declined. A
consumer that ignores the field will put a confident suburb name on a video of somebody who
was in the next suburb.

Resolution order is containment at the deepest wanted level, then shallower levels, then
tile place points within a distance cap, then **nothing**. Beyond the cap there is no
answer rather than a distant one.

## Boundary data: two stages, one interface

Behind a `BoundarySource` interface, so the second stage is an addition rather than a
rewrite.

### Stage one — Natural Earth, for country and region

[Public domain](https://www.naturalearthdata.com/about/terms-of-use/): *"No permission is
needed to use Natural Earth. Crediting the authors is unnecessary."* No attribution
obligation, no share-alike, no restriction on redistribution, and therefore no `NOTICE`
entry and nothing propagating to a consumer's output. It is the only candidate with zero
obligations.

It is coarse, around 1:10m, and that is the right trade for these two levels. Country and
region are exactly where nearest-point is worst — a country label anchor can be hundreds of
kilometres away, and region is missing outright for Denmark and New South Wales — while a
coarse outline answers "which country contains this point" correctly everywhere except
within about a kilometre of a border. It is also small enough that fetching it is barely an
event.

Stage one therefore turns two broken levels into two correct ones, for a day's work and no
licence obligations at all.

### Stage two — OSM administrative relations, for locality and suburb

This is the one that reaches the granularity the feature exists for. `boundary=administrative`
relations carry an `admin_level` and a name, levels 8 to 10 are suburb, and no other
available dataset reaches them (see the rejected alternatives below).

The architecture already specifies the pipeline: three streaming passes over a country
extract, because PBF is ordered nodes, then ways, then relations, and containment cannot be
decided before the geometry exists. Collect the wanted relations and their member way IDs;
collect those ways' node IDs; resolve coordinates, assemble and close the rings, simplify,
and write a compact derived file. The extract is transient and deleted afterwards.

ODbL, which is the licence regime this project already lives in. The distinction that
matters is the one `NOTICE` already draws: returning a place name is a **Produced Work** and
needs attribution only, while the derived boundary file is a **Derivative Database** and
share-alike attaches to it. That is an obligation on the data and not on the Apache-2.0
code, and it is acceptable — but it is a real difference from stage one, which has none.

### Rejected

| Source | Licence | Why not |
|---|---|---|
| **geoBoundaries** | CC BY 4.0 | Does not reach suburb. ADM0 and ADM1 near-complete, ADM2 substantial, ADM3 and deeper patchy — so it lands at municipality, which is between the two stages above and reaches neither of their goals. It would add an attribution obligation and a third data pipeline for a level nobody asked for. |
| **GADM** | non-commercial, no redistribution | Prohibits both redistribution and commercial use without prior permission. Unusable in a public Apache-2.0 repository whatever its quality. |
| **Overture divisions** | ODbL plus CC-BY components | Reaches the right levels, and is distributed as GeoParquet. A Parquet reader is a substantial dependency or a substantial implementation, against a project that writes its own decoders precisely to avoid both. Revisit if the OSM pipeline proves harder than it looks. |

## How the data arrives

Fetched on demand into the same store as the tiles, so nothing is bundled in the repository
or the binary, and the store, its manifest and its eviction all apply unchanged. A consumer
that never geocodes carries none of it.

Boundaries are not tiles, so they do not become a `slice` source: they get their own
directory under the store root with its own manifest. Keeping `slice`'s tile model intact
matters more than the tidiness of one directory, and a boundary file has no cell zoom, no
pyramid and no per-cell eviction to share.

The privacy account is better than the tile path's, and the architecture already makes the
argument: fetching a country extract reveals which country interests you and nothing finer,
which is strictly coarser than the per-cell requests the tile acquisition already makes.
The one-time cost buys better privacy, not worse.

## API sketch

```go
package locate

type Level uint8   // Country, Region, Locality, Macrohood, Neighbourhood, Street
type Source uint8  // Contained, Near

type Match struct {
    Level     Level
    Name      string   // in the preferred language where the data has one
    Kind      string   // the schema's own kind_detail: "city", "state", "suburb"
    Source    Source
    DistanceM float64  // 0 when Contained
}

type Place struct {
    Lat, Lon float64
    Matches  []Match
}

func At(ctx context.Context, src TileSource, lat, lon float64, opts Options) (Place, error)
func AtEach(ctx context.Context, src TileSource, pts []Coord, opts Options) ([]Place, error)
```

`AtEach` is not a convenience wrapper around `At`. A route is thousands of coordinates over
a handful of tiles, and the whole cost of a lookup is reading and decoding a tile; grouping
the coordinates by tile before reading anything turns thousands of reads into a handful.
The single-point form is the special case, not the other way round.

Each level is read at the zoom where its data lives — neighbourhood at 13 and deeper,
locality in the middle, country at 3 to 5 — so one lookup touches several tiles. They cache
like any others.

`Options` carries the language preference, the per-level distance caps, and the
`BoundarySource` — nil meaning nearest-feature only, which is a legitimate configuration and
the one that needs no download.

## What does not live here

The CLI here stays minimal and stdlib-only: `osmbase locate --lat --lon`, text and JSON.

Route summarising and reading coordinates out of FIT, GPX or KML files belong in a separate
program. Two reasons, and they point the same way. FIT decoding needs `fitactivity`, and
rich output formats need `github.com/wisborg/output`, which brings `uax29` and
`go-runewidth` with it — four modules for a library that has two and whose whole
acquisition story is built on not having more. And the split is the right one anyway:
osmbase is about the data, not about what a particular consumer wants done with it.

The summarising algorithm is worth recording even though it is built elsewhere, because it
constrains what this API must support. It is a CHANGE LOG rather than a sample: walk the
route, look up periodically, and emit a row only when a name at some level differs from the
previous row. A four-hour activity becomes a handful of rows naming the places it passed
through. Because lookups here are local and cheap, the aggressive point-thinning a metered
geocoding API forces is unnecessary — the filter can be generous and the change detection
does the work.
