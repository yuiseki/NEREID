---
name: osmable
description: Use osmable CLI for deterministic OSM workflows (geocode, reverse, aoi, poi, route, isochrone). Trigger for OSM map requests such as parks/shops/stations and Nominatim/Overpass retrieval.
---
# osmable Workflow

## Command surface (upstream)
- `osmable geocode`
- `osmable reverse`
- `osmable tag search`
- `osmable tag info`
- `osmable aoi resolve`
- `osmable poi count`
- `osmable poi fetch`
- `osmable route`
- `osmable isochrone`
- `osmable doctor`

## Core rules
1. Prefer `osmable` for agent-side OSM retrieval/validation before direct curl calls.
2. Default output is `text`; use `--format json` or `--format geojson` for machine-readable pipelines.
3. Keep area text exactly as user wrote it when possible (example: `東京都台東区`); do not silently transliterate to `tokyo taito`.
4. For park requests, use `--tag leisure=park` as primary query tag.
5. Save park fetch output to `parks.geojson` for downstream validation and map rendering.
6. There is no dedicated tool named `osmable_v1`; run `osmable ...` via shell command execution.

## Canonical park flow (NEREID)
1. `osmable poi count --tag leisure=park --within "東京都台東区" --format json`
2. `osmable poi fetch --tag leisure=park --within "東京都台東区" --format geojson > parks.geojson`
3. If count is unexpectedly zero, retry once:
   - `osmable aoi resolve "東京都台東区" --format geojson > aoi.geojson`
   - rerun the same `poi count` / `poi fetch` with the original area string.
4. Verify `parks.geojson` is non-empty before finalizing `index.html`.

## Important option semantics
- `poi count/fetch` requires `--within`; it does not accept `--bbox`.
- `--within` accepts place text, `@file`, or `-` (stdin).
- `poi fetch` optional tuning: `--limit`, `--sort name|id`.

## Invalid command patterns
- Do not use `osmable pois` (invalid); use `osmable poi ...`.
- Do not use `osmable map` (not implemented in upstream CLI).
- Do not switch park queries to unrelated tags (for example `amenity=hospital`) to bypass failures.

## Map handoff constraints
- `osmable` does retrieval; map visualization is authored in `index.html`.
- For MapLibre output, use pinned CDN assets and only these base style URLs:
  - `https://tile.yuiseki.net/styles/osm-bright/style.json`
  - `https://tile.yuiseki.net/styles/osm-fiord/style.json`
- Do not use `https://tile.yuiseki.net/style.json`.
- Do not add token placeholders such as `YOUR_MAPLIBRE_GL_ACCESS_TOKEN`.

## Failure and fallback
- If `osmable` fails, capture stderr/exit code in artifacts for debugging.
- Run `osmable doctor` when endpoint health is uncertain.
- Fall back to direct curl/browser fetch only when `osmable` cannot satisfy the task.
