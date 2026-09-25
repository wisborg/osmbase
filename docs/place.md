# Rendering a place by name

`osmbase render --place Denmark` draws a map of Denmark. This is `locate` run the other way:
a name in, a piece of ground out. Where `locate` asks which area holds a coordinate, `--place`
asks which areas carry a name, and then how to fit the one meant into an image.

Everything is resolved from files already in the store. A name is never sent to a geocoding
service — sending "where are you going" to a third party is the same leak this project refuses
for tiles, and the boundaries already on disk can answer it.

## The three steps

1. **Name → candidates.** Search the store's boundaries by name.
2. **Candidates → one.** Disambiguate, or refuse and list them.
3. **One → view.** Take the area's extent and fit it into `--width` by `--height`.

Step 3 is `render --bbox`, which is useful on its own and is the target the other two feed.

## Where names come from, and what each can say

| source | gives | reaches |
|---|---|---|
| Natural Earth countries | `NAME`, `NAME_EN`, `NAME_LONG`, `ISO_A2`, a full outline | the world, 258 features |
| Natural Earth regions | `name`, `name_en`, `admin` (the country), `type_en`, `iso_3166_2` | the world, 4,596 features |
| derived OpenStreetMap files | a name and an `admin_level`, a full outline | only where a file was built |
| tiles | named points, no extent | only where tiles were fetched |

Stage one of this uses Natural Earth only. Derived files and tile points are later parts.

## Ambiguity is the normal case, not the edge

Measured against the 10m files: `Luxembourg` is a country, a district of Luxembourg and a
province of Belgium. `Georgia` is a country and a US state. `Distrito Federal` is in Brazil and
in Mexico. And `Newcastle` finds only Newcastle upon Tyne, because Natural Earth's regions in
Australia are whole states — the Australian one needs a derived file.

So an ambiguous name is **refused, with the candidates listed**, and never resolved to the
first match. Two ways to narrow it, either of which may be enough:

- **A qualifier**: `--place "Luxembourg, Belgium"`. The part after the comma is matched against
  each candidate's context — for a region, its country's name or ISO code.
- **A level**: `--place-level country|region`.

A name matches when it equals any of an area's names or codes, ignoring case: `Denmark`, `DK`
and `DNK` are the same country, and so are `France`, `FR` and `FRA` — Natural Earth writes -99
in France's plain ISO fields, so the `_EH` and `ADM0_A3` codes are read too. A name that only
*begins* a place's name is offered and never taken: `Newcastle` suggests Newcastle upon Tyne
and draws nothing, because the Newcastle meant may be one this data does not hold.

One data error needed a rule. Seven regions in the 10m file carry their country's name as their
English one — Hovedstaden's `name_en` is "Denmark", Guyane française's is "France" — which made
both countries ambiguous. A region's name is never the name of the country it is in, so those
aliases are dropped.

## The extent is not the bounding box of everything

France's Natural Earth outline has 21 parts from longitude −61.8 to +55.9: the overseas
departments are part of it. The box around all of them is the Atlantic. So the view is fitted to
the **main part** — the largest polygon by box area — together with every other part whose box
lies within a margin of it, and the parts left out are reported. Denmark keeps Bornholm; France
keeps Corsica and loses Guiana, Réunion and the rest, and says so.

An area crossing the antimeridian is detected and refused for now, as `locate` does.

## When the store does not hold the tiles at that zoom

`render --store` never touches the network, and that stays true unless the user says otherwise.
When the store lacks the tiles a view needs at its zoom, render **offers** to fetch them,
following what the same offer in fitdash learned:

- The shortfall is measured **from the disk alone**. Planning a fetch reads the archive's
  directories, which already tells the host the area; asking afterwards would be asking
  permission for something done.
- The question names the host and the area **before** anything is read. The exact cost is shown
  by the plan once it is made, and the progress line reports against it.
- It is asked **only when a terminal can answer**. A render started from a script inherits a
  pipe that may never close. `--yes` answers in advance.
- A shortfall is tiles missing **at the render's zoom**, not tiles drawn: an overzoomed view
  reports full coverage and carries a fraction of the detail.

A country fits in an image at about zoom 7, which is shallower than the store's cell grid. The
fetch planner used to reach those shallow tiles through the cells beneath them, which for
Denmark is several thousand cells and past the planner's limit; a shallow request now lists its
tiles directly from the bounds.

## Parts

| # | part | status |
|---|---|---|
| 1 | Shallow fetch plans | ✅ a request above the cell zoom lists its tiles from the bounds; bounded by `MaxOverviewTiles`, not by cells |
| 2 | `render --bbox` | ✅ the deepest zoom that holds the rectangle, centred in the projection; cropped and said so when wider than the world |
| 3 | Natural Earth names | ✅ `boundary.Source.Find`: candidates with level, type, country and main-part extent |
| 4 | `--place` | ✅ on `render` and `fetch`, with `--place-level`; ambiguity refused with the list and a suggestion that works |
| 5 | The offer | ✅ `render --store` asks to fetch what the view lacks at its zoom, measured by `slice.Source.HeldAt`; `--yes` answers in advance |
| 6 | Derived files | not started: suburbs and councils where a file was built, context by containment |
| 7 | Tile points | not started: cities no outline covers, zoom from the kind — the weakest source, last |

Measured on a copy of a real store holding tiles around Horsens: `render --place Denmark`
fits Denmark, Bornholm included, at zoom 7, where the store held 2 of the 20 tiles the view
needs. The offer said so before anything was read, and declined, the render drew the rest
from shallower tiles and reported 83% of the image overzoomed.

## What this did not settle

- **`fetch --place` of a country plans the whole world**, at zooms 0 to 5, because the
  planner's depth bands put anything over 250 km there — the same as a `--bbox` of it would.
  `--max-zoom` asks for the area itself. A render's offer does not have the problem: it asks
  for the view's own zoom.
- **Overview tiles an archive lacks are offered again on every render.** A complete cell
  records that its fetch finished, so a tile missing from it is known to be absent; the
  overview has no such record. Protomaps' builds hold every shallow tile, so this has not been
  seen, but an archive with gaps above the cell zoom would meet it.
- **Names are English or codes.** Natural Earth's `name` is sometimes local, but `Danmark`
  finds nothing. Derived files carry OpenStreetMap's local names and are part 6.
- **The United States includes Alaska and Hawaii**, because the main-part rule gathers parts
  one step at a time and each is within reach of the last. A cartographer might do either;
  `--bbox` is the way to the other.
