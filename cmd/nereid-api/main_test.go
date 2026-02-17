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
	if got := plannerModel(plannerProviderGemini); got != "gemini-2.5-pro" {
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

func TestGeminiAgentScriptUsesWorkspaceTemplate(t *testing.T) {
	script := geminiAgentScript()
	for _, needle := range []string{
		`SPECIALS_SKILLS_DIR="${SPECIALS_DIR}/skills"`,
		`GEMINI_MD_FILE="${OUT_DIR}/GEMINI.md"`,
		`TEMPLATE_ROOT="${NEREID_GEMINI_TEMPLATE_ROOT:-/opt/nereid/gemini-workspace}"`,
		`Gemini workspace template missing: ${TEMPLATE_ROOT}/.gemini`,
		`Gemini workspace template missing: ${TEMPLATE_ROOT}/GEMINI.md`,
		`cp -a "${TEMPLATE_ROOT}/." "${OUT_DIR}/"`,
		`rm -rf "${OUT_DIR}/node_modules" "${OUT_DIR}/dist"`,
		`chmod +x "${OUT_DIR}/.gemini/hooks/"*.sh 2>/dev/null || true`,
		`GEMINI_CLI_MODEL="${NEREID_GEMINI_MODEL:-${GEMINI_MODEL:-gemini-2.5-pro}}"`,
		`--model "${GEMINI_CLI_MODEL}"`,
		`YOLO mode is enabled\. All tool calls will be automatically approved\.`,
		`WARNING: The following project-level hooks have been detected in this workspace:`,
		`Hook registry initialized with [0-9][0-9]* hook entries`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("geminiAgentScript() missing %q:\n%s", needle, script)
		}
	}

	for _, needle := range []string{
		`GEMINI_SKILL_FILE=`,
		`CREATE_SKILLS_SKILL_FILE=`,
		`KIND_OSMABLE_SKILL_FILE=`,
		`INDEX_VALIDATE_HOOK_FILE=`,
		`create_npx_wrapper`,
		`apt-get install -y -qq --no-install-recommends procps curl wget ca-certificates git`,
	} {
		if strings.Contains(script, needle) {
			t.Fatalf("geminiAgentScript() must not embed runtime assets %q:\n%s", needle, script)
		}
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
