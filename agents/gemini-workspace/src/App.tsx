/**
 * NEREID Map App - データ駆動型マルチレイヤーマップ
 *
 * このファイルは public/layers/config.json を読み込んで自動的にレンダリングする。
 * エージェントは App.tsx を変更せず、以下のファイルだけを操作すること:
 *   - public/layers/config.json  (レイヤー定義)
 *   - public/layers/*.geojson    (各レイヤーのGeoJSONデータ)
 */
import { useCallback, useEffect, useRef, useState } from "react";
import Map, {
    Source,
    Layer,
    Marker,
    Popup,
    NavigationControl,
    GeolocateControl,
} from "react-map-gl/maplibre";
import maplibregl from "maplibre-gl";
import { Protocol as PMTilesProtocol } from "pmtiles";
import * as turf from "@turf/turf";
import "maplibre-gl/dist/maplibre-gl.css";
import type { MapLayerMouseEvent } from "react-map-gl/maplibre";
import type { FeatureCollection, Feature, GeoJsonProperties, Point } from "geojson";

// ============================================================
// 型定義
// ============================================================

interface LayerConfig {
    id: string;
    name: string;
    file: string;
    emoji: string;
    color: string;
    outlineColor: string;
    showMarker: boolean;
}

interface MapConfig {
    title: string;
    initialView: {
        longitude: number;
        latitude: number;
        zoom: number;
    };
    showPopupOnClick: boolean;
    layers: LayerConfig[];
}

interface LoadedLayer {
    config: LayerConfig;
    geojson: FeatureCollection;
    markers: Array<{
        id: string;
        longitude: number;
        latitude: number;
        name: string;
        emoji: string;
        color: string;
    }>;
}

interface PopupInfo {
    longitude: number;
    latitude: number;
    name: string;
}

// ============================================================
// マップスタイル
// ============================================================

const mapStyles = {
    yuiseki_bright: "https://tile.yuiseki.net/styles/osm-bright/style.json",
    yuiseki_fiord: "https://tile.yuiseki.net/styles/osm-fiord/style.json",
    osm_vector: "./styles/osm_vector.json",
    osm_raster: "./styles/osm_raster.json",
    osm_pmtiles: "./styles/osm_pmtiles.json",
};

type MapStyle = keyof typeof mapStyles;

// ============================================================
// デフォルト config（config.json が存在しない場合のフォールバック）
// ============================================================

const DEFAULT_CONFIG: MapConfig = {
    title: "NEREID Map",
    initialView: { longitude: 139.7671, latitude: 35.6812, zoom: 12 },
    showPopupOnClick: false,
    layers: [],
};

// ============================================================
// PMTiles 初期化
// ============================================================

let pmTilesInitialized = false;
const initializePMTiles = () => {
    if (!pmTilesInitialized) {
        const protocol = new PMTilesProtocol();
        maplibregl.addProtocol("pmtiles", protocol.tile);
        pmTilesInitialized = true;
    }
};

// ============================================================
// ユーティリティ: ポリゴン/ポイントの centroid 計算
// ============================================================

function getCentroid(feature: Feature): Feature<Point, GeoJsonProperties> | null {
    try {
        const geomType = feature.geometry?.type;
        if (!geomType) return null;
        if (geomType === "Point") {
            return turf.point((feature.geometry as GeoJSON.Point).coordinates);
        }
        if (geomType === "Polygon") {
            return turf.centroid(turf.polygon((feature.geometry as GeoJSON.Polygon).coordinates));
        }
        if (geomType === "MultiPolygon") {
            return turf.centroid(turf.multiPolygon((feature.geometry as GeoJSON.MultiPolygon).coordinates));
        }
        if (geomType === "LineString") {
            const bbox = turf.bbox(feature);
            return turf.centroid(turf.bboxPolygon(bbox));
        }
        return null;
    } catch {
        return null;
    }
}

// ============================================================
// ユーティリティ: featureName 取得
// ============================================================

function getFeatureName(feature: Feature): string {
    const p = feature.properties ?? {};
    return p.name ?? p["name:ja"] ?? p["name:en"] ?? p.ref ?? "(名称不明)";
}

// ============================================================
// App コンポーネント
// ============================================================

