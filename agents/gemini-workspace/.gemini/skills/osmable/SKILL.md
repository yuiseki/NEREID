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
5. Save fetch output to `public/parks.geojson` for use by the Vite/React app.
6. There is no dedicated tool named `osmable_v1`; run `osmable ...` via shell command execution.

## Canonical park flow (NEREID)
1. `osmable poi count --tag leisure=park --within "東京都台東区" --format json`
2. `osmable poi fetch --tag leisure=park --within "東京都台東区" --format geojson > public/parks.geojson`
3. If count is unexpectedly zero, retry once:
   - `osmable aoi resolve "東京都台東区" --format geojson > public/aoi.geojson`
   - rerun the same `poi count` / `poi fetch` with the original area string.
4. Verify `public/parks.geojson` is non-empty before building.

## Alternative data fetch
- Use `python3 scripts/fetch_geojson.py --query "leisure=park" --area "東京都台東区" --output public/parks.geojson` as osmable alternative.

## Important option semantics
- `poi count/fetch` requires `--within`; it does not accept `--bbox`.
- `--within` accepts place text, `@file`, or `-` (stdin).
- `poi fetch` optional tuning: `--limit`, `--sort name|id`.

## Invalid command patterns
- Do not use `osmable pois` (invalid); use `osmable poi ...`.
- Do not use `osmable map` (not implemented in upstream CLI).
- Do not switch park queries to unrelated tags (for example `amenity=hospital`) to bypass failures.

## Map handoff — Vite/React workflow (two approaches)

### Approach A: Agent-side fetch (static data)
After fetching data to `public/`, edit `src/App.tsx`:

```tsx
import Map, { Source, Layer } from "react-map-gl/maplibre";
import maplibregl from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";

function App() {
  return (
    <Map
      mapLib={maplibregl}
      initialViewState={{ longitude: 139.78, latitude: 35.71, zoom: 14 }}
      style={{ width: "100%", height: "100vh" }}
      mapStyle="https://tile.yuiseki.net/styles/osm-bright/style.json"
    >
      <Source id="parks" type="geojson" data="/parks.geojson">
        <Layer id="parks-fill" type="fill"
          paint={{ "fill-color": "#2d6a4f", "fill-opacity": 0.4 }} />
        <Layer id="parks-outline" type="line"
          paint={{ "line-color": "#1b4332", "line-width": 1.5 }} />
      </Source>
    </Map>
  );
}
```

### Approach B: Browser-side fetch (swr + osmtogeojson)
For dynamic/real-time data, use swr and osmtogeojson in components:

```tsx
import useSWR from "swr";
import { Source, Layer, useMap } from "react-map-gl/maplibre";
import { overpassGeoJsonFetcher, buildOverpassUrl } from "../lib/overpass";
import { useFitBounds } from "../hooks/useFitBounds";
import { useEffect } from "react";

function ParksLayer({ area }: { area: string }) {
  const url = buildOverpassUrl("leisure=park", area);
  const { data: geoJson } = useSWR(url, overpassGeoJsonFetcher);
  const fitBounds = useFitBounds();

  useEffect(() => {
    if (geoJson) fitBounds(geoJson);
  }, [geoJson, fitBounds]);

  if (!geoJson) return null;

  return (
    <Source id="parks" type="geojson" data={geoJson}>
      <Layer id="parks-fill" type="fill"
        paint={{ "fill-color": "#2d6a4f", "fill-opacity": 0.4 }} />
    </Source>
  );
}
```

- Run `make build` after editing to generate the deployable `index.html`.
- Do not use `https://tile.yuiseki.net/style.json`.
- Do not add token placeholders such as `YOUR_MAPLIBRE_GL_ACCESS_TOKEN`.

## Failure and fallback
- If `osmable` fails, capture stderr/exit code in artifacts for debugging.
- Run `osmable doctor` when endpoint health is uncertain.
- Fall back to `scripts/fetch_geojson.py` or direct curl/browser fetch only when `osmable` cannot satisfy the task.
