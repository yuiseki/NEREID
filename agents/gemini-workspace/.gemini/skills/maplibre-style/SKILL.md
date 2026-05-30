---
name: maplibre-style
description: Apply MapLibre style spec from legacy-work-spec.json to public/layers/config.json. Do NOT read or modify src/App.tsx.
---
# MapLibre Style (config.json approach)

## CRITICAL: Do NOT modify src/App.tsx. Do NOT read src/App.tsx.

## Workflow

1. Read `legacy-work-spec.json` — find the `style.sourceStyle.json` field (inline style JSON string).
2. Save that style JSON to `public/layers/style.json`:
   ```bash
   python3 -c "
   import json
   spec = json.load(open('legacy-work-spec.json'))
   style = spec.get('style', {}).get('sourceStyle', {}).get('json', '{}')
   if isinstance(style, str): style = json.loads(style)
   json.dump(style, open('public/layers/style.json', 'w'), ensure_ascii=False)
   print('Saved style.json')
   "
   ```
3. Write `public/layers/config.json`:
   ```json
   {
     "title": "<title from spec>",
     "mapStyle": "./layers/style.json",
     "initialView": {"longitude": 0, "latitude": 20, "zoom": 1.7},
     "showPopupOnClick": false,
     "layers": []
   }
   ```
4. Run `make fast-build`.

## Notes
- `mapStyle` in config.json overrides the default tile style — App.tsx supports this.
- If `sourceStyle.json` is not in the spec, use a URL from `tile.yuiseki.net`.
- Keep the style file at `public/layers/style.json` so `make fast-build` copies it.
