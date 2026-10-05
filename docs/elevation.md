# Elevation: where terrain would come from

Hillshading is built and contours are not; see "Where it stands" at the end. This is the
comparison behind choosing an elevation source, for two things that might come later: a map
that shows the shape of the ground -- hillshading and contour lines under the streets, the
look of Thunderforest's Outdoors style -- and, much further out, a flyover video of an
activity over a 3D landscape, in the manner of Relive.

OpenStreetMap has no elevation surface. Buildings carry heights (in the Protomaps tiles this
module reads, `height` is on 115 of 1,075 buildings in one zoom-15 tile of Hornsby) and roads
carry `is_bridge`, `is_tunnel` and `layer`, but the ground itself has to come from a digital
elevation model (DEM) of its own, with its own licence.

Researched October 2026. Licences and hosting change; check them again before building.

## The candidates

| | Mapterhorn | AWS Terrain Tiles (Mapzen/Tilezen) | Copernicus GLO-30 directly |
|---|---|---|---|
| What it is | Terrain tiles assembled from 150-odd open DEMs, best available per place | Terrain tiles assembled in 2016-17 from a dozen DEMs | The 30 m global DEM itself |
| Format | Terrarium-encoded 512 px WebP tiles in PMTiles archives | Terrarium (and other) 256 px PNG tiles, one file per tile on S3 | Cloud-optimised GeoTIFF, 1° squares |
| Resolution | 30 m worldwide (GLO-30); national LiDAR where open -- 5 m for Australia, 0.4 m for Denmark | Mostly 30-90 m (SRTM, GMTED); 3DEP in the US, a few national sets | 30 m |
| Zooms | 0-12 in one global archive; 13-17 in regional archives where the data is finer | 0-15 | n/a (raw grid) |
| Access | HTTP range reads of PMTiles, or a tile endpoint | One GET per tile | One GET per 1° square, or range reads |
| Maintained | Yes; new project (2025), NLnet-funded, led by a former MapLibre coordinator | Frozen since the Mapzen shutdown | Yes, by ESA; periodic releases |
| Licences | Per source: CC BY 4.0, public domain, open government licences, Copernicus. No share-alike, no non-commercial | Per source: public domain, CC BY 3.0/4.0, OGL. No share-alike, no non-commercial | Copernicus GLO-30 licence (below) |
| Attribution | One credit per source used in the view; the list is machine-readable (`attribution.json`) | One credit per source; a fixed list | One fixed notice |

### Mapterhorn

The strongest candidate, and the closest fit to what this module already does.

- **It is the same shape as the vector tiles.** PMTiles archives read by HTTP range request
  is exactly how osmbase reads the Protomaps basemap today, so the fetch, the store, the
  per-cell caching and the opt-in consent all carry over. Only the decoding is new, and
  Terrarium is three lines: `elevation = R*256 + G + B/256 - 32768` metres.
- **It is the best data where it matters here.** Sydney is covered by Geoscience Australia's
  5 m LiDAR model and Denmark by Klimadatastyrelsen's 0.4 m one, both CC BY 4.0; elsewhere it
  falls back to GLO-30. The regional archives stop where the data does: Sydney's at zoom 14,
  Copenhagen's at 17. Fine enough for contours that follow a street, not just a hillside.
- **The archives are large, but nothing needs them whole.** The global archive is 356 GB,
  the Sydney region's 3.5 GB and the Copenhagen region's 59 GB, but a range read takes only
  the tiles a view needs, as with the vector tiles. What a cell costs has not been measured.
- **Attribution varies by place.** A view over Sydney owes Geoscience Australia and,
  around it, Copernicus; one over Copenhagen owes Klimadatastyrelsen. The machine-readable
  source list makes that computable, as osmbase already computes OpenStreetMap's credit.
  Whether the tiles carry which source each covers, or that has to come from the coverage
  data, is the first thing to find out.
- **It is young.** One maintainer and a grant. The pipeline is open source (BSD-3) and the
  archives are static files, so a copy could be mirrored or rebuilt if the hosting stopped,
  but that is a real cost to plan for rather than assume away.

### AWS Terrain Tiles

The safe fallback: on AWS's open-data programme since 2017, needing no account, and read by
nearly every web map library. It is frozen -- nothing has been updated since Mapzen closed --
and coarser than Mapterhorn almost everywhere: 30-90 m in most of the world, so contours at
street zooms would be smooth guesses. One GET per 256 px PNG tile, rather than range reads,
so it would need a second fetch path beside the PMTiles one.

### Copernicus GLO-30 directly

The best single global source, and the one both tile sets fall back to outside the countries
with national LiDAR. Taking it raw means tiling it ourselves: reading 1° GeoTIFF squares,
reprojecting to Web Mercator and resampling per zoom. That is a pipeline Mapterhorn has
already built and runs, which is the argument for taking it through Mapterhorn instead.

