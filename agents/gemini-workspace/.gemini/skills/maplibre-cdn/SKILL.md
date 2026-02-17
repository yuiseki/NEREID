---
name: maplibre-cdn
description: "Reference-only: MapLibre GL JS is installed via npm as maplibre-gl v5 and react-map-gl v8. Additional GIS libs: pmtiles, osmtogeojson, swr, @turf/turf. NOT a callable tool."
---
# MapLibre — npm Package Reference

> **This is reference information, NOT a callable tool.**

## Purpose
- MapLibre GL JS is installed as npm dependencies in this Vite+React project.
- No CDN script tags are needed — imports happen in TypeScript/React code.

## Installed packages (dependencies)
- `maplibre-gl` v5 — Map rendering engine
- `react-map-gl` v8 — React wrapper for maplibre-gl
- `pmtiles` — PMTiles protocol for MapLibre
- `osmtogeojson` — Convert Overpass API JSON to GeoJSON
- `swr` — React data fetching hooks
- `@turf/turf` — Geospatial analysis (bbox, buffer, centroid, etc.)

## Usage in React components

### Basic Map
```tsx
import Map from "react-map-gl/maplibre";
import maplibregl from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";

<Map
  mapLib={maplibregl}
  mapStyle="https://tile.yuiseki.net/styles/osm-bright/style.json"
  style={{ width: "100%", height: "100vh" }}
/>
```

### PMTiles Protocol
```tsx
import { Protocol as PMTilesProtocol } from "pmtiles";
import maplibregl from "maplibre-gl";

const protocol = new PMTilesProtocol();
maplibregl.addProtocol("pmtiles", protocol.tile);
```

### Overpass + osmtogeojson (with SWR)
```tsx
import useSWR from "swr";
import { overpassGeoJsonFetcher, buildOverpassUrl } from "./lib/overpass";

const url = buildOverpassUrl("leisure=park", "東京都台東区");
const { data: geoJson } = useSWR(url, overpassGeoJsonFetcher);
```

### fitBounds with Turf.js
```tsx
import { useFitBounds } from "./hooks/useFitBounds";

const fitBounds = useFitBounds();
// Inside Map children:
fitBounds(geoJson, { padding: { top: 40, left: 40, right: 40, bottom: 40 } });
```

## Required behavior
1. Import from `react-map-gl/maplibre` (not `react-map-gl` directly) for MapLibre backend.
2. Pass `mapLib={maplibregl}` prop to `<Map>`.
3. Import `maplibre-gl/dist/maplibre-gl.css` for map styles.
4. Do not add CDN script tags — all dependencies are bundled by Vite.
5. Do not add or request MapLibre/Mapbox access tokens for `tile.yuiseki.net` styles.
6. Never emit placeholders such as `YOUR_MAPLIBRE_GL_ACCESS_TOKEN`.
