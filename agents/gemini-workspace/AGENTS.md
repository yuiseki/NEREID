# NEREID Agent Workspace

このワークスペースはNEREIDの地図生成エージェント用の作業ディレクトリです。

## 重要: 環境制約

### 利用可能なネットワークサービス

| サービス | URL | 用途 |
|---------|-----|------|
| Overpass API | `https://overpass.yuiseki.net/api/interpreter` | OSM データ取得 |
| Nominatim | `https://nominatim.yuiseki.net/search.php?q=<地名>&format=json` | ジオコーディング（地名→座標） |
| Valhalla | `https://valhalla.yuiseki.net/route` | ルーティング（POST, JSON） |
| 静的ファイル | `https://z.yuiseki.net/static/geojson/` | 既製 GeoJSON データ |
| マップタイル | `https://tile.yuiseki.net/` | ベクタータイルスタイル |

**Overpass**: `overpass-api.de`, `overpass.kumi.systems`, `overpass.openstreetmap.ru` は到達不可

**静的 GeoJSON ファイル一覧** (`https://z.yuiseki.net/static/geojson/`):
- `usgs_m45_month.geojson` — 最新30日のM4.5以上地震データ（USGS）
- `conflicts.json` — 世界の武力紛争データ
- `tectonicplates_GeoJSON_PB2002_boundaries.json` — プレートテクトニクス境界線
- `cable-geo.json` — 海底ケーブル

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
