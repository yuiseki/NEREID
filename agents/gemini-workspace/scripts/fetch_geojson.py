#!/usr/bin/env python3
"""
scripts/fetch_geojson.py — Fetch GeoJSON data from Overpass/Nominatim APIs.

Usage:
    python3 scripts/fetch_geojson.py --query "leisure=park" --area "東京都台東区" --output public/parks.geojson
    python3 scripts/fetch_geojson.py --nominatim "東京駅" --output public/station.geojson

This is a template script. AI agents can modify this to add
custom data fetching and processing pipelines.
"""

import argparse
import json
import sys
import urllib.request
import urllib.parse
from pathlib import Path

OVERPASS_ENDPOINT = "https://overpass.yuiseki.net/api/interpreter"
NOMINATIM_ENDPOINT = "https://nominatim.yuiseki.net/search.php"


def fetch_overpass(tag: str, area: str, timeout: int = 25) -> dict:
    """Fetch OSM data via Overpass API."""
    key, value = tag.split("=", 1)
    query = f"""
[out:json][timeout:{timeout}];
area[name="{area}"]->.searchArea;
(
  nwr["{key}"="{value}"](area.searchArea);
);
out geom;
"""
    encoded = urllib.parse.urlencode({"data": query})
    url = f"{OVERPASS_ENDPOINT}?{encoded}"
    req = urllib.request.Request(url, headers={"User-Agent": "NEREID-fetch/1.0"})

    with urllib.request.urlopen(req, timeout=60) as resp:
        return json.loads(resp.read().decode("utf-8"))


def overpass_to_geojson(data: dict) -> dict:
    """Convert Overpass JSON elements to GeoJSON FeatureCollection."""
    features = []
    for elem in data.get("elements", []):
        geom = None
        if elem["type"] == "node":
            geom = {"type": "Point", "coordinates": [elem["lon"], elem["lat"]]}
        elif elem["type"] == "way" and "geometry" in elem:
            coords = [[pt["lon"], pt["lat"]] for pt in elem["geometry"]]
            if coords and coords[0] == coords[-1] and len(coords) >= 4:
                geom = {"type": "Polygon", "coordinates": [coords]}
            else:
                geom = {"type": "LineString", "coordinates": coords}
        elif elem["type"] == "relation" and "bounds" in elem:
            # Simplified: use bounds as polygon for relations
            b = elem["bounds"]
            geom = {
                "type": "Polygon",
                "coordinates": [
                    [
                        [b["minlon"], b["minlat"]],
                        [b["maxlon"], b["minlat"]],
                        [b["maxlon"], b["maxlat"]],
                        [b["minlon"], b["maxlat"]],
                        [b["minlon"], b["minlat"]],
                    ]
                ],
            }

        if geom:
            features.append(
                {
                    "type": "Feature",
                    "properties": elem.get("tags", {}),
                    "geometry": geom,
                }
            )

    return {"type": "FeatureCollection", "features": features}


def fetch_nominatim(query: str) -> dict:
    """Fetch location via Nominatim API."""
    params = urllib.parse.urlencode({"format": "jsonv2", "limit": 1, "q": query})
    url = f"{NOMINATIM_ENDPOINT}?{params}"
    req = urllib.request.Request(url, headers={"User-Agent": "NEREID-fetch/1.0"})

    with urllib.request.urlopen(req, timeout=30) as resp:
        results = json.loads(resp.read().decode("utf-8"))

    if not results:
        return {"type": "FeatureCollection", "features": []}

    r = results[0]
    return {
        "type": "FeatureCollection",
        "features": [
            {
                "type": "Feature",
                "properties": {
                    "name": r.get("display_name", ""),
                    "type": r.get("type", ""),
                    "category": r.get("category", ""),
                },
                "geometry": {
                    "type": "Point",
                    "coordinates": [float(r["lon"]), float(r["lat"])],
                },
            }
        ],
    }


def main():
    parser = argparse.ArgumentParser(description="Fetch GeoJSON data for NEREID")
    parser.add_argument("--query", help="Overpass tag query (e.g. leisure=park)")
    parser.add_argument("--area", help="Area name for Overpass (e.g. 東京都台東区)")
    parser.add_argument("--nominatim", help="Nominatim search query")
    parser.add_argument(
        "--output",
        default="public/data.geojson",
        help="Output file path (default: public/data.geojson)",
    )
    args = parser.parse_args()

    if not args.query and not args.nominatim:
        parser.print_help()
        sys.exit(1)

    output_path = Path(args.output)
    output_path.parent.mkdir(parents=True, exist_ok=True)

    if args.query:
        if not args.area:
            print("Error: --area is required with --query", file=sys.stderr)
            sys.exit(1)
        print(f"Fetching Overpass data: {args.query} in {args.area}...")
        raw = fetch_overpass(args.query, args.area)
        geojson = overpass_to_geojson(raw)
    else:
        print(f"Fetching Nominatim data: {args.nominatim}...")
        geojson = fetch_nominatim(args.nominatim)

    with open(output_path, "w", encoding="utf-8") as f:
        json.dump(geojson, f, ensure_ascii=False, indent=2)

    n = len(geojson["features"])
    print(f"Done. {n} features written to {output_path}")


if __name__ == "__main__":
    main()
