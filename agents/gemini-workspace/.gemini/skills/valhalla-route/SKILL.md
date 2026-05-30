---
name: valhalla-route
description: Valhalla ルーティング API を使って出発地→目的地のルートを GeoJSON LineString に変換し MapLibre で可視化する。「AからBまで歩く/車で行く/自転車で行く」ルート表示リクエストに使う。
---

# Valhalla Route スキル

## 環境制約
- Valhalla エンドポイント: `https://valhalla.yuiseki.net/route`（POST, JSON）
- Nominatim: `https://nominatim.yuiseki.net/search.php?q=<地名>&format=json`（座標取得）
- ビルド: **`make fast-build`** のみ（`make build` 禁止）

## ワークフロー（4ステップ）

### STEP 1: 出発地・目的地の座標取得

`legacy-work-spec.json` の `routing.from` / `routing.to` から地名を読む。**座標取得は Python スクリプトで行う**（日本語URL エンコーディングの問題を回避):

```python
python3 << 'EOF'
import urllib.request, urllib.parse, json

def geocode(place):
    url = "https://nominatim.yuiseki.net/search.php?" + urllib.parse.urlencode({
        "q": place, "format": "json", "limit": "1"
    })
    req = urllib.request.Request(url, headers={"User-Agent": "NereidMap/1.0"})
    with urllib.request.urlopen(req, timeout=10) as r:
        results = json.loads(r.read())
    if not results:
        raise ValueError(f"Nominatim: no results for '{place}'")
    return float(results[0]["lat"]), float(results[0]["lon"])

from_lat, from_lon = geocode("東京駅")
to_lat, to_lon   = geocode("上野駅")
print(f"FROM: {from_lat},{from_lon}")
print(f"TO:   {to_lat},{to_lon}")
EOF
```

コスティング選択:
- 徒歩・歩く → `pedestrian`
- 車・自動車 → `auto`
- 自転車 → `bicycle`

### STEP 2: Valhalla API 呼び出し

```bash
curl -s https://valhalla.yuiseki.net/route \
  -H "Content-Type: application/json" \
  -d "{
    \"locations\": [
      {\"lat\": $FROM_LAT, \"lon\": $FROM_LON},
      {\"lat\": $TO_LAT, \"lon\": $TO_LON}
    ],
    \"costing\": \"pedestrian\",
    \"units\": \"meters\"
  }" -o /tmp/valhalla_response.json

# 確認
python3 -c "
import json
d = json.load(open('/tmp/valhalla_response.json'))
s = d['trip']['summary']
print(f'距離: {s[\"length\"]*1000:.0f}m, 所要時間: {s[\"time\"]:.0f}秒')
"
```

### STEP 3: shape デコード → GeoJSON 変換

Valhalla は precision=6 の encoded polyline を返す。Python で GeoJSON LineString に変換:

```python
python3 -c "
import json

def decode_polyline6(encoded):
    coords = []
    index = lat = lng = 0
    while index < len(encoded):
        for is_lat in (True, False):
            shift = result = 0
            while True:
                b = ord(encoded[index]) - 63
                index += 1
                result |= (b & 0x1f) << shift
                shift += 5
                if b < 0x20: break
            delta = ~(result >> 1) if (result & 1) else (result >> 1)
            if is_lat: lat += delta
            else: lng += delta
        coords.append([lng / 1e6, lat / 1e6])
    return coords

d = json.load(open('/tmp/valhalla_response.json'))
shape = d['trip']['legs'][0]['shape']
coords = decode_polyline6(shape)
summary = d['trip']['summary']

geojson = {
    'type': 'FeatureCollection',
    'features': [{
        'type': 'Feature',
        'geometry': {'type': 'LineString', 'coordinates': coords},
        'properties': {
            'distance_m': round(summary['length'] * 1000),
            'time_s': round(summary['time']),
            'costing': 'pedestrian'
        }
    }]
}
import os
os.makedirs('public/layers', exist_ok=True)
with open('public/layers/route.geojson', 'w') as f:
    json.dump(geojson, f, ensure_ascii=False)
print(f'Route saved: {len(coords)} points, {round(summary[\"length\"]*1000)}m')
"
```

### STEP 4: config.json 更新 + ビルド

```bash
# ルートの中間点を中心座標として使用
python3 -c "
import json
gj = json.load(open('public/layers/route.geojson'))
coords = gj['features'][0]['geometry']['coordinates']
mid = coords[len(coords)//2]
props = gj['features'][0]['properties']
dist_km = props['distance_m'] / 1000

config = {
    'title': f'ルート ({dist_km:.1f}km)',
    'initialView': {'longitude': mid[0], 'latitude': mid[1], 'zoom': 14},
    'showPopupOnClick': False,
    'layers': [
        {
            'id': 'route',
            'name': 'ルート',
            'file': './layers/route.geojson',
            'emoji': '🚶',
            'color': '#2196f3',
            'outlineColor': '#0d47a1',
            'showMarker': False
        }
    ]
}
import os
os.makedirs('public/layers', exist_ok=True)
with open('public/layers/config.json', 'w') as f:
    json.dump(config, f, ensure_ascii=False, indent=2)
print('config.json saved')
"

make fast-build
```

## コスティング別絵文字・カラー

| 移動手段 | costing | emoji | color | outlineColor |
|---------|---------|-------|-------|--------------|
| 徒歩 | pedestrian | 🚶 | #bbdefb | #0d47a1 |
| 自転車 | bicycle | 🚴 | #c8e6c9 | #1b5e20 |
| 車 | auto | 🚗 | #ffccbc | #bf360c |

## エラーハンドリング

1. **Nominatim 結果が 0 件**: クエリを英語名に変更（例:「東京駅」→「Tokyo Station」）
2. **Valhalla が空レスポンス**: `echo $?` でステータス確認、座標の lat/lon が逆でないか確認
3. **make fast-build エラー**: `mkdir -p public/layers` を実行してから再試行