Its licence, read in full (the GLO-30 public instance, "full, free and open"):

- Grants reproduction, distribution, communication to the public, and adaptation, modification
  and combination with other data, worldwide, without limit in time, free of charge. There is
  no non-commercial restriction. (A separate, general Copernicus licence for contributing
  missions does restrict the public to non-commercial use, but it yields to any dataset
  available under other terms, and GLO-30 is.)
- Requires, for modified data such as a hillshade or contours:
  "produced using Copernicus WorldDEM-30 © DLR e.V. 2010-2014 and © Airbus Defence and Space
  GmbH 2014-2018 provided under COPERNICUS by the European Union and ESA; all rights reserved".
- Requires a liability disclaimer to be carried in any licence or notice covering
  distribution: "The organisations in charge of the Copernicus programme by law or by
  delegation do not incur any liability for any use of the Copernicus WorldDEM-30". For this
  repository that means a line in `NOTICE` and in the README, once the data is used.
- Forbids implying endorsement by Copernicus, ESA or Airbus.

**GLO-30 is a surface model, not a terrain model.** Its heights include tree canopy and
buildings. A hillshade from it shows forests as texture and cities as bumps; contours wrap
round woods. National LiDAR models in Mapterhorn are bare-earth terrain, so the effect is
confined to where it falls back to GLO-30 -- which is most of the world outside Europe,
North America, Japan and Australia.

## How each fits the Apache-2.0 licence

None of the candidates' data licences propagate to code: the data is fetched at run time and
never shipped in the repository, as with the OpenStreetMap tiles. What they impose is on the
output -- a rendered map or video carries the credits -- and on this project's notices.
CC BY 4.0, the open government licences and the Copernicus licence are all compatible with
that, and none is share-alike, so a map combining them with OpenStreetMap's ODbL-derived tiles
raises no conflict beyond listing every credit.

### Why commercial use matters to a non-commercial project

This project and its author's own use of it are non-commercial, so a non-commercial licence
would cover everything done here. It still matters, because Apache-2.0 lets anyone take the
code and use it for any purpose, commercial included, and nothing here can or should stop
that. The data is never shipped with the code: whoever runs it fetches the data themselves and
is bound by its licence directly, not through this project. So the job here is to choose a
default that does not turn an Apache-2.0 program into a licence trap for someone who reads
"Apache-2.0" and builds on it -- which is why every candidate above was checked for
non-commercial terms, and none has them -- and to state plainly, in the README and `NOTICE`,
what the data asks of whoever uses it. A source with non-commercial terms could still be
offered, but only as an opt-in that says so, never as the default.

As with the map tiles, a terrain fetch tells the host where the view is. It belongs behind the
same opt-in, the same consent and the same local store.

## Recommendation

Mapterhorn, read as PMTiles through the existing fetch and store, with its own per-source
attribution. AWS Terrain Tiles as the documented fallback if Mapterhorn's hosting proves
unreliable. Copernicus GLO-30 directly only if both fail, since it means building the tiling
pipeline Mapterhorn already runs.

## What the checks found

Three things were left open above; all three were checked in October 2026.

**The tiles are lossless and the resolution is as claimed.** Every tile fetched was WebP's
lossless `VP8L`, 512 pixels square, 57-118 KB. Decoded as Terrarium they read Hornsby as a
plateau at 200 m cut by gullies to 37 m and Copenhagen as 0 to 12 m. Hornsby's zoom-14 tile is
4 m a pixel, the 5 m LiDAR, and there is no zoom 15 there -- a closer view scales zoom 14 up.
Copenhagen's zoom 17 is 0.34 m a pixel. A test hillshade of Hornsby shows the quarry, the
gullies, the railway cutting and the terraced house lots. One of Copenhagen shows the cost
of bare-earth city data: where buildings were taken out, the ground under them was filled
with flat triangles, which a hillshade on ground that flat turns into facets. Shading has to
be weighed by how much relief there actually is, and the deepest zooms smoothed.

**A view's sources come from a coverage tileset.** Mapterhorn publishes `coverage.pmtiles`
(342 MB), vector tiles with one polygon per source in a layer `coverage` and the source's id
in a `source` property: around Hornsby `au5i`, Geoscience Australia's LiDAR, and `glo30`;
around Copenhagen `dk` and `glo30`. The ids are those of `attribution.json`, which names the
producer and licence of each. A view's credit is the sources whose polygons it crosses --
decoded by the same MVT reader that reads the map.

