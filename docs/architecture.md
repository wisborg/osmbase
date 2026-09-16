# osmbase architecture

A Go library that draws a map for a geographic rectangle from OpenStreetMap data held
on the local disk, and answers "what is this place called" from the same data. No API
key, no account, and no network access at the moment a map is drawn.

This is the design the library is being built to. It carries the reasoning, including
the alternatives that were rejected and why, because most of the decisions here look
arbitrary until you know what the other option cost.

It exists because [fitdash][fitdash] renders a route over third-party raster imagery,
and that costs a user an API key, sends the area of their activity to a company, and
produces a map whose colours were chosen by someone else. The third of those is not the
smallest: fitdash dims that imagery 65% because it is busy and mid-toned and fights the
route line, which is a correction applied after the damage.

[fitdash]: https://github.com/wisborg/fitdash

## What this library is not

**It does not know what an activity is.** There is no track, no sample, no timestamp
anywhere in its vocabulary. A caller hands it degrees and pixels and receives an
`*image.RGBA`. That is the whole contract, and it is what makes this a repository rather
than a package inside fitdash.

**It does not draw text.** It renders geometry and returns place names as *data*. Labels
belong to the consumer, in the consumer's font, at the consumer's size, with the
consumer's collision rules. fitdash already owns a face cache, a theme, a text fitter and
a panel that knows how large its box is; a library drawing its own labels would draw them
in a font fitdash did not choose and at a size fitdash could not control.

That decision is what makes the dependency budget below reachable, but it was not made
for that reason and it would stand on its own.

**The default store directory is application-neutral** (`<UserCacheDir>/osmbase`), so two
programs on one machine share one slice. Download Sydney once and both fitdash and
videofx render from it. That sharing is the strongest single argument that this is not a
package inside either of them.

## Packages

| Package | Contents |
|---|---|
| `osmbase` | `View`, `Bounds`, `Result`, `Renderer`, `Style`, `Palette`, `Place`, errors |
| `osmbase/pmtiles` | PMTiles v3 archive reader. Ours. See "Zero third-party modules" |
| `osmbase/mvt` | Mapbox Vector Tile decoder. Ours |
| `osmbase/mercator` | Web Mercator and the tile grid: degrees to a tile, a tile's local integers back to degrees. Not in `mvt`, which deliberately does not project |
| `osmbase/osmpbf` | Streaming OpenStreetMap PBF reader. Ours |
| `osmbase/raster` | Path construction, stroking, dashing, filling on `x/image/vector` |
| `osmbase/slice` | The on-disk store: cells, manifests, coverage, eviction |
| `osmbase/boundary` | Admin polygon derivation, the compact on-disk format, containment |
| `osmbase/acquire` | The **only** package that opens a socket |
| `cmd/osmbase` | Standalone CLI: `inspect`, `tile`, `geojson` today; `fetch`, `boundaries`, `render`, `place` to come |
| `osmbasetest` | Synthetic archives, tiles and slices, mirroring `fitactivity/fittest` |

The network lives in exactly one package, and that package is named after what it does.
A reader wondering whether this library can reach the internet at render time should be
able to answer it by reading the import list.

## The data source: vector tiles, not raw OSM

**Chosen: Protomaps PMTiles vector tiles**, read over HTTP range requests at acquisition
time, stored locally as MVT tiles, rasterized by us.

**Rejected: raw `.osm.pbf` as the rendering source.**

The reason is not effort. Raw OSM is not a basemap, it is the input to building one, and
the missing steps are each a project of their own:

- **Coastline assembly.** `natural=coastline` is a set of unclosed, directed ways; land
  and water polygons have to be built from them.
- **Multipolygon relation resolution.** Parks, lakes and forests with holes are relations,
  not ways, and resolving them is fiddly in ways that look wrong rather than fail.
- **Per-zoom generalisation.** Every polyline needs simplification proportional to the
  drawing scale, or a 10 km view strokes several hundred thousand vertices.
- **Feature selection and z-order.** Which of OSM's 900 `highway` values is a road, which
  is a path, and what draws over what.

An earlier draft of this document said coastline assembly required global context and was
therefore intractable locally. **That was an overstatement and it is corrected here.** OSM
coastline ways are directed with land on the left, so clipping them to a view and closing
against the view boundary is well defined at a 10 km scale. The argument against raw OSM
is not impossibility. It is volume and quality, and those are enough:

