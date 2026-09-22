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

### Stage one — Natural Earth, for country, region and water — **built**

[Public domain](https://www.naturalearthdata.com/about/terms-of-use/): *"No permission is
needed to use Natural Earth. Crediting the authors is unnecessary."* No attribution
obligation, no share-alike, no restriction on redistribution, and therefore no `NOTICE`
entry and nothing propagating to a consumer's output. It is the only candidate with zero
obligations.

**On which resolution.** The default is the finest set, 1:10m, and that was
not the first choice. 50m was picked on the grounds that the 10m state file is
forty megabytes of JSON and would be slow — a guess about size standing in for
a look at the contents. The 50m state file carries 294 subdivisions across
NINE countries and none at all for Denmark, Germany, France, Norway or the
United Kingdom; a region level that answers for nine countries is not a region
level. The 10m file carries 4,596 across 253. The cost it was rejected for did
not survive measurement either: both files parse in about 1.1 seconds, once per
process, and only for the levels actually asked about. 50m remains available
and is a fair choice for country alone, which it covers in full at a fifth of
the size.

Even at its finest the data is coarse next to OpenStreetMap — 1:10 million is a
world atlas, not a survey — and that is the right trade for these two levels.
Country and region are exactly where nearest-point is worst: a country label
anchor can be hundreds of kilometres away, and region is missing outright from
the tiles for Denmark and New South Wales. An atlas outline answers "which
country contains this point" correctly everywhere except within about a
kilometre of a border, which is a far better failure than the one it replaces.

**Water is a level of its own**, answered from Natural Earth's marine polygons — 306 named
oceans, seas, straits, gulfs and bays in the 10m set. It is independent of the land
hierarchy rather than an alternative to it: a point can be inside a country's outline and
inside a named strait at once, and a flight between mainland Australia and Tasmania is over
Bass Strait while still being described in terms of Australia.

It is what makes a flight describable at all. A trans-Pacific track has no country beneath
it for most of its length, and containment correctly reports nothing — honest, and nearly
useless. "South Pacific Ocean" is the answer that was wanted. There is no tile fallback for
it: the tiles name the river beside you, which is a different question from which sea you
are over.

Two properties of the marine file differ from the admin ones and are handled rather than
discovered later. Its lowercase `name` is the SPECIFIC form and `name_en` the generic —
"South Pacific Ocean" against "Pacific Ocean", for 71 of 306 features — which is the
opposite of the admin files, where the English key is the one to trust; and it shouts the
largest features, because `INDIAN OCEAN` is how an ocean is labelled on a map and not how a
sentence names one. Marine areas also NEST, so the smallest containing area wins: taking the
first reported a trans-Tasman flight as being over the Pacific.

Stage one therefore turns two broken levels into two correct ones, adds a third that had no
answer at all, and carries no licence obligations.

### Stage two — OSM administrative relations, for locality and suburb — **not built**

This is the one that reaches the granularity the feature exists for.
`boundary=administrative` relations carry an `admin_level` and a name, levels 8 to 10 are
suburb, and no other available dataset reaches them (see the rejected alternatives below).

ODbL, which is the licence regime this project already lives in. The distinction that
matters is the one `NOTICE` already draws: returning a place name is a **Produced Work** and
needs attribution only, while the derived boundary file is a **Derivative Database** and
share-alike attaches to it. That is an obligation on the data and not on the Apache-2.0
code, and it is acceptable — but it is a real difference from stage one, which has none, and
it has to be recorded in `NOTICE` and surfaced by the command that builds the file.

#### The pipeline, and why it is three passes

A PBF file is ordered nodes, then ways, then relations, and containment cannot be decided
before the geometry exists. So the file is read three times:

1. **Relations** → keep those with `boundary=administrative`, a wanted `admin_level` and a
   name. Record each one's member way IDs and roles (`outer`/`inner`).
2. **Ways** → for the IDs pass 1 wanted, record their node ID lists.
3. **Nodes** → for the IDs pass 2 wanted, record coordinates.

Then, in memory: assemble each relation's ways into closed rings, orient outers and inners,
simplify, and write the derived file.

#### The memory question, which is the part to get right

Peak resident size is dominated by the node-to-coordinate map, and the naive version — every
node in the extract — is hundreds of megabytes for a country and does not scale to a large
one.

**It does not need to be every node.** Pass 2 yields exactly the node IDs that boundary ways
reference, and administrative boundaries are a tiny fraction of an extract: the overwhelming
majority of nodes are buildings, roads and addresses that no boundary way touches. Pass 3
therefore keeps only those, which turns "every node in Denmark" into "the nodes on Denmark's
admin boundaries".

Two consequences to design around rather than discover:

- The wanted-ID sets must themselves be compact. A `map[int64]struct{}` costs about 50 bytes
  an entry; a **sorted slice of int64 with a binary search** costs 8 and is built once and
  never mutated, which is exactly the access pattern. Worth measuring before choosing.
- Pass 2 must record way→nodes for wanted ways only, and pass 3 coordinates for wanted nodes
  only. Neither pass may accumulate anything proportional to the file.

The number to check early, on a real Denmark extract: how many distinct nodes the admin
boundaries reference. If it is a few million the sorted-slice approach is a few tens of
megabytes and the design holds. **If it is far larger, stop and reconsider** — an
on-disk intermediate would be the next option, and it is much more work.

#### Sub-parts, in order, each reviewable on its own

Each lands as its own commit with its own tests, and is reviewed before the next begins.
None of them requires the one after it to be useful.

| # | Part | Done when |
|---|---|---|
| 1 | **Shared protobuf reader** | ✅ committed — `internal/protobuf`, extracted from `mvt` |
| 2 | **PBF block reader** | Blob/BlobHeader framing, zlib inflation, and `PrimitiveBlock` string tables decode from a synthetic fixture. No OSM semantics yet. |
| 3 | **Element decoding** | Dense nodes (delta-encoded), ways, and relations come back as Go structs, with tags resolved against the string table. Fuzzed, as `mvt` is. |
| 4 | **The three passes** | Given a reader, produce relation → rings of coordinates. This is where the memory question is answered, with the measurement recorded. |
| 5 | **Ring assembly** | Ways joined end to end into closed rings, outers and inners oriented, unclosed rings reported rather than silently dropped. |
| 6 | **The derived file** | A compact format `boundary` can read, plus the writer. Versioned, because it is on somebody's disk. |
| 7 | **`osmbase boundaries --osm`** | Fetch an extract through `acquire`, run the pipeline, delete the extract, record the ODbL obligation. |
| 8 | **Wire into `locate`** | `Locality` and `Neighbourhood` answered by containment when the file is present. |

Parts 2 and 3 need no network and no real extract: synthetic fixtures in the style of
`osmbasetest` are enough, and are better, because a real file cannot express a malformed one.
Part 4 is the first that wants a real Denmark extract, and is where to stop and measure.

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

`Options.Levels` restricts the lookup, and the caller is the only one who can decide what is
appropriate. A run wants every level; a flight wants country, region and water and nothing
finer, because a street 250 m below an aircraft is a true answer to a question nobody asked.
Fewer levels also means fewer tiles read, since each is read at its own zoom. The command
exposes it as `--levels`.

Altitude is deliberately absent. A flight track's coordinates are just coordinates, and
whatever carries them — a FIT or GPX file — is where altitude lives; a consumer that wants
to colour a drawn path by height reads it there. It is not this package's to know.

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

## Where this got to

Stage one is **built and merged**: country, region and water answered by containment from
Natural Earth, everything below by nearest-feature from the tiles, with `Place.Source` and
`Match.DistanceM` saying which and how far.

Stage two is **through part 3**. Parts 1 to 3 are committed on `decode-osm-elements`:
the protobuf reader is shared between the formats and knows the signed encodings,
`osmpbf` reads the file framing, blob decompression, header features and primitive
blocks, and nodes, ways and relations come back as Go structs with their tags
resolved. Parts 4 to 8 are untouched.

To resume: read this file, then the "Place names" section of `architecture.md`, then start
at part 4 of the table. The three reviews are worth repeating per sub-part — on stage one
they found a path traversal, an architectural violation, a concurrency crash and seven
provably-deletable decisions; on part 2 a 90,000× memory amplification and a
required-features check nobody owned; on part 3 the same amplification class one layer in,
a granularity narrowed before it was bounded, and an aliasing trap with no way for a caller
to escape it. None of it was visible from the code reading correctly.

### The rule the reviews keep finding

Three times now the same defect has appeared in a new place: **a limit that bounds what a
decoder returns instead of what it allocates**. The string table in part 2, then every
packed element run in part 3. The fix each time is the same — check the count *before* the
append, in the loop doing the appending, not after the run — and it now lives in
`protobuf.Packed`, which every repeated numeric field goes through.

Two things follow for part 4, which builds the first structure whose size is not bounded by
one block:

- The node-to-coordinate map is the obvious next instance. It is sized by the *file*, and
  the memory analysis above assumes only boundary-referenced nodes are kept. Whatever
  enforces that has to refuse before it allocates, not report afterwards.
- A test that asserts an error came back does not test this. It passes before and after the
  fix; `TestACraftedRunCannotAllocateInProportionToItself` measures the allocation instead,
  and is the shape to copy.

### Carried into part 4

- **The decompression-bomb guard is still in three places** — `pmtiles.decompress`,
  `slice.decompress` and `osmpbf.unzlib` — with three error vocabularies and three copies
  of the reasoning. Carried from part 3, where it was deferred again because it touches two
  working packages. It has already diverged once. It should be one function taking a
  decompressor constructor, an optional reusable buffer and a package name.
- **The header's sort order is still unread.** `Header.OptionalFeatures` has no consumer,
  and a file marked sorted type-then-id lets the three passes stop early rather than read to
  the end. Part 4 is where that pays off.
- **`Header` does not carry the bounding box.** Readable now that signed varints exist; it
  would let a caller reject an extract that does not cover the area before reading an
  element.

One gap is knowingly left open: `unzlib` grows its buffer to the declared size only when one
was declared, and that decision is invisible in the output, so no test pins it. A
`runtime.MemStats` delta is too flaky to be worth it; an `AllocedBytesPerOp` benchmark is the
honest form if it ever matters.
