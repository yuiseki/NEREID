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
4. If manual Overpass execution is required, use browser fetch or curl (not web_fetch).
5. Use endpoint https://overpass.yuiseki.net/api/interpreter with URL-encoded data parameter.
6. Keep timeout and output size reasonable.
7. Convert response to map-friendly geometry and render in index.html.

## Output expectations
- Store raw response for debugging.
- Show clear map visualization and concise summary in-page.