- **About 30 MB of tile cells against 917 MB, per country, to render one run.** Unlike the
  boundary file described later, the big extract cannot be deleted afterwards, because
  re-rendering needs the data. Either it stays on disk or every render re-derives a local
  store, which is the tile store again under another name.
- **Generalisation quality is the visible product.** Protomaps' per-zoom simplification,
  feature selection and z-ordering are years of cartographic judgement. Time buys the
  *ability* to reproduce that; it does not buy the judgement quickly, and in the meantime
  "worse" means the map the user is looking at.

Raw OSM comes back into the design, narrowly, for admin boundaries. See "Place names".

### What the tile choice costs

- **Someone else's schema.** We get Protomaps' tag selection, not OSM's. It is a good
  selection for a route basemap, and it is not ours: it changed between schema versions
  once already. The slice manifest records the schema version, and a style is written
  against a version.
- **A ceiling at zoom 15.** Public planet builds stop there. A tight highlight zoom
  overzooms. See trap T4.
- **No named administrative polygons**, which is why place containment needs a second
  source.

## Zero third-party modules

`golang.org/x/image` is the only non-standard-library import in this module, and it is
there for one package, `vector`.

This is a deliberate constraint rather than an accident of scope. The library will be
depended on by at least two public Apache-2.0 programs whose `NOTICE` files are
maintained by hand, and every module that arrives here arrives in both of them.

| Rejected | Why |
|---|---|
| `github.com/protomaps/go-pmtiles` | BSD-3 and correct, but its requires include `gocloud.dev`, the AWS, Azure and Google cloud SDKs, `caddyserver/caddy`, Prometheus, zap and `zombiezen.com/go/sqlite`. Importing it for one `Extract` call puts a web server and three cloud SDKs in fitdash's `go.sum` and `NOTICE`. |
| `github.com/paulmach/orb` | MIT, but its MVT path routes every geometry through GeoJSON, and the module pulls `go.mongodb.org/mongo-driver` for BSON. |
| `github.com/paulmach/osm` | MIT and pure Go, and it pulls `orb`, and thence the above. |
| `github.com/fogleman/gg` | MIT, and already in fitdash's tree so it would have been free *there*. It reaches `github.com/golang/freetype`, which is FTL/GPLv2 dual-licensed and whose advertising clause makes the credit mandatory rather than courteous. We need no text, so this is a second independent path to that dependency bought for nothing. |

`golang.org/x/image` is treated as materially different from a third-party module: it is
Go-team BSD-3, versioned and released with the toolchain, and already present in the
consumer's `go.mod` and `NOTICE`. Adding `vector` adds no module, no licence and no
`NOTICE` entry.

The formats we therefore implement ourselves are all frozen, small and specified:
PMTiles v3 is a 127-byte header plus varint-delta directories over Hilbert tile IDs;
MVT is a small protobuf schema; PBF is zlib-compressed blobs wrapping a string table and
delta-encoded dense nodes.

## The rasterizer, and the fill rule that shapes it

`golang.org/x/image/vector` does not document its fill rule. Reading `raster_floating.go`,
the accumulator is:

```go
acc += v
a := acc
if a < 0 { a = -a }
if a > 1 { a = 1 }
```

That is **absolute accumulated winding, clamped to one**. Not non-zero, not even-odd.
Three consequences follow, and all three are load bearing.

**1. Duplicate geometry is free in the interior, and not on the rim.** A feature appearing
in two tiles' buffers contributes winding 2, which clamps to 1 and fills identically to
winding 1 wherever the shape is more than a pixel thick. Overlapping stroke quads clamp the
same way, which is what lets a stroke be a heap of quads and discs.

On an antialiased EDGE it is different, and the correction is worth having because the
first draft of this section got it wrong. Two copies of an edge pixel at 0.4 coverage sum
to 0.8; they do not clamp to 0.4. Duplicates do not darken, they HARDEN -- the antialiasing
disappears. It is the same arithmetic as the seam property below and cannot be had one way
without the other. So deduplicating a feature that arrives in two tiles' buffers is a real
improvement to the picture rather than only an optimisation.

**2. One rasterizer per layer, fed by every tile, is structurally required.** It is not an
optimisation. At a pixel straddling a tile boundary, tile A's clipped ring contributes
0.4 coverage and tile B's contributes 0.6. Summed in one accumulator that is exactly 1.0.
Composited as two separate rasterizations it is `0.4 + 0.6*(1-0.4) = 0.76`, a permanent
24% hairline along every tile edge. See trap T1.

