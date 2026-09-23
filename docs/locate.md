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

- The wanted-ID sets must themselves be compact. A **sorted slice of int64 with a binary
  search** is built once and never mutated, which is exactly the access pattern.
  **Measured** (`TestIDSetRetainedSize`, `BenchmarkIDSet`, a million ids each added three
  times, a count deliberately not a power of two): the sorted slice retains **8 bytes an
  id**, a `map[int64]struct{}` **29**, and the slice builds about twelve times faster. The
  estimate here was originally ~50 bytes for the map; Go's map is more compact than that
  now, so the ratio is 3.6x rather than 6x — the conclusion is unchanged, the margin is
  smaller than the plan assumed.

  The duplication and the odd count in that test are not decoration. The first version used
  a power-of-two count with no duplicates, which is the one arrangement in which append's
  last growth step lands exactly on the length — it read 8 bytes from a set that was really
  holding an array sized by the number of `add` calls, and `freeze` used `slices.Clip`,
  which lowers the cap without releasing anything. On the realistic workload that was
  **25 bytes an id against the map's 29**, which would have left the design's whole
  rationale standing on a 1.2x margin. `freeze` copies into an exactly-sized array now.
  The sorted slice pays a second time, and this was not in the original plan: because a
  binary search yields a *position*, the coordinates can live in an array parallel to the
  frozen set instead of a `map[int64]Point`. That is 24 bytes a node all-in, against a map's
  per-entry overhead on top of the same 24.
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
| 4 | **The three passes** | ✅ committed — `boundary/osm`. Relation → member ways with their coordinates and node ids. Measured against a real Denmark extract: the memory gate passes by two orders of magnitude, and the *data* premise does not hold there. See below. |
| 5 | **Ring assembly** | ✅ committed — `boundary/osm.Assemble`. Ways joined on node id into closed rings, outers counterclockwise and inners clockwise per RFC 7946, unclosed chains reported with the node ids of the gap. Measured: Sydney closes 471 of 522 outlines, Hornsby's 46 ways into one ring of 450 points. |
| 6 | **The derived file** | ✅ committed — `boundary.WriteDerived`/`ReadDerived`, magic, version, a length-prefixed provenance header, delta-coded varints at OSM's own resolution. `boundary.Polygons` pairs holes to outlines; `osm.Areas` converts and reports. Measured: Sydney's 471 areas are **0.41 MB**, and containment answers are identical on either side of the file. |
| 7 | **`osmbase boundaries --osm`** | ✅ committed — a URL to fetch or a `.osm.pbf` to read, `--levels`, `--region`, `--keep-extract`. A fetched extract is deleted; one the user pointed at is left alone. The ODbL obligation is stated before the file exists, and travels inside it. |
| 8 | **Wire into `locate`** | ✅ committed — `boundary.Source` loads every derived file in the store, ranks the containment stack onto Locality/Macrohood/Neighbourhood, and `CreditFor` reports which licence an answer owes. `locate` opens the tile store only for the levels boundaries do not cover. |

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

Stage two is **through part 4**. Parts 1 to 4 are committed: the shared protobuf reader
knows the signed encodings, `osmpbf` reads framing, blocks and elements, and `boundary/osm`
runs the three passes to produce each boundary's member ways with their coordinates and
node ids. Parts 5 to 8 are untouched.

To resume: read this file, then the "Place names" section of `architecture.md`, then start
at part 5 of the table. The three reviews are worth repeating per sub-part — across parts 1
to 4 they have found a path traversal, an architectural violation, a concurrency crash, a
required-features check nobody owned, two separate memory amplifications of four to five
orders of magnitude, and a memory measurement taken on a workload that could not show the
defect it was written to rule out.

### The gate: run, and passed by two orders of magnitude

Measured 2026-09-23 against `denmark-latest.osm.pbf` (472 MB, dated 2026-09-21), with
`TestMeasureARealExtract`:

| | all levels | levels 8–10 |
|---|---|---|
| boundaries kept | 145 | 23 |
| distinct ways wanted | 5,725 (2,512 held) | 892 (72 held) |
| **distinct nodes** | **554,214** | 4,399 |
| coordinates held | 8.5 MB | 0.1 MB |
| id sets | 4.3 MB | — |
| live heap after three passes | 4.5 MB | 0.3 MB |
| wall time, three passes | 25 s | 25 s |

