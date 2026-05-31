# NEREID Agent Workspace

このワークスペースはNEREIDの地図生成エージェント用の作業ディレクトリです。

## 重要: 環境制約

### 利用可能なネットワークサービス

| サービス | URL | 用途 | カバレッジ |
|---------|-----|------|----------|
| Overpass API | `https://overpass.yuiseki.net/api/interpreter` | OSM データ取得 | **全世界** (planet) |
| Nominatim | `https://nominatim.yuiseki.net/search.php?q=<地名>&format=json` | ジオコーディング（地名→座標） | **全世界** (planet) |
| Valhalla | `https://valhalla.yuiseki.net/route` | ルーティング（POST, JSON） | **全世界** (planet) |
| 静的ファイル | `https://z.yuiseki.net/static/geojson/` | 既製 GeoJSON データ | 世界 |
| マップタイル | `https://tile.yuiseki.net/` | ベクタータイルスタイル | 全世界 |

全サービスは `osm-planet-in-da-house` (3TB planet データ) によりグローバルカバレッジ。日本国内だけでなく世界中の地名・ルート・POI を扱える。

**Overpass**: `overpass-api.de`, `overpass.kumi.systems`, `overpass.openstreetmap.ru` は到達不可

**静的ファイル一覧** (`https://z.yuiseki.net/static/`):

| パス | 内容 | 形式 |
|-----|------|------|
| `geojson/usgs_m45_month.geojson` | 最新30日 M4.5以上地震 (USGS) | GeoJSON Point |
| `geojson/conflicts.json` | 世界の武力紛争 | GeoJSON |
| `geojson/tectonicplates_GeoJSON_PB2002_boundaries.json` | プレートテクトニクス境界線 | GeoJSON LineString |
| `geojson/cable-geo.json` | 海底ケーブル | GeoJSON LineString |
| `ucdp/GEDEvent_v25_1.csv` | UCDP 紛争イベント 463k件 (1989-2024) | CSV |
| `overture/overture.pmtiles` | Overture Maps (建物・道路・POI) 56GB | **PMTiles** |

**PMTiles データ** (`pmtiles://` プロトコルで MapLibre から直接参照):

| データセット | PMTiles URL | source-layer | 内容 |
|------------|-------------|-------------|------|
| Kontur 人口密度 | `pmtiles://https://data.source.coop/smartmaps/foil4gr1/kpop.pmtiles` | `kpop` | 400m H3 hexの人口 |
| Google Open Buildings | `pmtiles://https://data.source.coop/cholmes/google-open-buildings/google-open-buildings.pmtiles` | `buildings` | 全世界建物フットプリント |
| OpenCelliD | `pmtiles://https://data.source.coop/smartmaps/opencellid/cellid.pmtiles` | `a` | 携帯基地局位置 |
| Overture Maps | `pmtiles://https://z.yuiseki.net/static/overture/overture.pmtiles` | `building`, `transportation` | 建物・道路・POI |
| UCDP 武力紛争 | `pmtiles://https://data.source.coop/smartmaps/uppsala-conflict/a.pmtiles` | `event` | 武力衝突イベント |

**Overture STAC** (GeoParquet → DuckDB で spatial query 可能):
- カタログ: `https://stac.overturemaps.org/2026-05-20.0/`
- テーマ: `buildings/building`, `places/place`, `divisions/division_area`, `transportation/segment`
- k8s service: `http://buildings-cng.yuiseki.com/tiles/{z}/{x}/{y}.mvt` — Overture buildings MVT

**HOTOSM 衛星画像 STAC**:
- 検索 API: `POST https://api.imagery.hotosm.org/stac/search` — bbox で衛星画像 COG を検索
- k8s service: `http://hotosm-imagery-tile.yuiseki.com/tiles/{z}/{x}/{y}.png` — 衛星画像タイル

**Valhalla ルーティング例**:
```bash
curl -s https://valhalla.yuiseki.net/route \
  -H "Content-Type: application/json" \
  -d '{"locations":[{"lat":35.6812,"lon":139.7671},{"lat":35.7100,"lon":139.8107}],
       "costing":"pedestrian","units":"meters"}'
```
コスティング: `auto`（車）、`pedestrian`（徒歩）、`bicycle`（自転車）

### コマンド
- Overpassデータ取得: **`osmcli`**（`osmable` コマンドは存在しない）
- ビルド: **`make fast-build`**（`make build` は使用禁止、数分かかりタイムアウトする）

## ワークスペース構造

```
./
├── src/App.tsx          - MapLibre GLマップアプリ（変更不要）
├── public/layers/       - GeoJSONデータ保存先
│   └── config.json      - レイヤー設定（必ずこれを更新）
├── Makefile             - make fast-build で public/layers/ → ./layers/ をコピー
└── .opencode/skills/    - 利用可能なスキル一覧
```

## 作業の基本方針

1. `legacy-work-spec.json` と `legacy-kind-prompt.txt` を読む
2. 適切なスキル（`.opencode/skills/` 内）を選択して実行する
3. `public/layers/config.json` を更新する
4. `make fast-build` でビルドする
5. `index.html` が生成されていることを確認する

## osmcli の使い方

```bash
# 基本的なPOI取得
osmcli poi fetch --tag amenity=cafe --within "東京都台東区" --format geojson > public/layers/cafe-taito.geojson

# タグ形式: key=value
osmcli poi fetch --tag leisure=park --within "東京都文京区" --format geojson > public/layers/parks-bunkyo.geojson
```

osmcliが失敗した場合は curl + `overpass.yuiseki.net` に切り替えること。