Both halves of this are pinned by a PAIR of tests, one asserting the full coverage and one
asserting that separate passes give exactly 0.75, so the expected value is a derivation
rather than a number somebody recorded. They run at two surface sizes, because
`x/image/vector` switches from fixed-point to floating-point arithmetic above 512 pixels
and those are two separate implementations of the accumulator. The reading above was taken
from the floating-point one; the small case is what checks the other.

**3. Absolute winding means a reversed ring still fills.** A hole whose exterior ring is
absent from the same path fills solid, and two copies of one ring arriving with opposite
winding cancel to nothing. **Ring orientation is normalised on decode** rather than
trusted: compute signed area, flip exteriors to a canonical direction and holes to the
other. See trap T13.

### Stroking, and why round joins are correct rather than tolerated

`vector` fills paths. It has no stroker, so we build one: per segment, a quad of width
*w*; at every interior vertex and every endpoint, a circle of radius *w*/2 approximated by
four cubic arcs. All wound the same direction, all into one path, clamped by the
accumulator above. Round joins and round caps, in about sixty lines.

**Round caps are not a compromise.** OSM splits a road into a new way at every attribute
change, so one visual road is dozens of ways meeting end to end. With butt caps, every one
of those joins shows a notch where the two segments fail to overlap on a bend. A round cap
of radius *w*/2 fills it exactly. A library with miter joins available would have needed
this setting anyway.

**Miter spikes are a real hazard here.** On a trail switchback at high zoom, two segments
meet at a few degrees and a miter join projects a spike many times the stroke width. Not
having miter at all removes the failure mode.

Round and miter joins are indistinguishable below roughly six output pixels of width, and
typical road strokes are one to four pixels at 1080p and three to eight at 4K. What is
genuinely lost is miter joins for a future style wanting sharp road casings, and the
clipping masks a general drawing library would provide. The second is not wanted: winding
in one path is a better mechanism than a mask, and fitdash already learned that `gg`'s
clip rasterizes a full-frame image per text draw.

Dashing, for footpaths and tracks, is a walk along arc length emitting sub-polylines.

### Off-surface geometry is correct and not free

A segment beginning far above the surface is walked scanline by scanline down to the top
edge before anything is drawn. Measured at 600 by 600: a segment starting a million pixels
up costs 12 ms, and a billion costs 4.5 seconds. The world at zoom 22 is about a billion
pixels across, so **the renderer must cull and clip before it fills**. The rasterizer does
not do it, because clipping changes geometry and belongs to whoever knows what the geometry
means.

## The cache

Two units, and conflating them is how this goes wrong.

**The storage unit is one source tile**, keyed on source build and tile coordinates and
**nothing else**. Not the view. Not the pixel size. Not the style. Re-rendering the same
route at 1080p and at 4K reads the same bytes, and changing the theme reads the same bytes.

This is the explicit fix for a defect in fitdash's existing imagery cache, which hashes the
requested pixel width and height into its key and therefore misses on every resolution
change, re-fetching imagery it already holds.

**The fetch and eviction unit is a cell**: one zoom-12 tile plus its full sub-pyramid to
the source's maximum zoom, plus the twelve shallower ancestors stored once per source and
shared between cells.

Zoom 12 is about 9.8 km times the cosine of the latitude, so 8.2 km at Sydney's. A 10 km
run touches one to four cells; a 100 km ride touches fifteen to twenty-five. Zoom 13 gives
finer eviction and less waste on a short run at four times the manifests; zoom 11 wastes
20 MB of city on a 5 km run.

**The cell zoom is recorded in the store manifest, not compiled in.** Zoom 12 is the right
default and the wrong thing to be unable to change, and recording it means a later change
re-keys the directory rather than silently orphaning every cell in it.

Per-activity bounding boxes were rejected for two reasons. Two runs from the same front
door produce two different boxes and never share a byte. And a box-keyed cache cannot
answer "do I have coverage here" without a geometric search over every entry, where a cell
grid answers it with a `stat`.

### Depth follows the extent, and a global track is the cheap case

Everything above assumes an activity a few kilometres across. A flight does not fit it at
all, and the correction is not a special case so much as a reminder that the zoom range is
an input rather than a constant.

