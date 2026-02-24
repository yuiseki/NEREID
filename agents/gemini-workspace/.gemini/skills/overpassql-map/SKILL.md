---
name: overpassql-map
description: OpenStreetMapのデータを使って高品質な地図を生成する。ユーザーの日本語プロンプトを構造化→Overpass QL生成→データ取得→MapLibre可視化まで5ステップで実行する。
---

# Overpass QL Map スキル

## 概要

このスキルはユーザーの自然言語プロンプトからOpenStreetMapデータを取得し、MapLibreで美しい地図を生成する。
以下の**5ステップワークフロー**を必ず順番通りに実行すること。

---

## 必須ワークフロー（5ステップ）

### STEP 1: TridentIL 生成（プロンプトの構造化）

ユーザーのプロンプトを以下のTridentILフォーマットに変換する。

```
TitleOfMap: <地図のタイトル>
Area: <エリア（英語表記）>
AreaWithConcern: <エリア（英語表記）>, <施設種別（英語）>
EmojiForConcern: <施設種別>, <絵文字>
ColorForConcern: <施設種別>, <Web Safe Color名>
ShowPopupOnClick: true/false
```

**ルール:**
- エリアは必ず英語表記に変換する（下記「東京各区 name:en マッピング」参照）
- 病院を求められたら医院(Doctors)も必ず追加する
- 色分け要求があれば同じ施設に別々の色を割り当てる
- OSMに存在しないデータ（人気・口コミ・ランキング）は `No map specified.` を出力して停止

#### TridentIL 生成 サンプル例（テストケース対応）

```
入力: 東京都台東区のラーメン屋を表示してください。
出力:
TitleOfMap: 台東区のラーメン屋
Area: Taito, Tokyo
AreaWithConcern: Taito, Tokyo, Ramen shops
EmojiForConcern: Ramen shops, 🍜
ColorForConcern: Ramen shops, lightyellow
ShowPopupOnClick: false
```

```
入力: 東京都文京区の公園を表示してください。
出力:
TitleOfMap: 文京区の公園
Area: Bunkyō, Tokyo
AreaWithConcern: Bunkyō, Tokyo, Parks
EmojiForConcern: Parks, 🌳
ColorForConcern: Parks, lightgreen
ShowPopupOnClick: false
```

```
入力: 東京都渋谷区の図書館を表示してください。
出力:
TitleOfMap: 渋谷区の図書館
Area: Shibuya, Tokyo
AreaWithConcern: Shibuya, Tokyo, Libraries
EmojiForConcern: Libraries, 📚
ColorForConcern: Libraries, lightyellow
ShowPopupOnClick: false
```

```
入力: 東京都千代田区の交番を表示してください。
出力:
TitleOfMap: 千代田区の交番
Area: Chiyoda, Tokyo
AreaWithConcern: Chiyoda, Tokyo, Police boxes
EmojiForConcern: Police boxes, 👮
ColorForConcern: Police boxes, lightblue
ShowPopupOnClick: false
```

```
入力: 東京都港区の病院を表示してください。
出力:
TitleOfMap: 港区の病院
Area: Minato, Tokyo
AreaWithConcern: Minato, Tokyo, Hospitals
EmojiForConcern: Hospitals, 🏥
ColorForConcern: Hospitals, pink
AreaWithConcern: Minato, Tokyo, Doctors
EmojiForConcern: Doctors, 🩺
ColorForConcern: Doctors, lightpink
ShowPopupOnClick: false
```
※ 病院を求められたら医院(Doctors)も必ず追加する

```
入力: 東京都墨田区のコンビニを表示してください。
出力:
TitleOfMap: 墨田区のコンビニ
Area: Sumida, Tokyo
AreaWithConcern: Sumida, Tokyo, Convenience stores
EmojiForConcern: Convenience stores, 🏪
ColorForConcern: Convenience stores, lightyellow
ShowPopupOnClick: false
```

```
入力: 東京都新宿区の駅を表示してください。
出力:
TitleOfMap: 新宿区の駅
Area: Shinjuku, Tokyo
AreaWithConcern: Shinjuku, Tokyo, Railway stations
EmojiForConcern: Railway stations, 🚉
ColorForConcern: Railway stations, lightblue
ShowPopupOnClick: false
```

