---
name: overpassql-map
description: OpenStreetMapのデータを使って高品質な地図を生成する。ユーザーの日本語プロンプトを構造化→Overpass QL生成→データ取得→MapLibre可視化まで5ステップで実行する。
---

# Overpass QL Map スキル

## 環境制約（必ず守ること）

- Overpassエンドポイント: **`https://overpass.yuiseki.net/api/interpreter`** のみ使用可能
- データ取得ツール: **`osmcli`**（`osmable` は存在しない）
- ビルド: **`make fast-build`** のみ使用（`make build` は使用禁止）

---

## 必須ワークフロー（5ステップ）

### STEP 1: TridentIL 生成（プロンプトの構造化）

```
TitleOfMap: <地図のタイトル>
Area: <エリア（英語表記）>
AreaWithConcern: <エリア（英語表記）>, <施設種別（英語）>
EmojiForConcern: <施設種別>, <絵文字>
ColorForConcern: <施設種別>, <Web Safe Color名>
ShowPopupOnClick: true/false
```

- エリアは英語表記に変換（台東区→Taito, 文京区→Bunkyo, 渋谷区→Shibuya, 千代田区→Chiyoda, 港区→Minato, 墨田区→Sumida, 江東区→Koto, 新宿区→Shinjuku, 中央区→Chuo）
- 病院を求められたら医院(Doctors)も必ず追加
- OSMに存在しないデータは `No map specified.` を出力して停止

### STEP 2: Overpass QL クエリ生成

- timeout は必ず 30000
- 出力は必ず `out geom;`
- 東京の区には `area["name:en"="Tokyo"]->.outer;` でスコープを絞ってネスト検索
- 区の英語表記: 台東区=Taito, 文京区=Bunkyo, 渋谷区=Shibuya, 千代田区=Chiyoda, 港区=Minato, 墨田区=Sumida, 江東区=Koto, 新宿区=Shinjuku, 中央区=Chuo（**長音符なし**で OSM に一致）

**単一エリア:**
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.inner;
(
  nwr["amenity"="cafe"](area.inner)(area.outer);
);
out geom;
```

**複数エリア（union パターン）— エリアごとに `.a1`, `.a2` とバインドしてまとめる:**
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.a1;
area["name:en"="Bunkyo"]->.a2;
(
  nwr["amenity"="hospital"](area.a1)(area.outer);
  nwr["amenity"="hospital"](area.a2)(area.outer);
);
out geom;
```
→ **クエリとファイルはタグ単位で分ける**（hospital 用1本 + doctors 用1本 = 2クエリ、出力は hospital-taito/hospital-bunkyo/doctors-taito/doctors-bunkyo の4ファイル）:
```
# doctors 用クエリ（hospital クエリと同パターンで amenity を変えるだけ）
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.a1;
area["name:en"="Bunkyo"]->.a2;
(
  nwr["amenity"="doctors"](area.a1)(area.outer);
  nwr["amenity"="doctors"](area.a2)(area.outer);
);
out geom;
```
→ 1クエリの結果を Python で `properties.amenity` によりエリア別にファイルに書き分けても可

### STEP 3: データ取得

**osmcli を使った取得（推奨）:**
```bash
osmcli poi fetch --tag amenity=cafe --within "東京都台東区" --format geojson > public/layers/cafe-taito.geojson
```

**osmcli 失敗時 — Nominatim で osm_id を取得して Overpass クエリを確実化:**
```bash
# 1. Nominatim で区の osm_id を取得（name:en に依存せず確実）
NOM=$(curl -s "https://nominatim.yuiseki.net/search.php?q=東京都台東区&format=json&limit=1")
OSM_ID=$(echo "$NOM" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d[0]['osm_id'])")
AREA_ID=$((3600000000 + OSM_ID))
echo "台東区 area_id: $AREA_ID"

# 2. Overpass クエリで area(AREA_ID) を使用（name:en 不要）
QUERY="[out:json][timeout:30000];area($AREA_ID);(nwr[\"amenity\"=\"cafe\"](area););out geom;"
curl -s "https://overpass.yuiseki.net/api/interpreter?data=$(python3 -c \"import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))\" \"$QUERY\")" -o /tmp/overpass_raw.json
python3 -c "
import json
data = json.load(open('/tmp/overpass_raw.json'))
features = []
for el in data.get('elements', []):
    props = el.get('tags', {}); props['osm_id'] = el.get('id')
    if el['type'] == 'node':
        features.append({'type':'Feature','geometry':{'type':'Point','coordinates':[el['lon'],el['lat']]},'properties':props})
    elif el['type'] in ['way','relation'] and 'geometry' in el:
        coords = [[p['lon'],p['lat']] for p in el['geometry']]
        geom = {'type':'Polygon','coordinates':[coords]} if len(coords)>=3 and coords[0]==coords[-1] else {'type':'LineString','coordinates':coords}
        features.append({'type':'Feature','geometry':geom,'properties':props})
print(json.dumps({'type':'FeatureCollection','features':features},ensure_ascii=False))
" > public/layers/cafe-taito.geojson
```

