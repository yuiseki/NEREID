import { useCallback } from "react";
import { useMap } from "react-map-gl/maplibre";
import * as turf from "@turf/turf";
import type { PaddingOptions } from "maplibre-gl";

/**
 * GeoJSON FeatureCollection の範囲に地図をフィットさせるフック
 * create-free-map-asap パターンを汎用化
 */
export function useFitBounds() {
    const { current: map } = useMap();

    const fitBounds = useCallback(
        (
            geoJson: GeoJSON.FeatureCollection,
            options?: { padding?: PaddingOptions; duration?: number }
        ) => {
            if (!map || !geoJson.features.length) return;

            const [minLng, minLat, maxLng, maxLat] = turf.bbox(geoJson);
            map.fitBounds(
                [
                    [minLng, minLat],
                    [maxLng, maxLat],
                ],
                {
                    padding: options?.padding ?? { top: 40, left: 40, right: 40, bottom: 40 },
                    duration: options?.duration ?? 1000,
                }
            );
        },
        [map]
    );

    return fitBounds;
}