function App() {
    const [mapStyle, setMapStyle] = useState<MapStyle>("yuiseki_bright");
    const [config, setConfig] = useState<MapConfig>(DEFAULT_CONFIG);
    const [loadedLayers, setLoadedLayers] = useState<LoadedLayer[]>([]);
    const [popupInfo, setPopupInfo] = useState<PopupInfo | null>(null);
    const [zoom, setZoom] = useState<number>(12);
    const mapRef = useRef<maplibregl.Map | null>(null);

    // PMTiles 初期化
    useEffect(() => {
        initializePMTiles();
    }, []);

    // config.json 読み込み
    useEffect(() => {
        fetch("./layers/config.json")
            .then((r) => r.json())
            .then((cfg: MapConfig) => {
                setConfig(cfg);
                document.title = cfg.title ?? "NEREID Map";
            })
            .catch(() => {
                // config.json がない場合はデフォルトを使用
                setConfig(DEFAULT_CONFIG);
            });
    }, []);

    // 各レイヤーの GeoJSON 読み込み
    useEffect(() => {
        if (!config.layers || config.layers.length === 0) return;

        const loadAll = async () => {
            const results: LoadedLayer[] = [];
            for (const layerCfg of config.layers) {
                try {
                    const res = await fetch(layerCfg.file);
                    const geojson: FeatureCollection = await res.json();

                    // マーカー生成（showMarker=true または Point フィーチャ）
                    const markers: LoadedLayer["markers"] = [];
                    if (layerCfg.showMarker) {
                        geojson.features.forEach((feature, idx) => {
                            const center = getCentroid(feature);
                            if (!center) return;
                            const lng = center.geometry.coordinates[0] as number;
                            const lat = center.geometry.coordinates[1] as number;
                            markers.push({
                                id: `${layerCfg.id}-marker-${idx}`,
                                longitude: lng,
                                latitude: lat,
                                name: getFeatureName(feature),
                                emoji: layerCfg.emoji,
                                color: layerCfg.color,
                            });
                        });
                    }

                    results.push({ config: layerCfg, geojson, markers });
                } catch {
                    console.warn(`Failed to load layer: ${layerCfg.file}`);
                }
            }
            setLoadedLayers(results);
        };

        loadAll();
    }, [config.layers]);

    // 全データに自動フィット
    useEffect(() => {
        if (!mapRef.current || loadedLayers.length === 0) return;
        const allFeatures = loadedLayers.flatMap((l) => l.geojson.features);
        if (allFeatures.length === 0) return;

        try {
            const collection: FeatureCollection = { type: "FeatureCollection", features: allFeatures };
            const bbox = turf.bbox(collection) as [number, number, number, number];
            // 有効な bbox かチェック
            if (bbox.some((v) => !isFinite(v))) return;
            mapRef.current.fitBounds(
                [[bbox[0], bbox[1]], [bbox[2], bbox[3]]],
                { padding: 60, duration: 1500, maxZoom: 16 }
            );
        } catch {
            // fitBounds 失敗時は何もしない
        }
    }, [loadedLayers]);

    // ズームレベル追跡（マーカーサイズ調整用）
    const handleMove = useCallback(() => {
        setZoom(mapRef.current?.getZoom() ?? 12);
    }, []);

    // クリックハンドラ（ポップアップ表示）
    const handleClick = useCallback(
        (e: MapLayerMouseEvent) => {
            if (!config.showPopupOnClick) return;
            const feature = e.features?.[0];
            if (!feature) {
                setPopupInfo(null);
                return;
            }
            const center = getCentroid(feature as Feature);
            if (!center) return;
            setPopupInfo({
                longitude: center.geometry.coordinates[0] as number,
                latitude: center.geometry.coordinates[1] as number,
                name: getFeatureName(feature as Feature),
            });
        },
        [config.showPopupOnClick]
    );

    // マーカークリック（flyTo）
    const handleMarkerClick = useCallback(
        (marker: LoadedLayer["markers"][0]) => {
            if (config.showPopupOnClick) {
                setPopupInfo({
                    longitude: marker.longitude,
                    latitude: marker.latitude,
                    name: marker.name,
                });
            }
            mapRef.current?.flyTo({
                center: [marker.longitude, marker.latitude],
                zoom: Math.max(mapRef.current.getZoom(), 15),
                duration: 800,
            });
        },
        [config.showPopupOnClick]
    );

    // マーカーの opacity/fontSize をズームに応じて変化
    const markerStyle = useCallback((zoom: number) => {
        let opacity = 0.85;
        let fontSize = "1.1em";
        if (zoom >= 14) { opacity = 1.0; fontSize = "1.4em"; }
        else if (zoom >= 13) { opacity = 0.9; fontSize = "1.3em"; }
        else if (zoom >= 12) { opacity = 0.85; fontSize = "1.2em"; }
        else if (zoom >= 11) { opacity = 0.75; fontSize = "1.1em"; }
        else if (zoom < 10) { opacity = 0.6; fontSize = "1em"; }
        return { opacity, fontSize };
    }, []);

    // インタラクティブレイヤーIDリスト（クリック検出用）
    const interactiveLayerIds = config.showPopupOnClick
        ? loadedLayers.flatMap((l) => [`${l.config.id}-fill`, `${l.config.id}-line`])
        : [];

    const { opacity, fontSize } = markerStyle(zoom);

    return (
        <Map
            ref={(ref) => { mapRef.current = ref?.getMap() ?? null; }}
            mapLib={maplibregl}
            initialViewState={config.initialView}
            style={{ width: "100%", height: "100vh" }}
            mapStyle={mapStyles[mapStyle]}
            hash={true}
            interactiveLayerIds={interactiveLayerIds}
            onClick={handleClick}
            onMove={handleMove}
        >
            {/* スタイル切替セレクタ */}
            <div style={{ position: "absolute", top: 10, left: 10, zIndex: 1 }}>
                <select
                    value={mapStyle}
                    onChange={(e) => setMapStyle(e.target.value as MapStyle)}
                    style={{
                        padding: "4px 8px",
                        borderRadius: 4,
                        border: "1px solid #ccc",
                        backgroundColor: "rgba(255,255,255,0.9)",
                        fontSize: 12,
                        cursor: "pointer",
                    }}
                >
                    <option value="yuiseki_bright">tile.yuiseki.net Bright</option>
                    <option value="yuiseki_fiord">tile.yuiseki.net Fiord</option>
                    <option value="osm_vector">OSM Vector</option>
                    <option value="osm_raster">OSM Raster</option>
                    <option value="osm_pmtiles">OSM PMTiles</option>
                </select>
            </div>

            {/* 地図タイトル */}
            {config.title && config.title !== "NEREID Map" && (
                <div
                    style={{
                        position: "absolute",
                        bottom: 36,
                        left: 10,
                        zIndex: 1,
                        backgroundColor: "rgba(255,255,255,0.85)",
                        padding: "6px 12px",
                        borderRadius: 6,
                        fontSize: 14,
                        fontWeight: "bold",
                        fontFamily: "sans-serif",
                        maxWidth: 300,
                        boxShadow: "0 1px 4px rgba(0,0,0,0.2)",
                    }}
                >
                    {config.title}
                </div>
            )}

            <GeolocateControl position="top-right" />
            <NavigationControl
                position="top-right"
                visualizePitch={true}
                showZoom={true}
                showCompass={true}
            />

            {/* レイヤーレンダリング */}
            {loadedLayers.map((layer) => (
                <Source
                    key={layer.config.id}
                    id={layer.config.id}
                    type="geojson"
                    data={layer.geojson}
                >
                    {/* Polygon/MultiPolygon フィル */}
                    <Layer
                        id={`${layer.config.id}-fill`}
                        type="fill"
                        filter={["in", ["geometry-type"], ["literal", ["Polygon", "MultiPolygon"]]]}
                        paint={{
                            "fill-color": layer.config.color,
                            "fill-opacity": 0.25,
                        }}
                    />
                    {/* Polygon/MultiPolygon アウトライン */}
                    <Layer
                        id={`${layer.config.id}-outline`}
                        type="line"
                        filter={["in", ["geometry-type"], ["literal", ["Polygon", "MultiPolygon", "LineString"]]]}
                        paint={{
                            "line-color": layer.config.outlineColor,
                            "line-width": 2,
                            "line-opacity": 0.8,
                        }}
                    />
                    {/* LineString */}
                    <Layer
                        id={`${layer.config.id}-line`}
                        type="line"
                        filter={["==", ["geometry-type"], "LineString"]}
                        paint={{
                            "line-color": layer.config.outlineColor,
                            "line-width": 2,
                            "line-opacity": 0.8,
                        }}
                    />
                </Source>
            ))}

            {/* 絵文字マーカー */}
            {loadedLayers.flatMap((layer) =>
                layer.markers.map((marker) => (
                    <Marker
                        key={marker.id}
                        longitude={marker.longitude}
                        latitude={marker.latitude}
                        onClick={() => handleMarkerClick(marker)}
                    >
                        <div
                            title={marker.name}
                            style={{
                                display: "flex",
                                alignItems: "center",
                                justifyContent: "center",
                                cursor: "pointer",
                                opacity,
                            }}
                        >
                            <div
                                style={{
                                    backgroundColor: marker.color,
                                    backdropFilter: "blur(4px)",
                                    borderRadius: "4px",
                                    padding: "2px 4px",
                                    fontSize,
                                    fontFamily: "sans-serif, emoji",
                                    lineHeight: "1.1",
                                    boxShadow: "0 1px 3px rgba(0,0,0,0.3)",
                                }}
                            >
                                {marker.emoji}
                            </div>
                        </div>
                    </Marker>
                ))
            )}

            {/* ポップアップ（クリックで名前表示） */}
            {popupInfo && (
                <Popup
                    longitude={popupInfo.longitude}
                    latitude={popupInfo.latitude}
                    anchor="bottom"
                    onClose={() => setPopupInfo(null)}
                    closeOnClick={false}
                >
                    <div
                        style={{
                            fontFamily: "sans-serif",
                            fontSize: 13,
                            maxWidth: 200,
                            wordBreak: "break-word",
                            padding: "2px 4px",
                        }}
                    >
                        {popupInfo.name}
                    </div>
                </Popup>
            )}
        </Map>
    );
}

export default App;