**curl + Overpass API（osmcliが使えない/0件の場合）:**
```bash
QUERY='[out:json][timeout:30000];area["name:en"="Tokyo"]->.outer;area["name:en"="Taito"]->.inner;(nwr["amenity"="cafe"](area.inner)(area.outer););out geom;'
curl -s --data-urlencode "data=${QUERY}" https://overpass.yuiseki.net/api/interpreter -o /tmp/overpass_raw.json
# osmtogeojson がない場合はPythonで変換:
python3 -c "
import json, sys
data = json.load(open('/tmp/overpass_raw.json'))
features = []
for el in data.get('elements', []):
    props = el.get('tags', {}); props['osm_id'] = el.get('id'); props['osm_type'] = el.get('type')
    if el['type'] == 'node':
        features.append({'type':'Feature','id':str(el['id']),'geometry':{'type':'Point','coordinates':[el['lon'],el['lat']]},'properties':props})
    elif el['type'] in ['way','relation'] and 'geometry' in el:
        coords = [[p['lon'],p['lat']] for p in el['geometry']]
        geom = {'type':'Polygon','coordinates':[coords]} if len(coords)>=3 and coords[0]==coords[-1] else {'type':'LineString','coordinates':coords}
        features.append({'type':'Feature','id':str(el['id']),'geometry':geom,'properties':props})
print(json.dumps({'type':'FeatureCollection','features':features},ensure_ascii=False))
" > public/layers/cafe-taito.geojson
```

**フィーチャ数確認（0件の場合は別タグで再試行）:**
```bash
python3 -c "import json; d=json.load(open('public/layers/cafe-taito.geojson')); print(f'Features: {len(d[\"features\"])}')"
```

**区境界ポリゴン取得（area-*.geojson）:**
```bash
# osmcli で区境界を取得
osmcli poi fetch --tag boundary=administrative --within "東京都台東区" --format geojson > public/layers/area-taito.geojson
# 0件またはエラーの場合は Overpass で relation を取得
QUERY='[out:json][timeout:30000];relation["boundary"="administrative"]["admin_level"="7"]["name"="台東区"];out geom;'
curl -s --data-urlencode "data=${QUERY}" https://overpass.yuiseki.net/api/interpreter -o /tmp/area_raw.json
python3 -c "
import json
data = json.load(open('/tmp/area_raw.json'))
features = []
for el in data.get('elements', []):
    props = {'name': el.get('tags', {}).get('name', ''), 'osm_id': el.get('id')}
    if el.get('type') in ['way','relation'] and 'geometry' in el:
        coords = [[p['lon'],p['lat']] for p in el['geometry']]
        geom = {'type':'Polygon','coordinates':[coords]} if len(coords)>=3 and coords[0]==coords[-1] else {'type':'LineString','coordinates':coords}
        features.append({'type':'Feature','geometry':geom,'properties':props})
print(json.dumps({'type':'FeatureCollection','features':features},ensure_ascii=False))
" > public/layers/area-taito.geojson
```

### STEP 4: config.json 更新

`public/layers/config.json` を以下フォーマットで書き込む（App.tsx は変更不要）:

**`showPopupOnClick`**: 施設名・住所などの詳細を確認したい POI（病院・店舗・駅）は `true`、エリア境界のみで詳細不要なら `false`。

**複数エリアの initialView**: 各エリアの代表座標を平均し、ズームは 12〜13 を使用（単一エリアは 14）:

```json
{
  "title": "台東区のカフェ",
  "initialView": {"longitude": 139.7850, "latitude": 35.7126, "zoom": 14},
  "showPopupOnClick": true,
  "layers": [
    {
      "id": "area-taito", "name": "台東区",
      "file": "./layers/area-taito.geojson",
      "emoji": "📍", "color": "rgba(173,216,230,0.3)", "outlineColor": "#6495ED", "showMarker": false
    },
    {
      "id": "cafe-taito", "name": "カフェ",
      "file": "./layers/cafe-taito.geojson",
      "emoji": "☕️", "color": "#d7ccc8", "outlineColor": "#795548", "showMarker": true
    }
  ]
}
```

### STEP 5: ビルド

```bash
make fast-build
```

---

## OSM タグ早見表

| 施設 | タグ |
|------|------|
| カフェ | `amenity=cafe` |
| ラーメン屋 | `amenity=restaurant cuisine=ramen` |
| 公園 | `leisure=park` |
| 病院 | `amenity=hospital`（＋必ず `amenity=doctors` も追加） |
| 図書館 | `amenity=library` |
| 交番 | `amenity=police` |
| コンビニ | `shop=convenience` |
| 駅 | `railway=station` |
| 神社 | `amenity=place_of_worship religion=shinto` |
| 寺院 | `amenity=place_of_worship religion=buddhist` |
| ホテル | `tourism=hotel` |
| 大学 | `amenity=university` |

## 絵文字・カラー推奨

| 施設 | emoji | color | outlineColor |
|------|-------|-------|--------------|
| カフェ | ☕️ | #d7ccc8 | #795548 |
| ラーメン屋 | 🍜 | lightyellow | #ccaa00 |
| 公園 | 🌳 | lightgreen | #4caf50 |
| 病院 | 🏥 | pink | #e91e63 |
| 医院 | 🩺 | lightpink | #f48fb1 |
| 図書館 | 📚 | lightyellow | #ccaa00 |
| 交番 | 👮 | lightblue | #2196f3 |
| コンビニ | 🏪 | lightyellow | #ccaa00 |
| 駅 | 🚉 | lightblue | #2196f3 |

## エラーハンドリング

1. **features が 0**: 別タグで再試行。`name:en` を `name` に変更して再試行
2. **osmcli 失敗**: curl + Overpass API (`overpass.yuiseki.net`) に切り替え
3. **make fast-build エラー**: `mkdir -p public/layers` を実行してから再試行