```
入力: 東京都台東区と東京都文京区の公園を色分けして表示してください。
出力:
TitleOfMap: 台東区と文京区の公園（色分け）
Area: Taito, Tokyo
Area: Bunkyō, Tokyo
AreaWithConcern: Taito, Tokyo, Parks in Taito
EmojiForConcern: Parks in Taito, 🌳
ColorForConcern: Parks in Taito, lightgreen
AreaWithConcern: Bunkyō, Tokyo, Parks in Bunkyo
EmojiForConcern: Parks in Bunkyo, 🌳
ColorForConcern: Parks in Bunkyo, lightyellow
ShowPopupOnClick: false
```

```
入力: 東京都台東区の公園を表示して、クリックで名前を表示してください。
出力:
TitleOfMap: 台東区の公園
Area: Taito, Tokyo
AreaWithConcern: Taito, Tokyo, Parks
EmojiForConcern: Parks, 🌳
ColorForConcern: Parks, lightgreen
ShowPopupOnClick: true
```

---

### STEP 2: Overpass QL クエリ生成

TridentIL の各 Area / AreaWithConcern に対して Overpass QL クエリを生成する。

**必須ルール:**
- timeout は必ず 30000
- 出力は必ず `out geom;`
- エリア指定には area specifier を使用
- 東京の区には必ず `area["name:en"="Tokyo"]->.outer;` を使ってネスト検索

#### Area クエリ（区の境界ポリゴン取得）

```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.searchArea;
(
  relation["boundary"="administrative"]["name:en"="Taito"](area.searchArea);
);
out geom;
```

#### AreaWithConcern クエリ（施設POI取得）

```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.inner;
(
  nwr["amenity"="restaurant"]["cuisine"="ramen"](area.inner)(area.outer);
);
out geom;
```

#### Overpass QL サンプル例（50例）

**国・広域エリア:**

Input: Area: Japan
```
[out:json][timeout:30000];
relation["boundary"="administrative"]["admin_level"=2]["name:en"="Japan"];
out geom;
```

Input: Area: Tokyo
```
[out:json][timeout:30000];
relation["boundary"="administrative"]["admin_level"=4]["name:en"="Tokyo"];
out geom;
```

Input: Area: New York City
```
[out:json][timeout:30000];
relation["boundary"="administrative"]["admin_level"=5]["name"="City of New York"];
out geom;
```

**東京都 各区 境界:**

Input: Area: Taito, Tokyo
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.searchArea;
(
  relation["boundary"="administrative"]["name:en"="Taito"](area.searchArea);
);
out geom;
```

Input: Area: Bunkyō, Tokyo
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.searchArea;
(
  relation["boundary"="administrative"]["name:en"="Bunkyō"](area.searchArea);
);
out geom;
```

Input: Area: Shibuya, Tokyo
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.searchArea;
(
  relation["boundary"="administrative"]["name:en"="Shibuya"](area.searchArea);
);
out geom;
```

Input: Area: Chiyoda, Tokyo
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.searchArea;
(
  relation["boundary"="administrative"]["name:en"="Chiyoda"](area.searchArea);
);
out geom;
```

Input: Area: Minato, Tokyo
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.searchArea;
(
  relation["boundary"="administrative"]["name:en"="Minato"](area.searchArea);
);
out geom;
```

Input: Area: Sumida, Tokyo
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.searchArea;
(
  relation["boundary"="administrative"]["name:en"="Sumida"](area.searchArea);
);
out geom;
```

Input: Area: Kōtō, Tokyo
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.searchArea;
(
  relation["boundary"="administrative"]["name:en"="Kōtō"](area.searchArea);
);
out geom;
```

Input: Area: Shinjuku, Tokyo
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.searchArea;
(
  relation["boundary"="administrative"]["name:en"="Shinjuku"](area.searchArea);
);
out geom;
```

Input: Area: Chūō, Tokyo
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.searchArea;
(
  relation["boundary"="administrative"]["admin_level"=7]["name:en"="Chūō"](area.searchArea);
);
out geom;
```

**飲食・グルメ:**

Input: AreaWithConcern: Taito, Tokyo, Ramen shops
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.inner;
(
  nwr["amenity"="restaurant"]["cuisine"="ramen"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Bunkyō, Tokyo, Ramen shops
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Bunkyō"]->.inner;
(
  nwr["amenity"="restaurant"]["cuisine"="ramen"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Taito, Tokyo, Soba noodle shops
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.inner;
(
  nwr["amenity"="restaurant"]["cuisine"="soba"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Taito, Tokyo, Pizza shops
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.inner;
(
  nwr["amenity"="fast_food"]["cuisine"="pizza"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Taito, Tokyo, Sushi shops
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.inner;
(
  nwr["amenity"="fast_food"]["cuisine"="sushi"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Taito, Tokyo, Izakaya
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.inner;
(
  nwr["amenity"="bar"](area.inner)(area.outer);
);
out geom;
```