The plan expected "a few million" distinct nodes and warned to stop and reconsider above
that. The real figure is **half a million**, and the whole working set is under 15 MB. The
in-memory design holds with two orders of magnitude to spare, and the on-disk intermediate
the plan named as the fallback is not needed.

Two things the numbers say that the plan did not ask about. Three passes over 472 MB take
**25 seconds**, all of it decode rather than IO, so a country is a coffee-break operation
and a planet file is not. And **56% of the ways these relations name are not in the
extract** (5,725 wanted, 2,512 held) — Denmark's own boundaries reference ways across its
land borders and around its coast, so "a way beyond the cut" is the common case, not the
edge case. Part 5 must treat an incomplete ring as normal input.

### The premise holds where the mapping does — Denmark is thin, Sydney is not

The gate was about memory. The thing that actually threatens stage two is the data.

`TestSurveyARealExtract` against the same file, counting what is there rather than what the
pipeline keeps:

```
relation boundary=administrative, admin_level:  "7" 108   "8" 21   "4" 9   "2" 6   "6" 3   "10" 1   "9" 1
way      boundary=administrative, admin_level:  "7"  68   "2" 56   "8" 14  "4" 7   "6" 5
relation place:   island 53  islet 44  archipelago 27  hamlet 23  square 22  locality 12  suburb 6
way      place:   islet 1030  square 695  hamlet 437  quarter 68  neighbourhood 56
node     place:   hamlet 6626  village 1480  locality 970  neighbourhood 927  quarter 542  suburb 415
```

This document says, as the justification for the whole of stage two: *"levels 8 to 10 are
suburb, and no other available dataset reaches them."* **In Denmark there are 21 relations
at level 8 and one each at 9 and 10.** Level 7 is the kommune. So the OSM pipeline, in
Denmark, lands at **municipality** — which is precisely the granularity geoBoundaries was
rejected for in the table below, on the grounds that it "reaches neither of their goals".

Where Denmark's suburbs actually live is as **points**: 415 `place=suburb` nodes, 927
`place=neighbourhood`, 542 `place=quarter`. Containment is impossible against a point, so
that is stage one's nearest-feature answer, which already exists.

So the survey was run against Sydney (BBBike, 54 MB) before concluding anything, and it
answers the question:

```
relation boundary=administrative, admin_level:  "9" 522   "6" 58   "2" 1   "4" 1
relation place:   suburb 509  municipality 28  borough 8
node     place:   suburb 483  locality 108  neighbourhood 58
```

**522 administrative relations at level 9, and they are the suburbs.** The whole pipeline
end to end on that extract: 522 boundaries at levels 8–10, every one of them with geometry,
61,382 distinct nodes, 0.9 MB of coordinates, 0.7 MB of live heap, three seconds. Hornsby
comes out at level 9 with 46 member ways and 496 points.

**The design is right and Denmark is thin.** Australia maps suburbs as administrative
boundaries; Denmark maps them as points and stops its admin hierarchy at the kommune. That
is a property of each country's mapping community, not of this pipeline, and no dataset
choice fixes it — geoBoundaries and Overture inherit the same OSM tagging.

What follows:

1. **Ship it, and let the data answer per country.** Where the relations exist, containment
   works; where they do not, `locate` already falls back to stage one's nearest point. The
   fallback is the honest answer for Denmark rather than a failure, and the `Source` field
   on a `Match` already tells a caller which it got.
2. **Every way arrives open.** Not one of Hornsby's 46 ways is closed on its own, and the
   same is true across the extract. Ring assembly is not an optimisation for tidiness; it
   is the only thing that turns this output into a polygon. Part 5 is load-bearing, and is
   now built: Sydney closes 471 of its 522 outlines completely, the other 51 being the
   suburbs the bounding box cuts through. Denmark closes 3 of 23 at these levels, which is
   the same thinness finding and not an assembly failure — the closure rate measures how
   complete an extract is, so `TestAssembleARealExtract` reports it and asserts only what
   holds whatever the data contains.
3. **`place=*` polygons remain a candidate, and are cheap.** 509 `place=suburb` relations in
   Sydney, 6 in Denmark. Same three passes, different predicate. Worth adding only if a
   country turns up that has them and lacks the admin relations — Denmark is not that
   country either.
