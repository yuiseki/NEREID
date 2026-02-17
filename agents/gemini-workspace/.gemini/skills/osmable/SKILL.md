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
- **Always overwrite `index.html` entirely** using `cat > index.html << 'EOF' ... EOF`. Do NOT use `replace` / partial edits.
- For MapLibre output, use pinned CDN assets and only these base style URLs:
  - `https://tile.yuiseki.net/styles/osm-bright/style.json`
  - `https://tile.yuiseki.net/styles/osm-fiord/style.json`
- Do not use `https://tile.yuiseki.net/style.json`.
- Do not add token placeholders such as `YOUR_MAPLIBRE_GL_ACCESS_TOKEN`.

## Complete index.html template (park map)

After `osmable poi fetch` produces `parks.geojson`, write `index.html` as a single shell command:

```bash
cat > index.html << 'EOF'
<!doctype html>
<html>
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width,initial-scale=1"/>
  <title>Parks Map</title>
  <link href="https://unpkg.com/maplibre-gl@5.18.0/dist/maplibre-gl.css" rel="stylesheet"/>
  <script src="https://unpkg.com/maplibre-gl@5.18.0/dist/maplibre-gl.js"></script>
  <style>html,body{margin:0;padding:0;height:100%}#map{width:100%;height:100%}</style>
</head>
<body>
  <div id="map"></div>
  <script>
    const map = new maplibregl.Map({
      container: 'map',
      style: 'https://tile.yuiseki.net/styles/osm-bright/style.json',
      center: [139.78, 35.71],
      zoom: 14
    });
    map.on('load', async () => {
      const res = await fetch('./parks.geojson');
      const geojson = await res.json();
      map.addSource('parks', { type: 'geojson', data: geojson });
      map.addLayer({
        id: 'parks-fill',
        type: 'fill',
        source: 'parks',
        paint: { 'fill-color': '#2d6a4f', 'fill-opacity': 0.4 }
      });
      map.addLayer({
        id: 'parks-outline',
        type: 'line',
        source: 'parks',
        paint: { 'line-color': '#1b4332', 'line-width': 1.5 }
      });
      const bounds = new maplibregl.LngLatBounds();
      geojson.features.forEach(f => {
        const coords = f.geometry.type === 'Point' ? [f.geometry.coordinates]
          : f.geometry.type === 'Polygon' ? f.geometry.coordinates[0]
          : f.geometry.type === 'MultiPolygon' ? f.geometry.coordinates.flat(2)
          : f.geometry.coordinates?.flat?.(2) || [];
        coords.forEach(c => { if (c && c.length >= 2) bounds.extend(c); });
      });
      if (!bounds.isEmpty()) map.fitBounds(bounds, { padding: 40 });
    });
  </script>
</body>
</html>
EOF
```

Adjust `center`, `zoom`, and `<title>` to match the user's target area. The template satisfies all validation checks (map markers, no bootstrap placeholder, no token placeholders).

## Failure and fallback
- If `osmable` fails, capture stderr/exit code in artifacts for debugging.
- Run `osmable doctor` when endpoint health is uncertain.
- Fall back to direct curl/browser fetch only when `osmable` cannot satisfy the task.