Measured against the real planet build: below zoom 3 the schema carries no roads at all,
only coastlines, administrative boundaries, water, landcover and place names, which is
exactly what a route across an ocean wants. A mid-Pacific tile at zoom 4 is 2.1 KiB. And
the totals are small enough to change the shape of the problem.

| zooms | tiles | total, compressed |
|---|---|---|
| 0 to 4 | 341 | 8.0 MB |
| 0 to 5 | 1,365 | 19.5 MB |

**Every tile on earth at flight detail is 19.5 MB**, which is what one city-sized cell
costs at street detail. So a Melbourne-to-Toulouse track needs no bounding box, no cell
arithmetic and no corridor: hold the whole world and be done. Applying the cell model to
it would be absurd in both directions, fetching a sub-pyramid to zoom 15 for ground nobody
will ever see at that scale.

The rule that falls out is that a slice is a zoom RANGE over an area, and the range comes
from the track's own extent: a run wants zoom 12 to 15 over eight kilometres, a flight
wants zoom 0 to 5 over everything. The cell grid is the right structure for the first and
pointless for the second, so the store has to be able to hold a shallow global set as well
as deep local ones. Recording the cell zoom in the manifest is what already makes that
possible; what this adds is that the DEPTH is not fixed either.

A useful consequence for a first run: the shallow global set is small enough to be worth
fetching unconditionally. It is the whole-world overview every render can fall back to,
and at 8 MB it removes the case where a track leaves its cells and there is nothing
underneath at all.

### On disk

Cell-first, so that eviction is one directory removal and a partial fetch resumes:

```
<root>/                        default <UserCacheDir>/osmbase
  store.json                   cell zoom, schema version, format version
  <source-id>/                 hash of source URL and build id
    manifest.json              source, build, schema version, max zoom,
                               attribution string, fetched-at
    overview/<z>/<x>/<y>.mvt   shallow zooms, shared between cells
    cells/<x>_<y>/
      cell.json                complete?, bytes, fetched-at, last-used
      <z>/<x>/<y>.mvt
```

Tiles are stored **as fetched**, still compressed, exactly as the archive holds them, so
the cache stays small and decompression happens once per render rather than once per
download. Files are written to a temporary name and renamed. **`cell.json` is written
last**, so a killed fetch leaves a cell that is correctly recognised as incomplete.

### Bounds and eviction

Default budget one gibibyte. Eviction is least-recently-used **by cell**, and a render
updates that timestamp **once per render, not once per tile**.

Eviction runs at the end of an acquisition and at the start of a render, **never during
one**. Evicting a cell a running render is reading produces a hole in the map that appears
on some frames and not others, which is the worst shape a bug can take here.

Overview tiles are never evicted. They are twelve files, and they are what makes a
partially evicted area degrade instead of vanish.

### A cache miss at render time

**A miss never triggers a fetch. Ever.** Working offline at render time is the entire
product claim, and a render that silently reached the network because a cell had been
evicted would be exactly the data exfiltration this library exists to remove, occurring at
the moment the user least expected it.

In order:

1. **Walk up the pyramid.** Vector data overzooms without blurring, so a shallower tile
   drawn at a deeper scale is a real, sharp, less detailed map. Reported as overview
   coverage.
2. **No tile at any zoom: paint the gap.** The uncovered sub-rectangles are filled with the
   style's no-data hatch and returned to the caller. This is a placeholder, not a blank:
   plain background would be indistinguishable from ocean and from a crash.
3. **Nothing anywhere: return `ErrNoCoverage` and no image.** The consumer decides what
   that means for its layout.

Where the threshold between (2) and (3) sits is the **consumer's** decision. The library
measures; the caller chooses.

## Acquisition: the only network access

One command, run deliberately, by a user who typed it.

`PlanFor` reads the archive's directories and nothing else, so the byte figure shown
before any download is **exact rather than estimated**. That is a property of the format
and it is worth building the interface around: a confirmation prompt that can state the
true cost is a different thing from one that guesses.

Adjacent byte ranges are coalesced, so a cell is a few requests rather than eighty-five.

### The privacy claim, stated precisely

**Acquisition is a third-party request.** It tells the host which roughly 8 km cells
interest you. The honest claim is "no third-party requests **at render time**", and any
README, help text or summary line that drops those three words is wrong in the direction
this project cares about most. See trap T11.