**公共施設:**

Input: AreaWithConcern: Bunkyō, Tokyo, Parks
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Bunkyō"]->.inner;
(
  nwr["leisure"="park"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Taito, Tokyo, Parks
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.inner;
(
  nwr["leisure"="park"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Kōtō, Tokyo, Parks
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Kōtō"]->.inner;
(
  nwr["leisure"="park"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Shibuya, Tokyo, Libraries
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Shibuya"]->.inner;
(
  nwr["amenity"="library"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Chiyoda, Tokyo, Police boxes
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Chiyoda"]->.inner;
(
  nwr["amenity"="police"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Minato, Tokyo, Hospitals
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Minato"]->.inner;
(
  nwr["amenity"="hospital"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Minato, Tokyo, Doctors
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Minato"]->.inner;
(
  nwr["amenity"="doctors"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Sumida, Tokyo, Convenience stores
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Sumida"]->.inner;
(
  nwr["shop"="convenience"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Shinjuku, Tokyo, Railway stations
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Shinjuku"]->.inner;
(
  nwr["railway"="station"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Taito, Tokyo, Hotels
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.inner;
(
  nwr["tourism"="hotel"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Tokyo, University campuses
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.searchArea;
(
  nwr["amenity"="university"](area.searchArea);
);
out geom;
```

Input: AreaWithConcern: Taito, Tokyo, Factories
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.inner;
(
  nwr["landuse"="industrial"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Taito, Tokyo, Seven-Eleven
```
[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.inner;
(
  nwr["name"~"7-Eleven"](area.inner)(area.outer);
  nwr["name:en"~"7-Eleven"](area.inner)(area.outer);
);
out geom;
```

Input: AreaWithConcern: Kosovo, Embassies
```
[out:json][timeout:30000];
area["name:en"="Kosovo"]->.searchArea;
(
  nwr["office"="diplomatic"](area.searchArea);
);
out geom;
```

Input: AreaWithConcern: Sudan, Shelters
```
[out:json][timeout:30000];
area["name:en"="Sudan"]->.searchArea;
(
  nwr["amenity"="shelter"](area.searchArea);
  nwr["amenity"="refugee_site"](area.searchArea);
);
out geom;
```

Input: AreaWithConcern: New York City, UN facilities
```
[out:json][timeout:30000];
area["name"="City of New York"]->.searchArea;
(
  nwr["name"~"United Nations"]["building"="yes"](area.searchArea);
  nwr["name"~"United Nations"]["building:part"="yes"](area.searchArea);
);
out geom;
```

---

### STEP 3: データ取得

各クエリを実行し、GeoJSON形式で `public/layers/` 以下に保存する。

**ディレクトリ作成:**
```bash
mkdir -p public/layers
```

**osmable を使った取得（推奨）:**
```bash
osmable poi fetch --tag leisure=park --within "東京都台東区" --format geojson > public/layers/parks-taito.geojson
```

**複数チェーン・複数エリアの場合（コンビニ等）: 1エリア1クエリで全店取得→Python分割（必須最適化）**

複数チェーン（セブン・ファミマ・ローソン等）×複数エリアのクエリは、チェーン×エリア数だけ個別クエリを叩くと時間切れになる。
必ず「1エリアで全店取得」→「Pythonでチェーン別に分割」のパターンを使うこと：

```bash
# 1回のosmableで1エリアの全コンビニを取得
osmable poi fetch --tag shop=convenience --within "東京都台東区" --format geojson > /tmp/conv-taito.geojson
osmable poi fetch --tag shop=convenience --within "東京都文京区" --format geojson > /tmp/conv-bunkyo.geojson
osmable poi fetch --tag shop=convenience --within "東京都江東区" --format geojson > /tmp/conv-koto.geojson
```

```python
# split_convenience.py として保存して実行すること（ヒアドキュメントは使わない）
import json, os
os.makedirs("public/layers", exist_ok=True)
CHAINS = {"seven-eleven": ["セブン-イレブン", "セブンイレブン", "7-Eleven"], "familymart": ["ファミリーマート", "FamilyMart"], "lawson": ["ローソン", "Lawson"]}
WARDS = {"taito": "/tmp/conv-taito.geojson", "bunkyo": "/tmp/conv-bunkyo.geojson", "koto": "/tmp/conv-koto.geojson"}
for ward, ward_file in WARDS.items():
    all_data = json.load(open(ward_file))
    for chain_id, names in CHAINS.items():
        feats = [f for f in all_data["features"] if any(n in f.get("properties", {}).get("name", "") for n in names)]
        json.dump({"type": "FeatureCollection", "features": feats}, open(f"public/layers/{chain_id}-{ward}.geojson", "w"), ensure_ascii=False)
        print(f"{chain_id}-{ward}: {len(feats)}")
```

```bash
# Pythonスクリプトをファイルとして書き出して実行（ヒアドキュメント使用禁止）
# 上記のPythonコードをファイルに書き出して実行すること:
python3 /tmp/split_convenience.py
```

**curl + Overpass API（osmableが使えない/0件の場合）:**
```bash
QUERY='[out:json][timeout:30000];
area["name:en"="Tokyo"]->.outer;
area["name:en"="Taito"]->.inner;
(
  nwr["amenity"="restaurant"]["cuisine"="ramen"](area.inner)(area.outer);
);
out geom;'

curl -s --data-urlencode "data=${QUERY}" \
  https://overpass.yuiseki.net/api/interpreter \
  -o /tmp/overpass_raw.json

# osmtogeojson がある場合
osmtogeojson /tmp/overpass_raw.json > public/layers/ramen-taito.geojson

# osmtogeojson がない場合はPythonで変換
python3 -c "
import json, sys
data = json.load(open('/tmp/overpass_raw.json'))
features = []
for el in data.get('elements', []):
    props = el.get('tags', {})
    props['osm_id'] = el.get('id')
    props['osm_type'] = el.get('type')
    if el['type'] == 'node':
        feat = {'type': 'Feature', 'id': str(el['id']), 'geometry': {'type': 'Point', 'coordinates': [el['lon'], el['lat']]}, 'properties': props}
        features.append(feat)
    elif el['type'] in ['way', 'relation'] and 'geometry' in el:
        coords = [[p['lon'], p['lat']] for p in el['geometry']]
        if len(coords) >= 3 and coords[0] == coords[-1]:
            feat = {'type': 'Feature', 'id': str(el['id']), 'geometry': {'type': 'Polygon', 'coordinates': [coords]}, 'properties': props}
        else:
            feat = {'type': 'Feature', 'id': str(el['id']), 'geometry': {'type': 'LineString', 'coordinates': coords}, 'properties': props}
        features.append(feat)
print(json.dumps({'type': 'FeatureCollection', 'features': features}, ensure_ascii=False))
" > public/layers/ramen-taito.geojson
```

**必ずフィーチャ数を確認すること:**
```bash
python3 -c "import json; d=json.load(open('public/layers/ramen-taito.geojson')); print(f'Features: {len(d[\"features\"])}')"
```
**features が 0 の場合は別のクエリで必ず再試行すること。**

---

### STEP 4: config.json 更新

`public/layers/config.json` を以下のフォーマットで書き込む。
**App.tsx はこのファイルを自動読み込みするので、App.tsx は変更不要。**

```json
{
  "title": "台東区のラーメン屋",
  "initialView": {
    "longitude": 139.7850,
    "latitude": 35.7126,
    "zoom": 14
  },
  "showPopupOnClick": false,
  "layers": [
    {
      "id": "area-taito",
      "name": "台東区",
      "file": "./layers/area-taito.geojson",
      "emoji": "📍",
      "color": "rgba(173, 216, 230, 0.3)",
      "outlineColor": "#6495ED",
      "showMarker": false
    },
    {
      "id": "ramen-taito",
      "name": "ラーメン屋",
      "file": "./layers/ramen-taito.geojson",
      "emoji": "🍜",
      "color": "lightyellow",
      "outlineColor": "#ccaa00",
      "showMarker": true
    }
  ]
}
```

**フィールド説明:**
- `title`: 地図のタイトル
- `initialView`: 初期表示位置（対象エリアの中心座標）
- `showPopupOnClick`: trueならクリックでfeature名ポップアップを表示
- `layers[].id`: レイヤーID（ユニークな英小文字+ハイフン）
- `layers[].name`: レイヤー表示名
- `layers[].file`: GeoJSONファイルパス（`./layers/` 相対パス）
- `layers[].emoji`: 絵文字マーカー（Pointおよびpolygon centroidに表示）
- `layers[].color`: フィルカラー（Web Safe Color名 または rgba()）
- `layers[].outlineColor`: アウトラインカラー（#hex）
- `layers[].showMarker`: trueならPolygon/MultiPolygon centroidにも絵文字マーカーを表示

---

### STEP 5: ビルド

JS/CSSはイメージ内で事前ビルド済みのため、データファイルのみデプロイする高速ビルドを使用すること：

```bash
make fast-build
```

`make fast-build` は `public/layers/` の内容を `./layers/` にコピーするだけで完了する。
`make build`（フルビルド）は使用しないこと。フルビルドは数分かかりタイムアウトの原因になる。

---

## OSM タグ知識ベース（重要ヒント集）

### ⚠️ よくある間違い（必ず確認）

| 施設 | ✅ 正しいタグ | ❌ 間違い |
|------|------------|---------|
| ラーメン屋 | `amenity=restaurant cuisine=ramen` | `cuisine=noodle` のみ |
| 工場 | `landuse=industrial` | `landuse=factory`（存在しない） |
| 居酒屋 | `amenity=bar` | `amenity=izakaya`（存在しない） |
| ピザ屋 | `amenity=fast_food cuisine=pizza` | `amenity=restaurant` |
| 寿司屋 | `amenity=fast_food cuisine=sushi` | `amenity=restaurant` |
| 神社 | `amenity=place_of_worship religion=shinto` | `amenity=shrine`（存在しない） |
| 寺院 | `amenity=place_of_worship religion=buddhist` | `religion=buddhism`（wrong） |
| 公園 | `leisure=park` | `amenity=park`（存在しない） |
| 交番 | `amenity=police` | `amenity=koban`（存在しない） |
| コンビニ | `shop=convenience` | `amenity=convenience` |
| 駅 | `railway=station` | `amenity=station` |
| 病院 | `amenity=hospital` + **必ず** `amenity=doctors` も追加 | hospital のみ |

### 施設タイプ別 OSM タグ

```
Church: nwr["building"="church"]
Mosque: nwr["building"="mosque"]
Shrine: nwr["amenity"="place_of_worship"]["religion"="shinto"]
Temple: nwr["amenity"="place_of_worship"]["religion"="buddhist"]
  ※ "religion"="buddhism" は間違い。必ず "buddhist" を使う

Factories: nwr["landuse"="industrial"]
  ※ "landuse"="factory" は存在しない

Izakaya: nwr["amenity"="bar"]
  ※ 居酒屋専用タグは存在しない

Pizza shops: nwr["amenity"="fast_food"]["cuisine"="pizza"]
  ※ ピザ屋はrestaurantではなくfast_food

Sushi shops: nwr["amenity"="fast_food"]["cuisine"="sushi"]
  ※ 寿司屋もfast_food

Ramen shops: nwr["amenity"="restaurant"]["cuisine"="ramen"]
Soba shops: nwr["amenity"="restaurant"]["cuisine"="soba"]
Udon shops: nwr["amenity"="restaurant"]["cuisine"="udon"]
Curry shops: nwr["amenity"="restaurant"]["cuisine"="curry"]

Parks: nwr["leisure"="park"]
  ※ amenity=park は存在しない

Libraries: nwr["amenity"="library"]
Police boxes (交番): nwr["amenity"="police"]
Hospitals: nwr["amenity"="hospital"]
Doctors (医院): nwr["amenity"="doctors"]
  ※ 病院を求められたら必ず doctors も追加

Convenience stores: nwr["shop"="convenience"]
Supermarkets: nwr["shop"="supermarket"]
Railway stations: nwr["railway"="station"]
Bus stops: nwr["highway"="bus_stop"]
Hotels: nwr["tourism"="hotel"]
Cafes: nwr["amenity"="cafe"]
Bars: nwr["amenity"="bar"]
Restaurants: nwr["amenity"="restaurant"]
Fast food: nwr["amenity"="fast_food"]
Banks: nwr["amenity"="bank"]
ATMs: nwr["amenity"="atm"]
Pharmacies: nwr["amenity"="pharmacy"]
Schools: nwr["amenity"="school"]
Universities: nwr["amenity"="university"]
Kindergartens: nwr["amenity"="kindergarten"]
Post offices: nwr["amenity"="post_office"]
Toilets: nwr["amenity"="toilets"]
Parking: nwr["amenity"="parking"]
Embassies: nwr["office"="diplomatic"]
Military: nwr["landuse"="military"]
Shelters: nwr["amenity"="shelter"] + nwr["amenity"="refugee_site"]
Castles: nwr["historic"="castle"]
National treasure castles: nwr["historic"="castle"]["heritage"]
```

---

## 東京各区 name:en マッピング

| 日本語 | name:en（Overpass で使用） |
|--------|--------------------------|
| 台東区 | Taito |
| 文京区 | Bunkyō |
| 渋谷区 | Shibuya |
| 千代田区 | Chiyoda |
| 港区 | Minato |
| 墨田区 | Sumida |
| 江東区 | Kōtō |
| 新宿区 | Shinjuku |
| 中央区 | Chūō |
| 豊島区 | Toshima |
| 品川区 | Shinagawa |
| 目黒区 | Meguro |
| 世田谷区 | Setagaya |
| 杉並区 | Suginami |
| 中野区 | Nakano |
| 練馬区 | Nerima |
| 板橋区 | Itabashi |
| 北区 | Kita |
| 荒川区 | Arakawa |
| 足立区 | Adachi |
| 葛飾区 | Katsushika |
| 江戸川区 | Edogawa |
| 大田区 | Ōta |

---

## 絵文字・カラー推奨対応表

| 施設 | 絵文字 | color | outlineColor |
|------|--------|-------|-------------|
| ラーメン屋 | 🍜 | lightyellow | #ccaa00 |
| 蕎麦屋 | 🍜 | lightgreen | #4caf50 |
| 公園 | 🌳 | lightgreen | #4caf50 |
| 病院 | 🏥 | pink | #e91e63 |
| 医院 | 🩺 | lightpink | #f48fb1 |
| 図書館 | 📚 | lightyellow | #ccaa00 |
| 交番 | 👮 | lightblue | #2196f3 |
| コンビニ | 🏪 | lightyellow | #ccaa00 |
| 駅 | 🚉 | lightblue | #2196f3 |
| レストラン | 🍴 | pink | #e91e63 |
| カフェ | ☕️ | #d7ccc8 | #795548 |
| バー/居酒屋 | 🍻 | yellow | #ffeb3b |
| ホテル | 🏨 | lightblue | #2196f3 |
| 大学 | 🎓 | lightyellow | #ccaa00 |
| 寺院 | 🛕 | lightyellow | #ccaa00 |
| 神社 | ⛩ | lightgreen | #4caf50 |
| 城 | 🏯 | white | #9e9e9e |
| 大使館 | 🏢 | lightblue | #2196f3 |
| 軍事施設 | 🪖 | yellow | #ffeb3b |
| 避難所 | 🏕 | lightgreen | #4caf50 |
| 川 | 🏞 | lightblue | #2196f3 |
| UN施設 | 🇺🇳 | lightblue | #2196f3 |

---

## 初期表示座標（東京各区）

| 区 | longitude | latitude | zoom |
|----|-----------|----------|------|
| 台東区 | 139.7850 | 35.7126 | 14 |
| 文京区 | 139.7522 | 35.7078 | 14 |
| 渋谷区 | 139.6980 | 35.6627 | 13 |
| 千代田区 | 139.7534 | 35.6938 | 14 |
| 港区 | 139.7514 | 35.6580 | 13 |
| 墨田区 | 139.8141 | 35.7101 | 14 |
| 江東区 | 139.8172 | 35.6722 | 13 |
| 新宿区 | 139.7035 | 35.6940 | 13 |
| 中央区 | 139.7726 | 35.6712 | 14 |
| 豊島区 | 139.7197 | 35.7280 | 14 |
| 品川区 | 139.7300 | 35.6093 | 13 |

---

## エラーハンドリング

1. **GeoJSON features が 0**: 別のタグでクエリを再試行。`name:en` を `name` に変更して再試行。
2. **osmable 失敗**: curl + Overpass API に切り替える。
3. **make fast-build エラー**: `public/layers/` ディレクトリが存在するか確認してから再実行。
4. **Overpass timeout**: クエリを絞り込むか、エリアを小さくする。
5. **config.json が読めない**: `public/layers/` ディレクトリが存在するか確認。`mkdir -p public/layers` を実行。
