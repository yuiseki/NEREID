---
name: osmable-v1
description: Use osmable CLI for deterministic OSM geocoding, AOI, POI, and routing workflows.
---
# osmable Workflow

## When to use
- User request involves OSM data retrieval or geospatial operations (Nominatim/Overpass/Valhalla).
- You need deterministic CLI output instead of fragile free-form web scraping.

## Core rules
1. Prefer osmable ... over direct API calls for geocode/aoi/poi/route tasks.
2. Use default text output for concise logs and context efficiency.
3. Use --format json or --format geojson when machine-readable output is required.
4. Run osmable doctor before relying on upstream endpoints for critical flows.
5. There is no dedicated tool named `osmable_v1`; always execute `osmable ...` via shell command tool.
6. Use `osmable` first for agent-side retrieval and validation; when building final interactive HTML output, prefer browser-side fetch in `index.html`.

## Common commands
- Geocode: osmable geocode "東京都台東区" --format json
- AOI: osmable aoi resolve "東京都台東区" --format geojson > aoi.geojson
- POI count: osmable poi count --tag leisure=park --within "東京都台東区" --format json
- POI fetch: osmable poi fetch --tag leisure=park --within "東京都台東区" --format geojson > parks.geojson
- Route: osmable route --from "上野駅" --to "浅草寺" --mode pedestrian --format json > route.json

## Failure and fallback
- If osmable fails, capture stderr and exit code in artifacts for debugging.
- Fall back to direct curl/browser fetch only when osmable cannot satisfy the task.
