# NEREID Agent Runtime TODO

## Done
- [x] Added `AfterAgent` hook to validate `index.html` existence/basic HTML shape and detect known runtime error signatures.
- [x] Added `curl` / `wget` bootstrap in generated Gemini job scripts.
- [x] Added npx-based command wrappers in job runtime PATH:
  - `osmable`
  - `http-server`
  - `playwright-cli`
- [x] Added configurable job image env wiring:
  - `NEREID_AGENT_IMAGE` (API planner default image + legacy fallback)
  - `NEREID_LEGACY_AGENT_IMAGE` (legacy-only override)
- [x] Added dedicated runner image definition: `Dockerfile.agent-runtime`.
- [x] Added static workspace template directory for image packaging:
  - `agents/gemini-workspace/GEMINI.md`
  - `agents/gemini-workspace/.gemini/settings.json`
  - `agents/gemini-workspace/.gemini/hooks/validate-index.sh`
  - `agents/gemini-workspace/.gemini/skills/*/SKILL.md` (all current generated skills)
  - `agents/gemini-workspace/.gemini/skills/playwright-cli/SKILL.md`
- [x] Added runtime overlay logic to copy `/opt/nereid/gemini-workspace` into each work dir when present.

## In Progress
- [ ] Validate end-to-end behavior on `nereid.yuiseki.net` with real prompt:
  - `東京都台東区の公園を表示してください。`
- [ ] Confirm whether we should switch Helm default `agentRuntime.image` to a prebuilt custom image (currently default remains `node:22-bookworm-slim` for compatibility).

## Pending / Design Decision
- [ ] Build/publish dedicated preinstalled runner image from `Dockerfile.agent-runtime` and update deployment values.
- [ ] Decide policy for Playwright browser binaries in runner image:
  - keep image small (install browsers on demand), or
  - preinstall Chromium at image build time (`PLAYWRIGHT_CHROMIUM=1`) for deterministic startup.
- [ ] De-inline duplicated skill markdown from Go source and use static templates as the primary source.
- [ ] Evaluate shared layout strategy for future multi-agent support:
  - `agents/gemini-workspace`, `agents/codex-workspace`, ...
  - or consolidated `agents/agent-workspace` base + per-agent overlays.
