---
name: nereid-skill-creator
description: Create or update NEREID workspace Gemini CLI skills. Use when asked to design, improve, rename, or maintain SKILL.md-based expertise, including extracting reusable session lessons into ./specials/skills.
---
# Skill Creator

## Purpose
- Create high-quality, reusable skills without bloating context.
- Prefer concise guidance that is easy for another agent to execute deterministically.

## Core principles
- Keep `SKILL.md` concise. Add only non-obvious guidance.
- Put trigger conditions in frontmatter `description` so discovery works.
- Use progressive disclosure: keep core workflow in `SKILL.md`, move deep details to `references/`.
- Add `scripts/` only when deterministic execution or repeated code reuse is needed.
- Add `assets/` only for files consumed by generated output.

## Skill anatomy
- Required: `SKILL.md` with frontmatter `name` and `description`.
- Optional: `scripts/`, `references/`, `assets/`.
- Do not add auxiliary docs such as `README.md`, `QUICK_REFERENCE.md`, or `CHANGELOG.md`.

## Naming rules
- Use lowercase letters, digits, and hyphens only.
- Keep names short, action-oriented, and under 64 characters.
- The folder name must match frontmatter `name` exactly.

## Workflow
1. Understand usage examples and trigger phrases.
2. Identify reusable elements (instructions, scripts, references, assets).
3. Create or update the skill folder and `SKILL.md`.
4. Ensure `description` clearly states what the skill does and when to use it.
5. Add deterministic execution steps and failure fallback guidance.
6. Validate uniqueness and safety before finishing.

## Required behavior for this environment
- For session-derived expertise, persist local skills under `./specials/skills/<skill-name>/SKILL.md`.
- If an equivalent local skill already exists, update it instead of creating a duplicate.
- Each created skill must be reproducible: explicit prerequisites, stable inputs, deterministic steps, expected outputs.
- Never include secrets, API keys, private tokens, or environment variable values.

## Quality checklist
- `name` equals folder name.
- `description` contains concrete trigger intent.
- Steps are imperative and executable.
- Failure signals and fallback actions are included.
- Skill is unique vs `./.gemini/skills` and `./specials/skills`.
