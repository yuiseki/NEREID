import { useCallback, useEffect, useState } from "react";
import Map, {
    Source,
    Layer,
    NavigationControl,
    GeolocateControl,
} from "react-map-gl/maplibre";
import maplibregl from "maplibre-gl";
import { Protocol as PMTilesProtocol } from "pmtiles";
import "maplibre-gl/dist/maplibre-gl.css";
import type { MapLayerMouseEvent } from "react-map-gl/maplibre";
import type {
    FillLayerSpecification,
    LineLayerSpecification,
} from "maplibre-gl";

/**
 * マップスタイル定義
 * 生成AIはここにスタイルを追加・変更する
 */
const mapStyles = {
    yuiseki_bright: "https://tile.yuiseki.net/styles/osm-bright/style.json",
    yuiseki_fiord: "https://tile.yuiseki.net/styles/osm-fiord/style.json",
    osm_vector: "./styles/osm_vector.json",
    osm_raster: "./styles/osm_raster.json",
    osm_pmtiles: "./styles/osm_pmtiles.json",
};

type MapStyle = keyof typeof mapStyles;

/**
 * GeoJSON データレイヤーのスタイル定義
 * 生成AIはここを編集してレイヤーを追加・変更する
 */
const fillLayerStyle: FillLayerSpecification = {
    id: "data-fill",
    type: "fill",
    source: "geojson-data",
    paint: {
        "fill-color": "#2d6a4f",
        "fill-opacity": 0.4,
    },
};

const outlineLayerStyle: LineLayerSpecification = {
    id: "data-outline",
    type: "line",
    source: "geojson-data",
    paint: {
        "line-color": "#1b4332",
        "line-width": 1.5,
    },
};

/**
 * GeoJSON データのプレースホルダー
 * 生成AIはここを実際のデータURL or インラインデータに置き換える
 */
const GEOJSON_DATA_URL = "./data.geojson";

/**
 * 初期ビューステート
 * 生成AIはここを対象エリアに合わせて変更する
 */
const INITIAL_VIEW = {
    longitude: 139.7671,
    latitude: 35.6812,
    zoom: 12,
};

/**
 * PMTiles プロトコル初期化（一度だけ）
 */
let pmTilesInitialized = false;
const initializePMTiles = () => {
    if (!pmTilesInitialized) {
        const protocol = new PMTilesProtocol();
        maplibregl.addProtocol("pmtiles", protocol.tile);
        pmTilesInitialized = true;
    }
};

function App() {
    const [mapStyle, setMapStyle] = useState<MapStyle>("yuiseki_bright");
    const [, setSelectedFeature] = useState<GeoJSON.Feature | null>(null);

    // PMTiles プロトコル初期化
    useEffect(() => {
        initializePMTiles();
    }, []);

    const handleClick = useCallback((e: MapLayerMouseEvent) => {
        const feature = e.features?.[0] ?? null;
        setSelectedFeature(feature ?? null);
        if (feature) {
            console.log("Selected feature:", feature.properties);
        }
    }, []);

    const handleStyleChange = useCallback((style: MapStyle) => {
        setMapStyle(style);
        const params = new URLSearchParams(window.location.search);
        params.set("style", style);
        let newUrl =
            window.location.protocol +
            "//" +
            window.location.host +
            window.location.pathname +
            "?" +
            params.toString();
        if (window.location.hash) {
            newUrl += window.location.hash;
        }
        window.history.replaceState({ path: newUrl }, "", newUrl);
    }, []);

    return (
        <Map
            mapLib={maplibregl}
            initialViewState={INITIAL_VIEW}
            style={{ width: "100%", height: "100vh" }}
            mapStyle={mapStyles[mapStyle]}
            hash={true}
            interactiveLayerIds={GEOJSON_DATA_URL ? ["data-fill"] : []}
            onClick={handleClick}
        >
            {/* スタイル切替 */}
            <div
                style={{
                    position: "absolute",
                    top: 10,
                    left: 10,
                    zIndex: 1,
                }}
            >
                <select
                    value={mapStyle}
                    onChange={(e) => handleStyleChange(e.target.value as MapStyle)}
                    style={{
                        padding: "4px 8px",
                        borderRadius: 4,
                        border: "1px solid #ccc",
                        backgroundColor: "rgba(255,255,255,0.9)",
                        fontSize: 12,
                    }}
                >
                    <option value="yuiseki_bright">tile.yuiseki.net Bright</option>
                    <option value="yuiseki_fiord">tile.yuiseki.net Fiord</option>
                    <option value="osm_vector">OSM Vector</option>
                    <option value="osm_raster">OSM Raster</option>
                    <option value="osm_pmtiles">OSM PMTiles</option>
                </select>
            </div>

            <GeolocateControl position="top-right" />
            <NavigationControl
                position="top-right"
                visualizePitch={true}
                showZoom={true}
                showCompass={true}
            />

            {GEOJSON_DATA_URL && (
                <Source id="geojson-data" type="geojson" data={GEOJSON_DATA_URL}>
                    <Layer {...fillLayerStyle} />
                    <Layer {...outlineLayerStyle} />
                </Source>
            )}
        </Map>
    );
}

export default App;