**No usage policy is published**, for the tile endpoint or the download servers. The archives
-- the global one, the regional ones, the coverage -- answer HTTP range requests, so osmbase
can read them exactly as it reads the Protomaps basemap: only the cells a view needs, kept in
the local store for good, behind the same opt-in, with an honest User-Agent and a backoff on
429. That is the behaviour a static archive on object storage is built for, and asks less of
the host than a tile endpoint would.

## Build plan

Each step is useful on its own and ends in something that can be looked at.

1. **Terrain in the store.** `fetch` refuses an archive that is not vector tiles and the
   store names every tile `.mvt`; a source's tile type has to decide both. Terrain also
   comes from several archives: zooms 0-12 from `planet.pmtiles`, 13-17 from the regional
   archive named for the cell's zoom-6 ancestor, where one exists (`download_urls.json` lists
   them). So a terrain source is a small set of archives chosen per cell, rather than one.
   Coverage is a third, vector, source.
2. **Decoding.** A Terrarium tile to a grid of metres, through `golang.org/x/image/webp`, which
   this module already has the dependency for. Scaling up past a region's deepest zoom, as the
   vector tiles already do.
3. **Hillshading.** A shade per pixel under the map's fills, from the slope and its direction
   in the view's own pixels; its strength set by the relief in view, so a flat city is not
   faceted and a mountain not burnt out; a palette role for the shade colour; off unless
   asked for, since it needs the opt-in fetch. Attribution from the coverage, alongside
   OpenStreetMap's.
4. **Contours.** Traced from the same grid by marching squares at an interval for the zoom,
   every fifth line heavier and labelled along the line with the street-name placement.

Then, separately and later, the tilted view and the flyover.

### Where it stands

Steps 1 to 3 are built. `osmbase fetch --terrain` fills a terrain store beside the map's and
copies the source list into it; the `dem` package decodes the tiles; and `osmbase render
--terrain` shades under the map.

What the hillshade does, and why:

- **Heights at one sample per pixel.** The elevation zoom is the view's tile zoom less one, since
  an elevation tile is 512 pixels; a zoom the store lacks is interpolated from the nearest
  shallower one, as the vector tiles overzoom.
- **Under the roads and the buildings.** The shade tints the style's leading run of area fills --
  the ground and its cover -- and stops at the first line or building. A building is not ground:
  bare-earth data fills the ground under a removed building with flat triangles, and shading a
  footprint drew them as smudges on its roof.
- **Strength from the slopes themselves, eased.** A tint toward the palette's Shade on slopes
  facing away from a north-west sun 45° up and toward its Highlight on slopes facing it, eased
  past a knee so a mountain is darker than a hillside but not burnt out. Flat ground stays flat:
  central Copenhagen is barely tinted, where the first probe -- a plain hillshade at full
  strength -- turned its triangles into facets. Slopes are steepened at shallow zooms, where coarse
  data flattens them, and drawn as measured from zoom 13.
- **A blur of 5 m.** At street zooms the heights are blurred over about five metres of ground,
  which removes what is left of the triangles around footprints and keeps any slope a reader
  would call a hill. Coarser than a few metres a pixel it is under a pixel and skipped.
- **The contrast check sees the shade.** The steepest slopes go 45% of the way to Shade or
  Highlight, and the check holds every shaded surface at those extremes to the same overlay
  and context rules as on flat ground. It is what set the light palette's shade: the slate
  first tried left the overlay's accent at 2.3:1 on water in shadow.
- **The credit from the coverage.** The sources whose coverage polygons cross the view, holes
  and all, with Copernicus's dictated sentence for GLO-30. Mapterhorn's coverage places GLO-30
  under the national data too -- its footprint is the whole world, with no holes where finer
  data exists -- so in practice GLO-30 is credited everywhere, and a credit wider than the image
  wraps onto a second line rather than being cut off.

## Sources

- [Mapterhorn](https://mapterhorn.com/), its [data access](https://mapterhorn.com/data-access/)
  and [attribution](https://mapterhorn.com/attribution) pages, and the
  [source list](https://download.mapterhorn.com/attribution.json) and
  [archive list](https://download.mapterhorn.com/download_urls.json) they publish
- [Mapterhorn on GitHub](https://github.com/mapterhorn/mapterhorn) and the
  [Protomaps announcement](https://protomaps.com/blog/mapterhorn-terrain/)
- [Terrain Tiles on the AWS Registry of Open Data](https://registry.opendata.aws/terrain-tiles/)
  and their [attribution](https://github.com/tilezen/joerd/blob/master/docs/attribution.md)
- [Copernicus DEM collection](https://dataspace.copernicus.eu/explore-data/data-collections/copernicus-contributing-missions/collections-description/COP-DEM)
  and the [GLO-30 licence](https://docs.sentinel-hub.com/api/latest/static/files/data/dem/resources/license/License-COPDEM-30.pdf)
