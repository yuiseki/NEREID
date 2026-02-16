---
name: overpassql-map-v1
description: Decide when to use Overpass QL and how to design robust map data queries.
---
# Overpass QL Strategy

## When to use
- User asks for specific real-world objects from OpenStreetMap (parks, convenience stores, stations, roads, rivers, boundaries).
- The request needs data filtering by tags, area, or bounding box.

## Core knowledge
- Overpass QL retrieves OSM elements: node / way / relation.
- Administrative area search commonly uses area objects and area references.
- Query shape and output mode strongly affect response size and performance.

## Recommended workflow
1. Resolve target area from user instruction (city/ward/region).
2. Build minimal Overpass QL with explicit tag filters.
3. Prefer osmable poi count/fetch for deterministic OSM retrieval.
4. For parks, use `leisure=park` as the primary tag (do not start with `amenity=park`).
5. Keep the original area string from the user prompt when calling osmable:
   - good: `osmable poi count --tag leisure=park --within "東京都台東区" --format json`
   - avoid replacing with transliterated English area names unless geocode confirms.
6. If count is zero unexpectedly, retry once with `osmable aoi resolve "<same area>" --format geojson` and then rerun the same `leisure=park` query.
7. Use endpoint https://overpass.yuiseki.net/api/interpreter with URL-encoded data parameter only when osmable cannot complete.
8. Keep timeout and output size reasonable.
9. Convert response to map-friendly geometry and render in index.html.

## Output expectations
- Store raw response for debugging.
- Show clear map visualization and concise summary in-page.
- Never fabricate dummy/placeholder parks to satisfy validation.
