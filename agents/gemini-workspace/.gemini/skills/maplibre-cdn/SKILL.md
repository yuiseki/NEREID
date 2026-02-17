---
name: maplibre-cdn
description: Use pinned MapLibre GL JS/CSS CDN assets when generating HTML maps. Trigger when writing or updating index.html that loads MapLibre.
---
# MapLibre CDN Pinning

## Purpose
- Keep Map rendering behavior stable by pinning MapLibre asset URLs.

## Required assets
- JavaScript: `https://unpkg.com/maplibre-gl@5.18.0/dist/maplibre-gl.js`
- CSS: `https://unpkg.com/maplibre-gl@5.18.0/dist/maplibre-gl.css`

## Required behavior
1. When creating MapLibre-based HTML, include both pinned JS and CSS URLs above.
2. Do not switch versions unless the user explicitly requests a different version.
3. Keep script/style tags explicit in `index.html` so runtime dependencies are auditable.
4. Do not add or request any MapLibre/Mapbox access token for `tile.yuiseki.net` styles.
5. Never emit placeholders such as `YOUR_MAPLIBRE_GL_ACCESS_TOKEN` or `YOUR_MAPTILER_KEY`.

## Example snippet
```html
<link href="https://unpkg.com/maplibre-gl@5.18.0/dist/maplibre-gl.css" rel="stylesheet" />
<script src="https://unpkg.com/maplibre-gl@5.18.0/dist/maplibre-gl.js"></script>
```
