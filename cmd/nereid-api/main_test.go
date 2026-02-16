package main

import "testing"

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