What acquisition is not is per-render, per-route-shape, or per-timing. The imagery service
it replaces receives the exact bounding box of an activity on every render, together with
the times the user chose to render it. The improvements are that the granularity is coarse
and cell-aligned, that it happens once rather than per render, that it is explicit, and
that it is avoidable entirely by pointing the source at an archive obtained some other way.

### The library ships no default source

`acquire.Source` must be supplied by the caller. A library that silently defaults to
somebody's bucket is a library that puts its consumer's traffic on a host the consumer
never chose. The default belongs in the application, where a flag can name it and
documentation can explain it.

This is also the published constraint rather than a precaution. Protomaps discourage
hotlinking their builds and say the URLs may change, which is a statement about where a
default should live and not about how often it may be used.

### A file you already have is a first-class source, not an escape hatch

Every acquisition path must have a manual twin: point the source at a `.pmtiles` archive
or an `.osm.pbf` extract already on disk, obtained however the user likes, and nothing is
requested from anyone.

This is documented beside the fetch command rather than under it, because the cases it
covers are ordinary rather than exotic: a machine with no network, a mirror, a torrent, a
copy from a colleague, a regional archive built with somebody else's tool. It is also the
only answer that survives a host changing its mind, which is why it is a peer of fetching
and not a fallback from it.

The two sources degrade differently and the documentation should say so. A country extract
is under a gigabyte, so downloading one in a browser is entirely ordinary and the manual
path is complete. The planet archive is over a hundred gigabytes, so the manual path there
means a regional archive rather than clicking a link.

### What the hosts actually ask for

Neither of the hosts this design names publishes an acceptable-use policy covering
automated range requests. Geofabrik's download page states that the data may be used for
any purpose provided OpenStreetMap is acknowledged, with no rate limit given. Source
Cooperative states that hosted data is public and may be accessed by anyone, and documents
no consumer obligations at all.

Two conclusions follow, and the second matters more.

**Triggering is not the variable.** A command a human typed and a script running unattended
are the same request to a server; what a host cares about is load and shape. The acquisition
step is a subcommand rather than something a render performs, and that is good design for
the user's sake -- it is not, and must not be described as, a way of satisfying a policy.

**Identify the program.** A `User-Agent` naming osmbase, its version and a contact address
is what these hosts want, because it lets them reach a person instead of blocking a range
of addresses. The contact is `osmbase@wisborg.dk`, which exists for this. Every request
this library makes carries it, and it belongs in `acquire` where the requests are built.

## Place names: containment, with an honest fallback

Two sources, and the API says which one answered.

**Containment** comes from administrative boundary polygons. These exist in OSM as
`boundary=administrative` relations carrying an `admin_level` and a name, they are not in
any vector tile schema, and the only keyless route to them is a raw country extract.

`cmd/osmbase boundaries` makes the derived file in three streaming passes, because PBF is
ordered nodes, then ways, then relations, and containment cannot be decided before the
geometry exists: collect the wanted relations and their member way IDs; collect those
ways' node IDs; resolve coordinates, assemble and close the rings, simplify, and write.

The cost is one 917 MB download, a few minutes of disk passes, and a peak resident set
dominated by the node-to-coordinate map. **The extract is transient and deleted
afterwards.** The derived file for Australia is roughly 16,000 polygons simplified at a
tolerance far below anything a route meaningfully hugs, which is tens of megabytes raw and
about ten compressed. Smaller than a single urban tile cell.

**This makes the country-sized download an argument *for* the boundary path rather than
against it.** Fetching a country extract reveals which country interests you and nothing
finer, which is strictly coarser than the cell request the tile acquisition makes. The
one-time cost buys better privacy, not worse.

### The field that stops this shipping a lie

A `Place` carries a `Source` saying whether the point was **contained** by a named polygon
or merely **near** a named point. A contained result may be rendered as `Newtown`. A
nearest result must be rendered as `near Newtown`, or declined.

That distinction is not cosmetic. It is the difference between a statement of fact and an
inference, and it is unavailable at any price from a design that only knows the nearest
point. A consumer that ignores the field will put a confident suburb name on a video of
somebody who was in the next suburb.

Resolution order is containment at the deepest wanted admin level, then shallower levels,
then the tile schema's own place points within a distance cap, then nothing. **Beyond the
cap there is no answer**, rather than a distant one.

## Styling

`Style` lives in the library. The palette comes from the caller, as a handful of roles,
so a consumer with a theme can supply one without knowing what a landcover class is.

