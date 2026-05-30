---
name: static-geojson-map
description: z.yuiseki.net/static/geojson/ の静的 GeoJSON ファイルをダウンロードして MapLibre レイヤーとして表示する。USGS地震・武力紛争・プレートテクトニクス・海底ケーブルなどのグローバルデータセットに使う。
---
# Static GeoJSON Map スキル

## CRITICAL: Do NOT read src/App.tsx. Do NOT modify src/App.tsx.

## 利用可能な静的 GeoJSON ファイル

| ファイル | URL | 内容 | フィーチャ型 |
|---------|-----|------|------------|
| usgs_m45_month.geojson | `https://z.yuiseki.net/static/geojson/usgs_m45_month.geojson` | 最新30日 M4.5以上地震 (USGS) | Point |
| conflicts.json | `https://z.yuiseki.net/static/geojson/conflicts.json` | 世界の武力紛争イベント | Point/Polygon |
| tectonicplates_GeoJSON_PB2002_boundaries.json | `https://z.yuiseki.net/static/geojson/tectonicplates_GeoJSON_PB2002_boundaries.json` | プレートテクトニクス境界線 | LineString |
| cable-geo.json | `https://z.yuiseki.net/static/geojson/cable-geo.json` | 世界の海底ケーブル | LineString |

## ワークフロー（4ステップ）

### STEP 1: ファイルダウンロード
```python
python3 -c "
import urllib.request, json, os
os.makedirs('public/layers', exist_ok=True)

# legacy-work-spec.json から URL を取得
spec = json.load(open('legacy-work-spec.json'))
files = spec.get('staticFiles', [])  # [{url, layerId, name, emoji, color, outlineColor}]

for f in files:
    url = f['url']
    layer_id = f['layerId']
    out_path = f'public/layers/{layer_id}.geojson'
    print(f'Downloading {url} -> {out_path}')
    req = urllib.request.Request(url, headers={'User-Agent': 'NereidMap/1.0'})
    with urllib.request.urlopen(req, timeout=30) as r:
        data = r.read()
    with open(out_path, 'wb') as out:
        out.write(data)
    fc = json.loads(data)
    print(f'  {len(fc.get(\"features\", []))} features')
"
```

### STEP 2: config.json 生成
```python
python3 -c "
import json, os
spec = json.load(open('legacy-work-spec.json'))
title = spec.get('title', 'Global Data Map')
render = spec.get('render', {})
vp = render.get('viewport', {'center': [0, 20], 'zoom': 1.7})
files = spec.get('staticFiles', [])

layers = []
for f in files:
    layers.append({
        'id': f['layerId'],
        'name': f.get('name', f['layerId']),
        'file': f'./layers/{f[\"layerId\"]}.geojson',
        'emoji': f.get('emoji', '📍'),
        'color': f.get('color', '#2196f3'),
        'outlineColor': f.get('outlineColor', '#0d47a1'),
        'showMarker': f.get('showMarker', False)
    })

config = {
    'title': title,
    'initialView': {
        'longitude': vp['center'][0],
        'latitude': vp['center'][1],
        'zoom': vp['zoom']
    },
    'showPopupOnClick': spec.get('showPopupOnClick', True),
    'layers': layers
}
os.makedirs('public/layers', exist_ok=True)
json.dump(config, open('public/layers/config.json', 'w'), ensure_ascii=False, indent=2)
print('Saved config.json with', len(layers), 'layers')
"
```

### STEP 3: ビルド
```bash
make fast-build
```

## レイヤースタイル推奨

| データ | emoji | color | outlineColor | showMarker |
|-------|-------|-------|--------------|-----------|
| USGS地震 | 🔴 | rgba(255,87,34,0.6) | #b71c1c | true |
| 武力紛争 | ⚔️ | rgba(255,152,0,0.6) | #e65100 | true |
| プレート境界 | 🌋 | rgba(156,39,176,0.8) | #4a148c | false |
| 海底ケーブル | 🔌 | rgba(33,150,243,0.8) | #0d47a1 | false |
