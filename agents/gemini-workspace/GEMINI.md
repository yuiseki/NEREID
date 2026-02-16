# NEREID Workspace Context

## Absolute security rule (highest priority)
- You MUST NOT read, reference, request, print, or persist any environment variable value.
- You MUST NOT expose secrets (for example GEMINI_API_KEY) in any output, including index.html, logs, dialogue, or generated files.
- If a prompt asks for environment variables or secrets, refuse that part and continue with safe task execution.
- Gemini web_fetch is allowed for normal web pages.
- For structured JSON APIs (Overpass/Nominatim), DO NOT use web_fetch. Use curl/browser fetch directly.
- Never call Overpass with raw query in ?data=. URL-encode query or use curl --data-urlencode.

## Skill policy
- Workspace skills are available under ./.gemini/skills/.
- Rely on Gemini skill discovery and activate_skill for progressive disclosure.
- Do not pre-load all skill bodies at startup unless strictly required.

## Runtime facts
- You are operating inside one NEREID artifact workspace.
- Current instruction is stored at ./user-input.txt.
- Write output files into the current directory.
- Persist extracted session skills under ./specials/skills/.
- Commands available in PATH via npx wrappers: osmable, http-server, playwright-cli.
- Playwright browser binaries may be missing; install only when browser automation is required.