**Widths are in tile pixels, never output pixels.** A road is "1.8 px at 256-pixel tile
scale" and the renderer multiplies by the continuous zoom the view resolves to. A stroke
width written as an output-pixel constant is the same number of pixels on a 4K frame and a
720p frame, which is two different maps.

### This is where the 65% dim goes away

fitdash washes third-party imagery 65% toward its background because that imagery is busy,
mid-toned, and full of colours somebody else chose. It is a correction applied after the
fact.

When every ink is chosen from the consumer's own palette, separation can be guaranteed
**by construction and tested**, rather than corrected: every map ink below a contrast
threshold against the background, because the map is context; and the consumer's
foreground, accent and highlight inks each above a threshold against every map ink,
because the route, the covered prefix and the position marker have to read against it.

**That test is the real deliverable of the styling work.** The colours are tuning.

It is built, and building it changed the design in one way worth recording: there are
THREE constraints, not two, and they need TWO metrics.

The two above are luminance questions and WCAG 2's contrast ratio answers them: no map ink
above 4.5 against the background, borrowing the body-text threshold as a ceiling because
an ink as prominent as text is content rather than context; and every overlay ink at or
above 3.0 against every map ink, which is WCAG's own minimum for graphical objects, and a
route line is exactly that.

The third is that the map must still be legible AS a map -- water told from parkland, road
from built ground -- and contrast ratio cannot express it. The reason is forced rather
than incidental, for a dark palette. Clearing the overlay threshold against a dark theme's
inks pins every map ink below a luminance of 0.0514, and the widest contrast ratio
available inside that band is 2.03. So a dark basemap satisfying the first two constraints
CANNOT separate its roles by luminance and must separate them by hue -- and contrast ratio
is a function of luminance alone, so two colours of equal lightness score exactly 1.00
however different they look.

A light palette is the counterexample and the general form of the claim is false: its
dimmest overlay ink is a near-black foreground, so the algebra runs the other way and map
inks are pinned ABOVE a luminance of about 0.131, with ratios up to 5.4 available. One
threshold serves both because the dark case binds.

Role separation is therefore measured as a perceptual colour difference, with a floor of 6.
CIEDE2000 rather than the simpler CIE76, because CIE76 reads 10% to 36% high against these
palettes and every deviation is in the unsafe direction: its blind spot is the lightness
weighting at the extremes of L*, and the first two constraints force both palettes to
exactly those extremes. The formula is checked against the Sharma-Wu-Dalal reference pairs,
since forty lines of constants with no external check would be a worse bet than the metric
it replaced.

A FOURTH constraint was added after a consumer shipped a palette that passed all three and
still shouted: a ceiling on chroma. Saturation is a separate axis from both metrics above.
Contrast ratio sees only luminance, and delta-E measures how far two colours sit from each
other rather than how far either sits from neutral, so a uniformly vivid palette scores
perfectly on both. The ceiling is a coarse guard rather than an aesthetic judgement -- every
palette looked at and judged correct falls between 15.5 and 28.4, the one that read as a
diagram was 48 -- and two earlier settings of it were wrong in instructive ways, the second
because chroma at a light palette's lightness reads far quieter than the same chroma at a
dark one's, which a flat ceiling cannot see.

Both built-in palettes satisfy all four, verified against a stated reference overlay,
because a palette alone cannot pass or fail: the question is always "against what".

The negative case is not hypothetical. The first light palette this library shipped put a
white route line at 1.11 against its own land -- an invisible stroke on a rendered map that
looked entirely correct. That palette is kept as the fixture the check must reject.

## Attribution

A rendered image is a Produced Work under the ODbL. Attribution is required; share-alike
does not reach it.

**The credit string is data, not a constant.** It is written into the slice manifest at
acquisition, from the source, and surfaced through the render result. Shaping it as a
constant is how the wrong credit survives a change of source; shaping it as data means a
slice built from something else carries something else.

It is discharged in three places: in **every frame** that shows imagery, by the consumer,
from the string the library returns; in **`NOTICE`**, in a data section kept separate from
the software section, because a data licence obliges the output and a software licence
obliges the program; and **once at acquisition**, printed by the fetch command, because
that is the moment the user acquires the obligation.

## How a consumer uses it

