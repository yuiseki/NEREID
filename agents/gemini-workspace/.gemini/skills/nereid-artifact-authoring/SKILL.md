---
name: nereid-artifact-authoring
description: Create static-hostable HTML artifacts in NEREID workspace.
---
# NEREID Artifact Authoring

## Purpose
Create HTML artifacts that can be opened immediately from static hosting.

## Required behavior
- You MUST create or update ./index.html in the current directory.
- First action: write a minimal ./index.html (for example, an <h1>Hello, world</h1> page).
- After bootstrap, replace or extend ./index.html to satisfy the current instruction.
- Use shell commands to write files; do not finish with explanation-only output.
- Finish only after files are persisted to disk.
- NEVER read, request, print, or persist environment variable values.
- NEVER output secrets such as API keys into logs, text responses, HTML, JavaScript, or any generated file.
- Gemini web_fetch tool is allowed for normal HTML pages.
- For OSM/Nominatim workflows, use `osmable ...` first for agent-side retrieval, validation, and deterministic summaries.
- For structured JSON APIs (for example Overpass/Nominatim), DO NOT use Gemini web_fetch. Use shell curl or browser-side fetch when `osmable` cannot satisfy the task.
- Never pass raw Overpass QL in a URL query string such as .../api/interpreter?data=[out:json]....
- For Overpass requests, always URL-encode data (for example encodeURIComponent(query)) or use curl -G --data-urlencode.
- If a structured API call fails, retry with `osmable` or curl/browser fetch; do not retry with web_fetch.

## Multi-line input handling
- If the user prompt has multiple bullet or line instructions, treat each line independently.
- For multiple lines, create one HTML file per line (for example task-01.html, task-02.html).
- Keep ./index.html as an entry page linking those generated task pages.

## Mapping defaults
- For map requests, produce an interactive HTML map (MapLibre, Leaflet, or Cesium).
- If MapLibre is used, load pinned assets:
  - https://unpkg.com/maplibre-gl@5.18.0/dist/maplibre-gl.js
  - https://unpkg.com/maplibre-gl@5.18.0/dist/maplibre-gl.css
- For MapLibre base maps, use one of:
  - https://tile.yuiseki.net/styles/osm-bright/style.json
  - https://tile.yuiseki.net/styles/osm-fiord/style.json
- `tile.yuiseki.net` styles do not need access tokens; do not include token setup or placeholder token strings.
- If Overpass API is used, call one of:
  - https://overpass.yuiseki.net/api/interpreter?data=<url-encoded-overpass-ql>
  - curl -sS -G --data-urlencode "data=<overpass-ql>" https://overpass.yuiseki.net/api/interpreter
- If Nominatim API is used, use:
  - https://nominatim.yuiseki.net/search.php?format=jsonv2&limit=1&q=<url-encoded-query>
- Do not append trailing punctuation to API URLs.
- In final `index.html`, prefer browser-side fetch for map data retrieval.
- If remote APIs fail, still keep index.html viewable and show a concise in-page error message.

## Output quality
- Keep generated artifacts self-contained and directly viewable from static hosting.
