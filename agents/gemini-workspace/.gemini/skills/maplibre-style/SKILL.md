---
name: maplibre-style
description: Decide when to author a MapLibre Style Spec and how to apply styles in react-map-gl components.
---
# MapLibre Style Authoring (react-map-gl)

## When to use
- User asks to change visual styling (colors, labels, layer visibility, emphasis).
- Task is primarily cartographic presentation rather than heavy data processing.

## Core knowledge
- Style Spec is JSON with version, sources, layers, glyphs/sprites.
- Layer order controls rendering priority.
- Filters and paint/layout properties should be explicit and readable.
- `tile.yuiseki.net` styles do not require access tokens.
- Never include token placeholders such as `YOUR_MAPLIBRE_GL_ACCESS_TOKEN`.

## Usage in react-map-gl
```tsx
import Map from "react-map-gl/maplibre";

// Apply style via mapStyle prop
<Map mapStyle="https://tile.yuiseki.net/styles/osm-bright/style.json" />

// Or use inline style object
<Map mapStyle={customStyleObject} />
```

## Recommended workflow
1. Choose base style source (`tile.yuiseki.net` styles when possible).
2. Add or modify layers to match user intent using `<Layer>` components from `react-map-gl`.
3. Validate style structure and field names.
4. Run `make build` to verify.

## Output expectations
- If style is a separate file, persist as `public/style.json`.
- Keep style and preview easy to inspect and iterate.