The render entry point is `(ctx, view) -> image, error`, which is deliberately the shape
fitdash's existing `tilemap.Provider` already has. The adapter is about sixty lines and
**nothing in fitdash's panel layer changes**: the view fetching, the cross-fade, the
progress reporting, the credit drawing and the layout that closes up around a failed fetch
were all written against that interface and none of them know where an image came from.

Two things the adapter must get right:

- It must report that it did **not** reach the network, through the interface fitdash
  already uses to decide whether to print a privacy notice. A local provider that stayed
  silent would be reported as having sent the user's route to a third party, which is a
  false statement in the one line the user reads to find out whether that happened.
- It must **not** be wrapped in the existing on-disk image cache. Doing so would add a
  resolution-keyed cache above a tile cache that deliberately is not resolution-keyed,
  store megabytes of PNG for something regenerable from bytes already on disk, and apply a
  thirty-day expiry to a slice the user deliberately downloaded, after which the entry
  expires into a network fetch the local backend cannot perform. See trap T2.

The third-party imagery backend survives untouched as a peer. The two never share code
below the provider interface and never share a cache. Having both is the point: a user who
would rather not hold a data slice keeps the option.

## Absent data

| Situation | Knowable | Policy |
|---|---|---|
| No slice covers this area | before drawing | `ErrNoCoverage`, no image. The consumer renders exactly as it would with no basemap configured, and says so in words. |
| The slice covers part of the view | before drawing | Draw it, hatch the gaps, report the covered fraction. **A placeholder, not a blank.** |
| Only shallower tiles (evicted, or the route left the fetched cells) | before drawing | Draw the ancestor, overzoomed, and report it. Silent, honest degradation with no hole. |
| A layer has no features here (no water inland, no buildings on a trail) | not applicable | Draw nothing. This is **not absence**; it is the correct picture of an area without water, and reporting it would cry wolf. See trap T9. |
| No named place anywhere on the route | before drawing | The consumer's label declines and its siblings grow. |
| No named place within the distance cap | per frame | No answer. **The previous name is not held.** A name carried past its cap is a confident claim about where somebody was. |
| A place name across a data gap | per frame | A name is a nominal quantity and is **never interpolated**, blended, or carried across a gap. |

## Build order

Each step is independently testable, and every step before the acquisition work is
entirely offline.

**Prototypes first.** P1: read a real archive and record its header fields, directory
structure, root directory size, and the byte-range layout of one cell's sub-pyramid. It is
a measurement rather than a gate, and what it decides is how requests are coalesced and how
long the planning phase takes. It is now permanent as `cmd/osmbase inspect`, so the
measurement can be repeated against any archive rather than living in a throwaway. P2 is MEASURED and the design stands: a zoom-12
cell's zoom-12-to-15 pyramid is 8.9 MB for the City of London, 3.7 MB for central Sydney,
2.0 MB for the Blue Mountains, 21 KB for outback New South Wales and 6 KB mid-Pacific.
The assumption was five to fifteen megabytes for an urban cell, so nothing moves -- and the
tail matters as much as the head: an empty or oceanic cell is effectively free, so rounding
generously outward to whole cells costs nothing where there is nothing. P3 is MEASURED and
the guess was wrong in a useful direction: suburbs are `neighbourhood`, not `macrohood`.
Inner Sydney returns 88 of them including Newtown, Surry Hills, Redfern, Glebe and
Marrickville, so the nearest-place fallback is a usable product rather than a degraded
mode. Two caveats it brings: the entries carry `min_zoom` 13, so a lookup must reach that
deep to find one, and `population` is zero with `population_rank` 1 for every suburb, so
distance is the only usable discriminator. P5:
stroker quality on a switchback, a multi-way junction and a dashed path, at both
resolutions. P6: boundary derivation wall time, peak memory, polygon count and output size.

| # | Step | First verifiable thing |
|---|---|---|
| 1 | `pmtiles` | Golden tile bytes out of a synthetic archive |
| 2 | `mvt`, including ring normalisation | Every geometry command type against a hand-built fixture |
| 3 | `slice` | Fill, evict, reopen, resume a partial cell. **Built**, and a store filled from the real archive renders with no network in scope |
| 4a | `raster` | Golden PNGs. No map data involved |
| 4 | `acquire` | A local HTTP test server with range support |
| 5 | `render` | A known coordinate is water; a road from one tile draws over landuse from another |
| 6 | `cmd/osmbase render` | **The first picture**, with no consumer involved |
| 7 | Styling and the contrast test | Eyes on step 6's output |
| 7a | `osmpbf` | A small hand-built PBF fixture |
| 7b | `boundary` | A known coordinate resolves to a known suburb |
| 8 | The fitdash adapter | **The first render** |
| 9 | The fetch and boundaries subcommands | A no-GPS activity is refused |
| 10 | Flags, defaults, summary wording, `NOTICE` | An unflagged third-party render is unchanged |
| 11 | `place`, containment first | A point near a boundary resolves correctly |

