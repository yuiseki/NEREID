# NEREID Agent Runtime TODO

## Done
- [x] Added `AfterAgent` hook to validate `index.html` existence/basic HTML shape and detect known runtime error signatures.
- [x] Standardized runtime setup around a prebuilt agent image + workspace templates (removed script-level `apt-get`/wrapper bootstrapping).
- [x] Runtime scripts now fail fast when workspace template assets are missing:
  - `${NEREID_GEMINI_TEMPLATE_ROOT}/.gemini`
  - `${NEREID_GEMINI_TEMPLATE_ROOT}/GEMINI.md`
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
- [x] Removed inline skill/hook/GEMINI markdown generation from Go runtime scripts and switched to template-only workspace copy.
- [x] Removed legacy inline skill embedding (`legacy*SkillDoc`) and replaced with kind->skill mapping plus template checks.

## In Progress
- [ ] Validate end-to-end behavior on `nereid.yuiseki.net` with real prompt:
  - `東京都台東区の公園を表示してください。`
- [ ] Confirm whether we should switch Helm default `agentRuntime.image` to a prebuilt custom image (currently default remains `node:22-bookworm-slim`).

## Pending / Design Decision
- [ ] Build/publish dedicated preinstalled runner image from `Dockerfile.agent-runtime` and update deployment values.
- [ ] Decide policy for Playwright browser binaries in runner image:
  - keep image small (install browsers on demand), or
  - preinstall Chromium at image build time (`PLAYWRIGHT_CHROMIUM=1`) for deterministic startup.
- [ ] Evaluate shared layout strategy for future multi-agent support:
  - `agents/gemini-workspace`, `agents/codex-workspace`, ...
  - or consolidated `agents/agent-workspace` base + per-agent overlays.
