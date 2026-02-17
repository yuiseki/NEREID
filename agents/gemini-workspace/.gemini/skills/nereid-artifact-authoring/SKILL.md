---
name: nereid-artifact-authoring
description: Create map artifacts in NEREID Vite+React workspace by editing src/App.tsx.
---
# NEREID Artifact Authoring (Vite + React)

## Purpose
Create interactive map artifacts using the Vite + React + TypeScript + react-map-gl project.

## Project structure
```
gemini-workspace/
├── index.html          ← Vite entry (do not edit directly for content)
├── src/
│   ├── main.tsx        ← React entry point
│   ├── App.tsx         ← **Main editing target**
│   ├── App.css         ← Styles
│   ├── vite-env.d.ts   ← Vite types
│   ├── hooks/
│   │   └── useFitBounds.ts  ← turf.bbox fitBounds hook
│   └── lib/
│       └── overpass.ts      ← osmtogeojson + swr fetcher
├── public/
│   └── styles/         ← Map style JSON files
│       ├── osm_vector.json
│       ├── osm_raster.json
│       └── osm_pmtiles.json
├── scripts/
│   ├── setup.sh
│   └── fetch_geojson.py
├── Makefile
└── package.json
```

## Available libraries
| Package | Use |
|---|---|
| `react-map-gl` v8 | React map components (`<Map>`, `<Source>`, `<Layer>`, `<Marker>`) |
| `maplibre-gl` v5 | Map rendering engine |
| `pmtiles` | PMTiles protocol for MapLibre |
| `osmtogeojson` | Convert Overpass JSON → GeoJSON |
| `swr` | React data fetching hooks |
| `@turf/turf` | Geospatial analysis (bbox, buffer, centroid, etc.) |

## Required behavior
- **Primary edit target**: `src/App.tsx` — modify this file to change map behavior, layers, and data.
- **Add components**: Create under `src/` as `.tsx` files and import from `App.tsx`.
- **Static data**: Place GeoJSON, JSON files in `public/` directory.
- **Data fetching**: Always use relative paths (e.g. `./parks.geojson`) to fetch data from `public/`. DO NOT use absolute paths (`/parks.geojson`).
- **Build**: Run `make build` — builds and copies entire `dist/` contents to project root.
- Always pass `mapLib={maplibregl}` prop to `<Map>`.
- Initialize PMTiles protocol before using `osm_pmtiles` style.
- Do NOT hand-write `./index.html` from scratch.
- NEVER read, request, print, or persist environment variable values.
- For Overpass, always URL-encode data or use `src/lib/overpass.ts` utilities.

## Mapping defaults
- Default style: `https://tile.yuiseki.net/styles/osm-bright/style.json`
- Alternative: `https://tile.yuiseki.net/styles/osm-fiord/style.json`
- Local styles: `./styles/osm_vector.json`, `./styles/osm_raster.json`, `./styles/osm_pmtiles.json`
- `tile.yuiseki.net` styles need NO access tokens.

## Build workflow
```bash
make install   # npm install
make dev       # Vite dev server
make build     # Production build → ./index.html
make typecheck # tsc --noEmit
```

## Output quality
- Use TypeScript types properly.
- Keep components focused and reusable.
- After editing, run `make build` to ensure the artifact passes validation.
