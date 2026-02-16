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
	if !strings.Contains(script, "@./.gemini/skills/nereid-artifact-authoring/SKILL.md") {
		t.Fatalf("geminiAgentScript() missing skill import in GEMINI.md: %q", script)
	}
	if !strings.Contains(script, "@./.gemini/skills/overpassql-map-v1/SKILL.md") {
		t.Fatalf("geminiAgentScript() missing overpass strategy skill import: %q", script)
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
	if !strings.Contains(script, "MUST NOT use Gemini web_fetch for HTTP API calls") {
		t.Fatalf("geminiAgentScript() missing web_fetch prohibition in GEMINI.md: %q", script)
	}
	if !strings.Contains(script, "https://nominatim.yuiseki.net/search.php?format=jsonv2&limit=1&q=<url-encoded-query>") {
		t.Fatalf("geminiAgentScript() missing strict Nominatim URL template: %q", script)
	}
	if !strings.Contains(script, "apt-get install -y -qq --no-install-recommends procps") {
		t.Fatalf("geminiAgentScript() missing pgrep bootstrap for slim image: %q", script)
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

func TestGenerateWorkIDv7ReturnsErrorWhenGeneratorFails(t *testing.T) {
	old := newUUIDv7Func
	newUUIDv7Func = func() (uuid.UUID, error) { return uuid.UUID{}, errors.New("boom") }
	t.Cleanup(func() { newUUIDv7Func = old })

	if _, err := generateWorkIDv7(); err == nil {
		t.Fatal("generateWorkIDv7() expected error, got nil")
	}
}