## Traps

Things that will look like they work, and things a reader may later "fix" wrongly.

**T1 — Rendering tile by tile.** Layers must be drawn across **all** tiles, layer by layer,
into one rasterizer. Draw tile by tile and one tile's landuse paints over another's road
wherever a feature crosses the seam, and abutting clipped rings leave a permanent hairline
along every tile edge. It is invisible on a route in the middle of a tile and obvious on
one that crosses a boundary, which depends on where the user happens to live.

**T2 — Wrapping the local provider in the consumer's image cache.** See "How a consumer
uses it". Someone will read the existing wrapping and fix the inconsistency.

**T3 — Putting an expiry on the slice.** There must not be one. This is offline data,
deliberately acquired; expiring it breaks a render on a plane, which is the case it exists
for. Staleness is **reported**, never enforced.

**T4 — Overzoom looks sharp, so it looks complete.** Public builds stop at zoom 15. A tight
zoom renders that geometry at a deeper scale: lines stay crisp, so it looks right, but
detail that exists in OSM is simply not in the tile. Someone will try to fix a thin trail
map by raising the maximum zoom. There is nothing above 15 to fetch.

**T6 — Querying only the containing tile for a place.** A centroid three hundred metres
away can live in the neighbouring tile. Query the containing tile and its eight neighbours,
or the answer is wrong in a band around every tile edge.

**T7 — Null Island.** A bounding box computed from samples without checking the GPS
presence flag produces a box around the Gulf of Guinea for an indoor activity. Use
GPS-present samples only, and **refuse** an activity with fewer than two of them.

**T8 — Re-rasterizing per frame because it is now free.** With no network, "just re-render
at each frame's viewport" becomes tempting, and it is genuinely sharper. It is also tens of
thousands of rasterizations. The basemap is a render-wide invariant resolved once.

**T9 — Measuring coverage by ink.** "Did any geometry draw" is not coverage. An ocean tile
legitimately draws almost nothing. Coverage is measured from which cells exist in the
store, never from the rendered image.

**T11 — "No third-party requests."** Acquisition is one. Any claim missing the words "at
render time" is wrong.

**T12 — Importing a PMTiles library "just for the extract".** It is the obvious shortcut,
it is correctly licensed, and it works. It also puts a web server, three cloud SDKs,
Prometheus and sqlite into two public repositories' dependency graphs. The hand-written
range logic carries a comment saying so, because the reason is not visible from the code.

**T13 — Reversed ring orientation fills solid.** Absolute accumulated winding means a hole
whose exterior is missing from the same path fills, and two copies of one ring with
opposite winding cancel to nothing. Normalise on decode, and put every ring of a polygon
into the same path. A reader who later "optimises" by rasterizing exteriors and holes
separately, or one tile per rasterizer, breaks both properties at once, and the symptom is
a hairline grid or a solid lake rather than anything that looks like a winding bug.

**T14 — The boundary file is a snapshot.** Suburb boundaries change rarely, so a file
derived today will still answer confidently in four years. Its derivation date is reported
alongside the tile slice's.

## Open questions

Things this design assumes and has not verified. Each is listed with what it gates.

- **Bytes per cell.** Five to fifteen megabytes is inferred from typical tile sizes, not
  measured. If a dense urban cell is forty, the cell zoom or the maximum zoom changes.
- **Boundary derivation cost.** Wall time, peak memory and output size are estimates. They
  gate whether the node map needs to spill to disk.
- **The tile source's terms** for programmatic access, and **the country extract host's**.
  Both are expected to be plain ODbL with no additional conditions. Neither has been read,
  and they gate the steps that contact them.
- **Rasterization time** for a large view with buildings. Probably comparable to the
  network fetch it replaces, and it happens a handful of times per render, but a zooming
  render with four views has not been measured.
- **Whether zoom 15 keeps trail paths.** A trail run is a primary use case, and zoom 15 is
  where footpaths either survive generalisation or do not. One PNG at step 6 answers it.