4. **Closed ways are unread, and that costs nothing — checked.** A boundary may be a single
   closed way rather than a relation, and this pipeline reads only relations. Denmark tags
   175 ways `boundary=administrative`; **174 of them (99.4%) are already members of an admin
   relation**, so the tag is redundant on a segment the pipeline reads anyway. The single
   orphan carries `admin_level=""`, which is not a level, so it would be refused even by a
   pipeline that did read ways. Sydney tags none at all. `TestSurveyARealExtract` reports
   this for any extract, because it is a property of a country's tagging habits rather than
   a fact about the format.

### The rule the reviews keep finding

Four times now, in four different places: **a limit that bounds what a decoder returns
instead of what it allocates**. The string table (part 2), every packed element run (part
3), then in part 4 both the geometry `assemble` expands — a 1,495-byte file produced 1.6 GB
— and the string table entries a relation's names and roles copy out. The fix is always the
same: check before the append, in the loop doing the appending.

Two corollaries, both learned the hard way here:

- **A test that asserts an error came back does not test this.** It passes before and after
  the fix. Measure the allocation instead; `TestACraftedRunCannotAllocateInProportionToItself`
  and `TestTheGeometryHandedBackIsBounded` are the shapes to copy.
- **A limit on counts is not a limit on bytes.** `MaxStrings` bounded how many entries a
  table held and nothing bounded how long one was, so a 450-byte file retained 1.26 GB.
  Both axes need a bound.

### What the derived file carries, and why

The header is **length-prefixed**, and that is the escape hatch rather than a detail. The
version is compared for equality in both directions, so refusing an older file means every
file on disk becomes a fresh country download — which makes "add a field in part 7" an
expensive sentence. With the prefix, a later osmbase can append to the header and this
reader steps over what it does not know; the version stays reserved for changes to the
*geometry* encoding, which cannot be skipped.

It carries **provenance** because of the licence, not for tidiness. A derived file is a
Derivative Database under the ODbL — it *is* the map data in another shape — so share-alike
attaches to the file, and a format carrying only geometry hands somebody a Derivative
Database with no notice inside it. `osm.Attribution` is the single spelling of the credit
and `osm.Provenance` fills it in, so the OSM path cannot produce a file that has forgotten
it. Source and levels ride along for a second reason: two stores' files are otherwise
indistinguishable, and without the levels a reader cannot tell "this file has no suburbs"
from "this file was not asked for suburbs".

### How a level is chosen, and why it is not a table

An `admin_level` is a number whose meaning differs by country — seven is the municipality
in Denmark and nine is the suburb in Australia — so a table mapping numbers to level names
would be asserting one country's scheme over every other. That is why `Kind` stays the
number.

The rule is the nesting instead: of the administrative areas containing a point, the
**outermost is its locality and the innermost its neighbourhood**, with anything between a
macrohood. One area is a locality and nothing else, because a place with one administrative
name at this range has one, and inventing two from it would be a claim the data does not
make.

Measured on the two extracts this was built against, that gives Denmark's kommune as a
locality, and Sydney's council area as a locality with its suburb as a neighbourhood — which
is what a person would have said. Worth knowing when reading an answer: which slot a name
lands in depends on how many levels the extract actually *closed*, so a bounding-box cut
that severs the council area leaves the suburb reported as a locality. The `Kind` is how a
reader tells.

### What stage two did not settle

- **The antimeridian is detected, not handled.** `osm.Areas` counts a ring that crosses the
  seam and leaves it out rather than handing it to `inRing`, which treats longitude as
  linear. Neither extract produces one. Handling them — unwrapping, or splitting at the seam
  — is a design decision for whoever first needs Fiji.
- **The decompression-bomb guard is still in three places**: `pmtiles.decompress`,
  `slice.decompress` and `osmpbf.unzlib`, with three error vocabularies. Carried since part
  3 and deferred each time because it touches two working packages. It has already diverged
  once.
- **The header's sort order is still unread**, and `osmpbf.ErrStop` still has no consumer
  outside its own test. A file marked sorted type-then-id would let the three passes stop
  early rather than read to the end.
- **`Header` does not carry the bounding box**, which would let a caller reject an extract
  that does not cover the area before reading an element.
- **`osmbasetest.Extract` emits one block per element kind**, so a multi-block file, a block
  mixing kinds, a non-default granularity and a coordinate origin are all unexercised.
- **Nothing handles a signal.** An interrupted run's temporary files are swept by the *next*
  run rather than by the interrupted one; a `signal.NotifyContext` would do it properly and
  would also let a long download be cancelled cleanly.
