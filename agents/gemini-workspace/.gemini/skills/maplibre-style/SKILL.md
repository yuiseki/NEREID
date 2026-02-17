---
name: maplibre-style
description: Decide when to author a MapLibre Style Spec and how to structure layers.
---
# MapLibre Style Authoring

## When to use
- User asks to change visual styling (colors, labels, layer visibility, emphasis).
- Task is primarily cartographic presentation rather than heavy data processing.

## Core knowledge
- Style Spec is JSON with version, sources, layers, glyphs/sprites.
- Layer order controls rendering priority.
- Filters and paint/layout properties should be explicit and readable.
- When rendering MapLibre in HTML, pin assets to:
  - https://unpkg.com/maplibre-gl@5.18.0/dist/maplibre-gl.js
  - https://unpkg.com/maplibre-gl@5.18.0/dist/maplibre-gl.css

## Recommended workflow
1. Choose base style source (tile.yuiseki.net styles when possible).
2. Add or modify layers to match user intent (labels, fills, lines, symbols).
3. Validate style structure and field names.
4. Render preview map in index.html.

## Output expectations
- If style is inline, persist style.json.
- Keep style and preview easy to inspect and iterate.
