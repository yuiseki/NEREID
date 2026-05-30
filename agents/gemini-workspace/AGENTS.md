# NEREID Agent Workspace

このワークスペースはNEREIDの地図生成エージェント用の作業ディレクトリです。

## 重要: 環境制約

### ネットワーク
- **Overpass API**: `https://overpass.yuiseki.net/api/interpreter` のみ使用可能
  - `overpass-api.de`, `overpass.kumi.systems`, `overpass.openstreetmap.ru` は到達不可
  - 必ず `overpass.yuiseki.net` を使用すること

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
