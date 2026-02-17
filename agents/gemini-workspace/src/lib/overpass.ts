import osmtogeojson from "osmtogeojson";

/**
 * Overpass API のエンドポイント
 * NEREID 用: overpass.yuiseki.net
 * フォールバック: z.overpass-api.de
 */
export const OVERPASS_ENDPOINT =
    "https://overpass.yuiseki.net/api/interpreter";

/**
 * Overpass API から取得した結果を GeoJSON に変換する SWR 用フェッチャー
 * create-free-map-asap パターン
 *
 * 使い方:
 * ```tsx
 * import useSWR from "swr";
 * import { overpassGeoJsonFetcher, buildOverpassUrl } from "../lib/overpass";
 *
 * const url = buildOverpassUrl('leisure=park', '東京都台東区');
 * const { data: geoJson } = useSWR(url, overpassGeoJsonFetcher);
 * ```
 */
export const overpassGeoJsonFetcher = async (
    url: string
): Promise<GeoJSON.FeatureCollection> => {
    const res = await fetch(url);

    if (!res.ok) {
        throw new Error(
            `Overpass API error: ${res.status} ${res.statusText}`
        );
    }

    const json = await res.json();
    return osmtogeojson(json) as GeoJSON.FeatureCollection;
};

/**
 * Overpass QL クエリ URL を構築する
 *
 * @param tag - OSM タグ (例: "leisure=park", "amenity=restaurant")
 * @param area - エリア名 (例: "東京都台東区")
 * @param options - タイムアウトなどのオプション
 */
export function buildOverpassUrl(
    tag: string,
    area: string,
    options?: { timeout?: number; nameKey?: string }
): string {
    const timeout = options?.timeout ?? 30000;
    const nameKey = options?.nameKey ?? "name";
    const [key, value] = tag.split("=", 2);

    const query = `
[out:json][timeout:${timeout}];
area["${nameKey}"="${area}"]->.searchArea;
(
  nwr["${key}"="${value}"](area.searchArea);
);
out geom;
`;

    return `${OVERPASS_ENDPOINT}?data=${encodeURIComponent(query)}`;
}

/**
 * Overpass QL クエリ URL を直接構築する（高度なクエリ用）
 */
export function buildRawOverpassUrl(query: string): string {
    return `${OVERPASS_ENDPOINT}?data=${encodeURIComponent(query)}`;
}
