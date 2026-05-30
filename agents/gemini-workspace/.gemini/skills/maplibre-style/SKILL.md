---
name: maplibre-style
description: Apply MapLibre style spec from legacy-work-spec.json to public/layers/config.json. Handles both inline JSON (mode=inline) and URL reference (mode=url) to tile.yuiseki.net. Do NOT read or modify src/App.tsx.
---
# MapLibre Style (config.json approach)

## CRITICAL: Do NOT modify src/App.tsx. Do NOT read src/App.tsx.

## Workflow

Read `legacy-work-spec.json` and dispatch by `style.sourceStyle.mode`:

```python
python3 -c "
import json, os
spec = json.load(open('legacy-work-spec.json'))
title = spec.get('title', 'NEREID Map')
render = spec.get('render', {})
vp = render.get('viewport', {})
lon = vp.get('center', [0, 20])[0]
lat = vp.get('center', [0, 20])[1]
zoom = vp.get('zoom', 1.7)
mode = spec.get('style', {}).get('sourceStyle', {}).get('mode', 'inline')

os.makedirs('public/layers', exist_ok=True)

if mode == 'url':
    # STEP A: URL mode — mapStyle points directly to tile.yuiseki.net
    style_url = spec['style']['sourceStyle']['url']
    config = {
        'title': title,
        'mapStyle': style_url,
        'initialView': {'longitude': lon, 'latitude': lat, 'zoom': zoom},
        'showPopupOnClick': False,
        'layers': []
    }
    print(f'URL mode: {style_url}')
else:
    # STEP B: inline mode — save JSON to style.json
    style = spec.get('style', {}).get('sourceStyle', {}).get('json', '{}')
    if isinstance(style, str): style = json.loads(style)
    json.dump(style, open('public/layers/style.json', 'w'), ensure_ascii=False)
    config = {
        'title': title,
        'mapStyle': './layers/style.json',
        'initialView': {'longitude': lon, 'latitude': lat, 'zoom': zoom},
        'showPopupOnClick': False,
        'layers': []
    }
    print('inline mode: saved style.json')

json.dump(config, open('public/layers/config.json', 'w'), ensure_ascii=False, indent=2)
print('Saved config.json')
"
```

Then run: `make fast-build`

## tile.yuiseki.net 利用可能スタイル一覧 (mode=url で直接参照可能)

| スタイル名 | URL | 内容 |
|-----------|-----|------|
| conflicts | `https://tile.yuiseki.net/styles/conflicts/style.json` | UCDP 武力紛争データ |
| biodiversity | `https://tile.yuiseki.net/styles/biodiversity/style.json` | 生物多様性・保護区 |
| healthcare | `https://tile.yuiseki.net/styles/healthcare/style.json` | 医療施設・ヘルスケア |
| admins | `https://tile.yuiseki.net/styles/admins/style.json` | 行政境界 |
| global_connectivity | `https://tile.yuiseki.net/styles/global_connectivity/style.json` | 海底ケーブル・通信インフラ |
| energy_transition | `https://tile.yuiseki.net/styles/energy_transition/style.json` | エネルギー転換・再生可能エネルギー |
| disaster_prevention | `https://tile.yuiseki.net/styles/disaster_prevention/style.json` | 防災・自然災害リスク |
| peacekeeping_network | `https://tile.yuiseki.net/styles/peacekeeping_network/style.json` | 平和維持・国連ミッション |
| global_seismic_alerts | `https://tile.yuiseki.net/styles/global_seismic_alerts/style.json` | 地震・プレートテクトニクス |
| water_stress | `https://tile.yuiseki.net/styles/water_stress/style.json` | 水ストレス・水資源 |
| railways | `https://tile.yuiseki.net/styles/railways/style.json` | 鉄道ネットワーク |
| rivers | `https://tile.yuiseki.net/styles/rivers/style.json` | 河川・水系 |
| osm-bright | `https://tile.yuiseki.net/styles/osm-bright/style.json` | OSM ベースマップ (明) |
| osm-fiord | `https://tile.yuiseki.net/styles/osm-fiord/style.json` | OSM ベースマップ (暗) |

## Notes
- `mapStyle` in config.json overrides the default tile style — App.tsx supports this.
- URL mode は style.json を作成しない（`make fast-build` は layers/ をコピーするだけ）
