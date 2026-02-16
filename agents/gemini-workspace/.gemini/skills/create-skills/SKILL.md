---
name: create-skills
description: Extract reusable lessons from this session and persist them as local skill documents under specials/skills.
---
# Create Session Skills

## Goal
- Persist reusable operational knowledge from the current task as skill documents.

## Required behavior
- Before finishing, write at least one skill directory under ./specials/skills/.
- For each created skill, create ./specials/skills/<skill-name>/SKILL.md.
- The frontmatter name must exactly match <skill-name>.
- Keep each SKILL.md focused on reusable decision rules, not task-specific narration.
- Use this structure in each SKILL.md:
  1. Trigger patterns
  2. Decision rule
  3. Execution steps
  4. Failure signals and fallback
- Use lowercase letters, digits, and hyphens for <skill-name>.
- Add scripts/, references/, and assets/ only when needed.
- Each created skill must be unique compared with existing skills in ./.gemini/skills and ./specials/skills.
- Each created skill must be highly reproducible: include explicit prerequisites, stable inputs, deterministic steps, and expected outputs.
- If an equivalent skill already exists, update that local session skill instead of creating a duplicate.
- Never include secrets, environment variables, or user-private sensitive content.

## Scope
- Save only local session skills in ./specials/skills/.
- Do not modify global NEREID runtime code or external skill repositories.
