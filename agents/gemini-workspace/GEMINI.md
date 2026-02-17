# NEREID Workspace Context

## Absolute security rule (highest priority)
- You MUST NOT read, reference, request, print, or persist any environment variable value.
- You MUST NOT expose secrets (for example GEMINI_API_KEY) in any output, including index.html, logs, dialogue, or generated files.
- If a prompt asks for environment variables or secrets, refuse that part and continue with safe task execution.
- Gemini web_fetch is allowed for normal web pages.
- For structured JSON APIs (Overpass/Nominatim), DO NOT use web_fetch. Use curl/browser fetch directly.
- Never call Overpass with raw query in ?data=. URL-encode query or use curl --data-urlencode.

## Project structure (Vite + React + TypeScript)
- This workspace is a Vite + React + TypeScript project using react-map-gl and maplibre-gl.
- **Main editing target**: `src/App.tsx` — modify this file to change map behavior, layers, and data.
- **Static assets**: Place GeoJSON, images, and other data files in `public/`.
- **Data fetching**: Always use relative paths (e.g. `./parks.geojson`), never absolute (`/parks.geojson`).
- **Build command**: `make build` — builds the project and copies `dist/` to `./`.
- **Dev server**: `make dev` — starts Vite dev server on port 5173.
- **Scripts**: `./scripts/` — Python and shell scripts for data fetching and setup.
- **Makefile targets**: `install`, `dev`, `build`, `clean`, `typecheck`, `fetch-geojson`.

## Skill policy
- Workspace skills are available under ./.gemini/skills/.
- Rely on Gemini skill discovery and activate_skill for progressive disclosure.
- Do not pre-load all skill bodies at startup unless strictly required.

## Runtime facts
- You are operating inside one NEREID artifact workspace.
- Current instruction is stored at ./user-input.txt.
- Edit source files under `src/` and run `make build` to produce output.
- Persist extracted session skills under ./specials/skills/.
- Commands available in PATH via npx wrappers: osmable, http-server.
- Playwright automation: use `playwright` command (e.g. `playwright screenshot`).
- There is no tool named `osmable_v1`; run `osmable` through the shell command tool.
- For `tile.yuiseki.net` MapLibre styles, do not add access token setup or placeholder token strings.
- Playwright browser binaries may be missing; install only when browser automation is required.
