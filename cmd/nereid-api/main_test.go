package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPlannerCredentialsFromEnvPrefersOpenAI(t *testing.T) {
	t.Setenv("NEREID_OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("NEREID_GEMINI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gemini-key")

	creds := plannerCredentialsFromEnv()
	if creds.key != "openai-key" {
		t.Fatalf("plannerCredentialsFromEnv().key got=%q want=%q", creds.key, "openai-key")
	}
	if creds.provider != plannerProviderOpenAI {
		t.Fatalf("plannerCredentialsFromEnv().provider got=%q want=%q", creds.provider, plannerProviderOpenAI)
	}
}

func TestPlannerCredentialsFromEnvFallsBackToGemini(t *testing.T) {
	t.Setenv("NEREID_OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("NEREID_GEMINI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gemini-key")

	creds := plannerCredentialsFromEnv()
	if creds.key != "gemini-key" {
		t.Fatalf("plannerCredentialsFromEnv().key got=%q want=%q", creds.key, "gemini-key")
	}
	if creds.provider != plannerProviderGemini {
		t.Fatalf("plannerCredentialsFromEnv().provider got=%q want=%q", creds.provider, plannerProviderGemini)
	}
}

func TestPlannerBaseURLDefaultsByProvider(t *testing.T) {
	t.Setenv("NEREID_LLM_BASE_URL", "")

	if got := plannerBaseURL(plannerProviderOpenAI); got != "https://api.openai.com/v1" {
		t.Fatalf("plannerBaseURL(openai) got=%q", got)
	}
	if got := plannerBaseURL(plannerProviderGemini); got != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Fatalf("plannerBaseURL(gemini) got=%q", got)
	}
}

func TestPlannerModelDefaultsByProvider(t *testing.T) {
	t.Setenv("NEREID_LLM_MODEL", "")
	t.Setenv("NEREID_GEMINI_MODEL", "")
	t.Setenv("GEMINI_MODEL", "")

	if got := plannerModel(plannerProviderOpenAI); got != "gpt-4o-mini" {
		t.Fatalf("plannerModel(openai) got=%q", got)
	}
	if got := plannerModel(plannerProviderGemini); got != "gemini-2.0-flash" {
		t.Fatalf("plannerModel(gemini) got=%q", got)
	}
}

func TestValidatePlannedSpecAcceptsAgentCLIKind(t *testing.T) {
	spec := map[string]interface{}{
		"kind":  "agent.cli.v1",
		"title": "agent",
		"agent": map[string]interface{}{
			"image": "node:22-bookworm-slim",
			"command": []interface{}{
				"npx",
				"-y",
				"@google/gemini-cli",
				"--help",
			},
		},
	}
	if err := validatePlannedSpec(spec); err != nil {
		t.Fatalf("validatePlannedSpec() error = %v", err)
	}
}

func TestValidatePlannedSpecRejectsAgentCLIWithoutScriptOrCommand(t *testing.T) {
	spec := map[string]interface{}{
		"kind":  "agent.cli.v1",
		"title": "agent",
		"agent": map[string]interface{}{
			"image": "node:22-bookworm-slim",
		},
	}
	if err := validatePlannedSpec(spec); err == nil {
		t.Fatal("validatePlannedSpec() expected error, got nil")
	}
}

func TestNormalizePlannedSpecConvertsAgentCommandFromString(t *testing.T) {
	spec := map[string]interface{}{
		"kind":  "agent.cli.v1",
		"title": "agent",
		"agent": map[string]interface{}{
			"image":   "node:22-bookworm-slim",
			"command": `npx -y "@google/gemini-cli" --help`,
		},
	}

	normalizePlannedSpec(spec)
	if err := validatePlannedSpec(spec); err != nil {
		t.Fatalf("validatePlannedSpec() after normalize error = %v", err)
	}
}

func TestNormalizePlannedSpecConvertsAgentArgsFromJSONString(t *testing.T) {
	spec := map[string]interface{}{
		"kind":  "agent.cli.v1",
		"title": "agent",
		"agent": map[string]interface{}{
			"image":   "node:22-bookworm-slim",
			"command": "npx",
			"args":    `["-y","@google/gemini-cli","--version"]`,
		},
	}

	normalizePlannedSpec(spec)
	if err := validatePlannedSpec(spec); err != nil {
		t.Fatalf("validatePlannedSpec() after normalize error = %v", err)
	}
}

func TestUserPromptAnnotationValueTrimsAndTruncates(t *testing.T) {
	in := strings.Repeat("x", maxUserPromptBytes+100)
	got := userPromptAnnotationValue("  " + in + "  ")
	if got == "" {
		t.Fatal("userPromptAnnotationValue() returned empty")
	}
	if len([]byte(got)) != maxUserPromptBytes {
		t.Fatalf("annotation length got=%d want=%d", len([]byte(got)), maxUserPromptBytes)
	}
}

func TestUserPromptAnnotationValueEmpty(t *testing.T) {
	if got := userPromptAnnotationValue("   "); got != "" {
		t.Fatalf("userPromptAnnotationValue() got=%q want empty", got)
	}
}

func TestComposeAgentPromptIncludesParentAndContext(t *testing.T) {
	got := composeAgentPrompt("next instruction", "work-123", "previous logs")
	if !strings.Contains(got, "work-123") {
		t.Fatalf("composeAgentPrompt() missing parent work: %q", got)
	}
	if !strings.Contains(got, "previous logs") {
		t.Fatalf("composeAgentPrompt() missing follow-up context: %q", got)
	}
	if !strings.Contains(got, "next instruction") {
		t.Fatalf("composeAgentPrompt() missing new prompt: %q", got)
	}
	if strings.Contains(got, "You are operating inside NEREID artifact workspace.") {
		t.Fatalf("composeAgentPrompt() should not include fixed system prompt: %q", got)
	}
}

func TestComposeAgentPromptWithoutContextReturnsPromptOnly(t *testing.T) {
	if got := composeAgentPrompt("simple", "", ""); got != "simple" {
		t.Fatalf("composeAgentPrompt() got=%q want=%q", got, "simple")
	}
}

func TestGeminiAgentScriptGeneratesGeminiMdAndSkill(t *testing.T) {
	script := geminiAgentScript()
	if !strings.Contains(script, `GEMINI_MD_FILE="${OUT_DIR}/GEMINI.md"`) {
		t.Fatalf("geminiAgentScript() missing GEMINI.md generation: %q", script)
	}
	if !strings.Contains(script, `GEMINI_SKILL_FILE="${GEMINI_SKILL_DIR}/SKILL.md"`) {
		t.Fatalf("geminiAgentScript() missing skill generation: %q", script)
	}
	if !strings.Contains(script, `CREATE_SKILLS_SKILL_FILE="${GEMINI_DIR}/skills/create-skills/SKILL.md"`) {
		t.Fatalf("geminiAgentScript() missing create-skills skill generation: %q", script)
	}
	if !strings.Contains(script, `SPECIALS_SKILLS_DIR="${SPECIALS_DIR}/skills"`) {
		t.Fatalf("geminiAgentScript() missing specials/skills output directory: %q", script)
	}
	if !strings.Contains(script, `KIND_OSMABLE_SKILL_FILE="${GEMINI_DIR}/skills/osmable-v1/SKILL.md"`) {
		t.Fatalf("geminiAgentScript() missing osmable skill generation: %q", script)
	}
	if !strings.Contains(script, `GEMINI_SETTINGS_FILE="${GEMINI_DIR}/settings.json"`) {
		t.Fatalf("geminiAgentScript() missing hooks settings generation path: %q", script)
	}
	if !strings.Contains(script, `INDEX_VALIDATE_HOOK_FILE="${GEMINI_HOOKS_DIR}/validate-index.sh"`) {
		t.Fatalf("geminiAgentScript() missing index validation hook path: %q", script)
	}
	if !strings.Contains(script, `OSMABLE_WRAPPER_FILE="${BIN_DIR}/osmable"`) {
		t.Fatalf("geminiAgentScript() missing osmable wrapper path: %q", script)
	}
	if !strings.Contains(script, `HTTP_SERVER_WRAPPER_FILE="${BIN_DIR}/http-server"`) {
		t.Fatalf("geminiAgentScript() missing http-server wrapper path: %q", script)
	}
	if !strings.Contains(script, `PLAYWRIGHT_CLI_WRAPPER_FILE="${BIN_DIR}/playwright-cli"`) {
		t.Fatalf("geminiAgentScript() missing playwright-cli wrapper path: %q", script)
	}
	if !strings.Contains(script, `exec npx -y --loglevel=error --no-update-notifier --no-fund --no-audit ${pkg} "\$@"`) {
		t.Fatalf("geminiAgentScript() missing generic npx wrapper body: %q", script)
	}
	if !strings.Contains(script, `create_npx_wrapper "${OSMABLE_WRAPPER_FILE}" "github:yuiseki/osmable"`) {
		t.Fatalf("geminiAgentScript() missing osmable wrapper registration: %q", script)
	}
	if !strings.Contains(script, `create_npx_wrapper "${HTTP_SERVER_WRAPPER_FILE}" "http-server"`) {
		t.Fatalf("geminiAgentScript() missing http-server wrapper registration: %q", script)
	}
	if !strings.Contains(script, `create_npx_wrapper "${PLAYWRIGHT_CLI_WRAPPER_FILE}" "playwright-cli"`) {
		t.Fatalf("geminiAgentScript() missing playwright-cli wrapper registration: %q", script)
	}
	if !strings.Contains(script, "Playwright browser binaries may be missing") {
		t.Fatalf("geminiAgentScript() missing Playwright runtime note in GEMINI.md: %q", script)
	}
	if !strings.Contains(script, "Commands available in PATH via npx wrappers: osmable, http-server, playwright-cli.") {
		t.Fatalf("geminiAgentScript() missing wrapper runtime facts in GEMINI.md: %q", script)
	}
	if !strings.Contains(script, `TEMPLATE_ROOT="${NEREID_GEMINI_TEMPLATE_ROOT:-/opt/nereid/gemini-workspace}"`) {
		t.Fatalf("geminiAgentScript() missing template root wiring: %q", script)
	}
	if !strings.Contains(script, `cp -R "${TEMPLATE_ROOT}/.gemini/." "${OUT_DIR}/.gemini/"`) {
		t.Fatalf("geminiAgentScript() missing template .gemini copy: %q", script)
	}
	if !strings.Contains(script, `cp "${TEMPLATE_ROOT}/GEMINI.md" "${GEMINI_MD_FILE}"`) {
		t.Fatalf("geminiAgentScript() missing template GEMINI.md copy: %q", script)
	}
	if !strings.Contains(script, `export PATH="${BIN_DIR}:${PATH}"`) {
		t.Fatalf("geminiAgentScript() missing PATH update for wrappers: %q", script)
	}
	if !strings.Contains(script, `"AfterAgent"`) {
		t.Fatalf("geminiAgentScript() missing AfterAgent hook configuration: %q", script)
	}
	if !strings.Contains(script, `"command": "$GEMINI_PROJECT_DIR/.gemini/hooks/validate-index.sh"`) {
		t.Fatalf("geminiAgentScript() missing hook command path: %q", script)
	}
	if !strings.Contains(script, `{"decision":"deny","reason":"%s"}`) {
		t.Fatalf("geminiAgentScript() missing hook deny output contract: %q", script)
	}
	if !strings.Contains(script, "osmable doctor") {
		t.Fatalf("geminiAgentScript() missing osmable guidance in skill body: %q", script)
	}
	if !strings.Contains(script, "Workspace skills are available under ./.gemini/skills/.") {
		t.Fatalf("geminiAgentScript() missing skill discovery policy in GEMINI.md: %q", script)
	}
	if strings.Contains(script, "@./.gemini/skills/") {
		t.Fatalf("geminiAgentScript() should not eager-load skill bodies via @ imports: %q", script)
	}
	if strings.Contains(script, "/.gemini/skills/legacy-") {
		t.Fatalf("geminiAgentScript() should not use legacy- prefixed skill paths: %q", script)
	}
	if !strings.Contains(script, "Absolute security rule (highest priority)") {
		t.Fatalf("geminiAgentScript() missing highest-priority security rule in GEMINI.md: %q", script)
	}
	if !strings.Contains(script, "MUST NOT read, reference, request, print, or persist any environment variable value") {
		t.Fatalf("geminiAgentScript() missing environment-variable prohibition in GEMINI.md: %q", script)
	}
	if !strings.Contains(script, "MUST NOT expose secrets (for example GEMINI_API_KEY)") {
		t.Fatalf("geminiAgentScript() missing secret exfiltration prohibition in GEMINI.md: %q", script)
	}
	if !strings.Contains(script, "Gemini web_fetch is allowed for normal web pages.") {
		t.Fatalf("geminiAgentScript() missing web_fetch allowance in GEMINI.md: %q", script)
	}
	if !strings.Contains(script, "DO NOT use web_fetch. Use curl/browser fetch directly.") {
		t.Fatalf("geminiAgentScript() missing structured API web_fetch prohibition in GEMINI.md: %q", script)
	}
	if !strings.Contains(script, "Never call Overpass with raw query in ?data=") {
		t.Fatalf("geminiAgentScript() missing raw Overpass query prohibition in GEMINI.md: %q", script)
	}
	if !strings.Contains(script, "curl -sS -G --data-urlencode \"data=<overpass-ql>\" https://overpass.yuiseki.net/api/interpreter") {
		t.Fatalf("geminiAgentScript() missing URL-encoded Overpass curl guidance in skill: %q", script)
	}
	if !strings.Contains(script, "https://nominatim.yuiseki.net/search.php?format=jsonv2&limit=1&q=<url-encoded-query>") {
		t.Fatalf("geminiAgentScript() missing strict Nominatim URL template: %q", script)
	}
	if !strings.Contains(script, "apt-get install -y -qq --no-install-recommends procps curl wget ca-certificates git") {
		t.Fatalf("geminiAgentScript() missing bootstrap for pgrep/curl/wget/git tools: %q", script)
	}
	if !strings.Contains(script, `GEMINI_CLI_MODEL="${NEREID_GEMINI_MODEL:-${GEMINI_MODEL:-gemini-2.5-flash}}"`) {
		t.Fatalf("geminiAgentScript() missing gemini-2.5-flash model default: %q", script)
	}
	if !strings.Contains(script, `--model "${GEMINI_CLI_MODEL}"`) {
		t.Fatalf("geminiAgentScript() missing explicit --model flag: %q", script)
	}
	if !strings.Contains(script, "YOLO mode is enabled\\. All tool calls will be automatically approved\\.") {
		t.Fatalf("geminiAgentScript() missing YOLO banner cleanup filter: %q", script)
	}
	if !strings.Contains(script, "Hook registry initialized with [0-9][0-9]* hook entries") {
		t.Fatalf("geminiAgentScript() missing hook registry cleanup filter: %q", script)
	}
}

func TestGenerateWorkIDv7(t *testing.T) {
	idText, err := generateWorkIDv7()
	if err != nil {
		t.Fatalf("generateWorkIDv7() error = %v", err)
	}
	id, err := uuid.Parse(idText)
	if err != nil {
		t.Fatalf("generateWorkIDv7() not uuid: %v", err)
	}
	if got := id.Version(); got != 7 {
		t.Fatalf("generateWorkIDv7() version got=%d want=7", got)
	}
}

func TestGeminiAgentImageUsesEnvOverride(t *testing.T) {
	t.Setenv("NEREID_AGENT_IMAGE", "ghcr.io/yuiseki/nereid-agent-runtime:test")
	if got := geminiAgentImage(); got != "ghcr.io/yuiseki/nereid-agent-runtime:test" {
		t.Fatalf("geminiAgentImage() got=%q", got)
	}
}

func TestGeminiAgentImageDefaults(t *testing.T) {
	t.Setenv("NEREID_AGENT_IMAGE", "")
	if got := geminiAgentImage(); got != defaultAgentImage {
		t.Fatalf("geminiAgentImage() got=%q want=%q", got, defaultAgentImage)
	}
}

func TestGenerateWorkIDv7ReturnsErrorWhenGeneratorFails(t *testing.T) {
	old := newUUIDv7Func
	newUUIDv7Func = func() (uuid.UUID, error) { return uuid.UUID{}, errors.New("boom") }
	t.Cleanup(func() { newUUIDv7Func = old })

	if _, err := generateWorkIDv7(); err == nil {
		t.Fatal("generateWorkIDv7() expected error, got nil")
	}
}
