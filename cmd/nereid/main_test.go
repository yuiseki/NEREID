package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"sigs.k8s.io/yaml"
)

func TestRunSubmitBuildsKubectlArgs(t *testing.T) {
	argsFile, stdinFile := setupFakeKubectl(t, 0)
	specPath := writeWorkSpec(t, "overpass-parks-tokyo")

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runSubmit([]string{specPath, "-n", "nereid"})
	})
	if runErr != nil {
		t.Fatalf("runSubmit() error = %v", runErr)
	}
	got := readLines(t, argsFile)
	want := []string{"create", "-f", "-", "-n", "nereid"}
	assertLinesEqual(t, got, want)

	stdin := readFile(t, stdinFile)
	var obj map[string]interface{}
	if err := yaml.Unmarshal([]byte(stdin), &obj); err != nil {
		t.Fatalf("parse kubectl stdin yaml: %v", err)
	}
	meta := obj["metadata"].(map[string]interface{})
	gotName, _ := meta["name"].(string)
	assertUUIDv7WorkName(t, gotName)
	if !strings.Contains(stderr, fmt.Sprintf("artifactUrl=http://nereid-artifacts.yuiseki.com/%s/", gotName)) {
		t.Fatalf("runSubmit() stderr did not include artifact URL, got:\n%s", stderr)
	}
}

func TestRunSubmitSupportsGrantFlagInjectsGrantRef(t *testing.T) {
	argsFile, stdinFile := setupFakeKubectl(t, 0)
	specPath := writeWorkSpec(t, "overpass-parks-tokyo")

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runSubmit([]string{specPath, "--grant", "demo-grant", "-n", "nereid"})
	})
	if runErr != nil {
		t.Fatalf("runSubmit() error = %v", runErr)
	}
	got := readLines(t, argsFile)
	want := []string{"create", "-f", "-", "-n", "nereid"}
	assertLinesEqual(t, got, want)

	stdin := readFile(t, stdinFile)
	var obj map[string]interface{}
	if err := yaml.Unmarshal([]byte(stdin), &obj); err != nil {
		t.Fatalf("parse kubectl stdin yaml: %v", err)
	}
	spec := obj["spec"].(map[string]interface{})
	grantRef := spec["grantRef"].(map[string]interface{})
	if gotName := grantRef["name"]; gotName != "demo-grant" {
		t.Fatalf("spec.grantRef.name mismatch got=%v want=%q", gotName, "demo-grant")
	}
	meta := obj["metadata"].(map[string]interface{})
	workName, _ := meta["name"].(string)
	assertUUIDv7WorkName(t, workName)
	if !strings.Contains(stderr, fmt.Sprintf("artifactUrl=http://nereid-artifacts.yuiseki.com/%s/", workName)) {
		t.Fatalf("runSubmit() stderr did not include artifact URL, got:\n%s", stderr)
	}
}

func TestRunWatchBuildsKubectlArgs(t *testing.T) {
	argsFile, _ := setupFakeKubectl(t, 0)

	err := runWatch([]string{"overpass-parks-tokyo", "-n", "nereid"})
	if err != nil {
		t.Fatalf("runWatch() error = %v", err)
	}

	got := readLines(t, argsFile)
	want := []string{
		"get",
		"work",
		"overpass-parks-tokyo",
		"-w",
		"-o",
		"custom-columns=NAME:.metadata.name,PHASE:.status.phase,ARTIFACT:.status.artifactUrl",
		"-n",
		"nereid",
	}
	assertLinesEqual(t, got, want)
}

func TestPlanWorksFromInstructionTextSupportsRequestedFiveLines(t *testing.T) {
	text := `- 東京都台東区の公園を表示してくだい。
- 東京都台東区と東京都文京区と東京都江東区のセブンイレブンとファミリーマートとローソンを表示してください。
- 国の名前を青色にしてください。川の名前を黄色にしてください。
- 人口密度が一番高い国を表示してください。
- 日本から一番近い国を表示してください。`

	plans, err := planWorksFromInstructionText(text)
	if err != nil {
		t.Fatalf("planWorksFromInstructionText() error = %v", err)
	}
	if len(plans) != 5 {
		t.Fatalf("plan count mismatch got=%d want=5", len(plans))
	}

	wantKinds := []string{
		"overpassql.map.v1",
		"overpassql.map.v1",
		"maplibre.style.v1",
		"overpassql.map.v1",
		"maplibre.style.v1",
	}
	for i := range wantKinds {
		gotKind, _ := plans[i].spec["kind"].(string)
		if gotKind != wantKinds[i] {
			t.Fatalf("plan[%d] kind mismatch got=%q want=%q", i, gotKind, wantKinds[i])
		}
	}

	firstQuery := plans[0].spec["overpass"].(map[string]interface{})["query"].(string)
	if !strings.Contains(firstQuery, `"admin_level"="7"`) {
		t.Fatalf("first query should target admin_level=7, got:\n%s", firstQuery)
	}
	secondQuery := plans[1].spec["overpass"].(map[string]interface{})["query"].(string)
	if !strings.Contains(secondQuery, `"admin_level"="7"`) {
		t.Fatalf("second query should target admin_level=7, got:\n%s", secondQuery)
	}

	fifthStyle := plans[4].spec["style"].(map[string]interface{})["sourceStyle"].(map[string]interface{})["json"].(string)
	if !strings.Contains(fifthStyle, "Russia") {
		t.Fatalf("fifth style should reference Russia highlight, got:\n%s", fifthStyle)
	}
}

func TestRunPromptBuildsKubectlArgsAndGeneratedWork(t *testing.T) {
	argsFile, stdinFile := setupFakeKubectl(t, 0)
	t.Setenv("NEREID_PROMPT_PLANNER", "rules")

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runPrompt([]string{"東京都台東区の公園を表示してくだい。", "-n", "nereid", "--dry-run=server", "-o", "name"})
	})
	if runErr != nil {
		t.Fatalf("runPrompt() error = %v", runErr)
	}
	got := readLines(t, argsFile)
	want := []string{"create", "-f", "-", "-n", "nereid", "--dry-run=server", "-o", "name"}
	assertLinesEqual(t, got, want)

	stdin := readFile(t, stdinFile)
	var obj map[string]interface{}
	if err := yaml.Unmarshal([]byte(stdin), &obj); err != nil {
		t.Fatalf("parse kubectl stdin yaml: %v", err)
	}
	meta := obj["metadata"].(map[string]interface{})
	workName, _ := meta["name"].(string)
	assertUUIDv7WorkName(t, workName)
	annotations, _ := meta["annotations"].(map[string]interface{})
	if annotations == nil {
		t.Fatal("metadata.annotations should be set")
	}
	if gotPrompt := annotations[userPromptAnnotationKey]; gotPrompt != "東京都台東区の公園を表示してくだい。" {
		t.Fatalf("metadata.annotations[%q] mismatch got=%v", userPromptAnnotationKey, gotPrompt)
	}
	if !strings.Contains(stderr, fmt.Sprintf("artifactUrl=http://nereid-artifacts.yuiseki.com/%s/", workName)) {
		t.Fatalf("runPrompt() stderr did not include artifact URL, got:\n%s", stderr)
	}
	spec := obj["spec"].(map[string]interface{})
	if gotKind := spec["kind"]; gotKind != "overpassql.map.v1" {
		t.Fatalf("spec.kind mismatch got=%v", gotKind)
	}
}

func TestRunPromptSupportsGrantFlagInjectsGrantRef(t *testing.T) {
	argsFile, stdinFile := setupFakeKubectl(t, 0)
	t.Setenv("NEREID_PROMPT_PLANNER", "rules")

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runPrompt([]string{"東京都台東区の公園を表示してくだい。", "--grant", "demo-grant", "-n", "nereid", "--dry-run=server", "-o", "name"})
	})
	if runErr != nil {
		t.Fatalf("runPrompt() error = %v", runErr)
	}
	got := readLines(t, argsFile)
	want := []string{"create", "-f", "-", "-n", "nereid", "--dry-run=server", "-o", "name"}
	assertLinesEqual(t, got, want)

	stdin := readFile(t, stdinFile)
	var obj map[string]interface{}
	if err := yaml.Unmarshal([]byte(stdin), &obj); err != nil {
		t.Fatalf("parse kubectl stdin yaml: %v", err)
	}
	spec := obj["spec"].(map[string]interface{})
	grantRef := spec["grantRef"].(map[string]interface{})
	if gotName := grantRef["name"]; gotName != "demo-grant" {
		t.Fatalf("spec.grantRef.name mismatch got=%v want=%q", gotName, "demo-grant")
	}
	meta := obj["metadata"].(map[string]interface{})
	workName, _ := meta["name"].(string)
	assertUUIDv7WorkName(t, workName)
	if !strings.Contains(stderr, fmt.Sprintf("artifactUrl=http://nereid-artifacts.yuiseki.com/%s/", workName)) {
		t.Fatalf("runPrompt() stderr did not include artifact URL, got:\n%s", stderr)
	}
}

func TestParsePlannerWorksAcceptsCodeFenceJSON(t *testing.T) {
	content := "```json\n{\"works\":[{\"baseName\":\"demo\",\"spec\":{\"kind\":\"overpassql.map.v1\",\"title\":\"x\",\"overpass\":{\"endpoint\":\"https://overpass-api.de/api/interpreter\",\"query\":\"[out:json];node(1,2,3,4);out;\"}}}]}\n```"
	plans, err := parsePlannerWorks(content)
	if err != nil {
		t.Fatalf("parsePlannerWorks() error = %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plan count mismatch got=%d want=1", len(plans))
	}
	if plans[0].baseName != "demo" {
		t.Fatalf("baseName mismatch got=%q", plans[0].baseName)
	}
}

func TestParsePlannerWorksNormalizesStyleModeJSON(t *testing.T) {
	content := `{"works":[{"baseName":"style-demo","spec":{"kind":"maplibre.style.v1","title":"style","style":{"sourceStyle":{"mode":"json","json":"{\"version\":8,\"sources\":{},\"layers\":[]}"}}}}]}`
	plans, err := parsePlannerWorks(content)
	if err != nil {
		t.Fatalf("parsePlannerWorks() error = %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plan count mismatch got=%d want=1", len(plans))
	}
	style := plans[0].spec["style"].(map[string]interface{})
	sourceStyle := style["sourceStyle"].(map[string]interface{})
	if got := sourceStyle["mode"]; got != "inline" {
		t.Fatalf("normalized mode mismatch got=%v want=inline", got)
	}
}

func TestParsePlannerWorksAcceptsAgentCLIKind(t *testing.T) {
	content := `{"works":[{"baseName":"agent-demo","spec":{"kind":"agent.cli.v1","title":"agent","agent":{"image":"node:22-bookworm-slim","command":["npx","-y","@google/gemini-cli","--help"]}}}]}`
	plans, err := parsePlannerWorks(content)
	if err != nil {
		t.Fatalf("parsePlannerWorks() error = %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plan count mismatch got=%d want=1", len(plans))
	}
	if got, _ := plans[0].spec["kind"].(string); got != "agent.cli.v1" {
		t.Fatalf("kind mismatch got=%q", got)
	}
}

func TestParsePlannerWorksNormalizesAgentCommandString(t *testing.T) {
	content := `{"works":[{"baseName":"agent-demo","spec":{"kind":"agent.cli.v1","title":"agent","agent":{"image":"node:22-bookworm-slim","command":"npx -y @google/gemini-cli --help"}}}]}`
	plans, err := parsePlannerWorks(content)
	if err != nil {
		t.Fatalf("parsePlannerWorks() error = %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plan count mismatch got=%d want=1", len(plans))
	}
	if err := validatePlannedSpec(plans[0].spec); err != nil {
		t.Fatalf("validatePlannedSpec() error = %v", err)
	}
}

func TestParsePlannerWorksNormalizesAgentArgsJSONString(t *testing.T) {
	content := `{"works":[{"baseName":"agent-demo","spec":{"kind":"agent.cli.v1","title":"agent","agent":{"image":"node:22-bookworm-slim","command":"npx","args":"[\"-y\",\"@google/gemini-cli\",\"--version\"]"}}}]}`
	plans, err := parsePlannerWorks(content)
	if err != nil {
		t.Fatalf("parsePlannerWorks() error = %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plan count mismatch got=%d want=1", len(plans))
	}
	if err := validatePlannedSpec(plans[0].spec); err != nil {
		t.Fatalf("validatePlannedSpec() error = %v", err)
	}
}

func TestPlanWorksWithLLMUsesOpenAICompatibleEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"works\":[{\"baseName\":\"llm-plan\",\"spec\":{\"kind\":\"overpassql.map.v1\",\"title\":\"from llm\",\"overpass\":{\"endpoint\":\"https://overpass-api.de/api/interpreter\",\"query\":\"[out:json][timeout:120];node(35.6,139.7,35.7,139.8);out;\"},\"constraints\":{\"deadlineSeconds\":120},\"artifacts\":{\"layout\":\"map\"}}}]}"}}]}`))
	}))
	defer server.Close()

	t.Setenv("NEREID_OPENAI_API_KEY", "test-key")
	t.Setenv("NEREID_LLM_BASE_URL", server.URL)
	t.Setenv("NEREID_LLM_MODEL", "test-model")

	plans, err := planWorksWithLLM(context.Background(), "東京都台東区の公園")
	if err != nil {
		t.Fatalf("planWorksWithLLM() error = %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plan count mismatch got=%d want=1", len(plans))
	}
	if plans[0].baseName != "llm-plan" {
		t.Fatalf("baseName mismatch got=%q want=%q", plans[0].baseName, "llm-plan")
	}
	if got, _ := plans[0].spec["kind"].(string); got != "overpassql.map.v1" {
		t.Fatalf("kind mismatch got=%q", got)
	}
}

func TestPlannerAPIKeyFallsBackToGemini(t *testing.T) {
	t.Setenv("NEREID_OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("NEREID_GEMINI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gemini-key")

	if got := plannerAPIKey(); got != "gemini-key" {
		t.Fatalf("plannerAPIKey() got=%q want=%q", got, "gemini-key")
	}
}

func TestPlannerBaseURLDefaultsToGeminiWhenGeminiKeySet(t *testing.T) {
	t.Setenv("NEREID_LLM_BASE_URL", "")
	t.Setenv("NEREID_OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("NEREID_GEMINI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gemini-key")

	if got := plannerBaseURL(); got != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Fatalf("plannerBaseURL() got=%q", got)
	}
}

func TestPlannerModelDefaultsToGeminiWhenGeminiKeySet(t *testing.T) {
	t.Setenv("NEREID_LLM_MODEL", "")
	t.Setenv("NEREID_GEMINI_MODEL", "")
	t.Setenv("GEMINI_MODEL", "")
	t.Setenv("NEREID_OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("NEREID_GEMINI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gemini-key")

	if got := plannerModel(); got != "gemini-2.0-flash" {
		t.Fatalf("plannerModel() got=%q want=%q", got, "gemini-2.0-flash")
	}
}

func TestPlanWorksWithPlannerAutoPrefersRulesEvenWhenKeySet(t *testing.T) {
	t.Setenv("NEREID_PROMPT_PLANNER", "auto")
	t.Setenv("NEREID_OPENAI_API_KEY", "test-key")
	// If auto incorrectly tries LLM first, this invalid base URL would make the test fail.
	t.Setenv("NEREID_LLM_BASE_URL", "http://127.0.0.1:1")

	plans, err := planWorksWithPlanner(context.Background(), "東京都台東区の公園を表示してくだい。")
	if err != nil {
		t.Fatalf("planWorksWithPlanner() error = %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plan count mismatch got=%d want=1", len(plans))
	}
	if plans[0].baseName != "taito-parks" {
		t.Fatalf("baseName mismatch got=%q want=%q", plans[0].baseName, "taito-parks")
	}
}

func TestPlanWorksWithPlannerAutoUsesLLMWhenRulesFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"works\":[{\"baseName\":\"llm-fallback\",\"spec\":{\"kind\":\"overpassql.map.v1\",\"title\":\"from llm\",\"overpass\":{\"endpoint\":\"https://overpass-api.de/api/interpreter\",\"query\":\"[out:json];node(35.6,139.7,35.7,139.8);out;\"}}}]}"}}]}`))
	}))
	defer server.Close()

	t.Setenv("NEREID_PROMPT_PLANNER", "auto")
	t.Setenv("NEREID_OPENAI_API_KEY", "test-key")
	t.Setenv("NEREID_LLM_BASE_URL", server.URL)
	t.Setenv("NEREID_LLM_MODEL", "test-model")

	plans, err := planWorksWithPlanner(context.Background(), "大阪市の公園を表示してください。")
	if err != nil {
		t.Fatalf("planWorksWithPlanner() error = %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plan count mismatch got=%d want=1", len(plans))
	}
	if plans[0].baseName != "llm-fallback" {
		t.Fatalf("baseName mismatch got=%q want=%q", plans[0].baseName, "llm-fallback")
	}
}

func TestPlanWorkFromInstructionLineRejectsUnknownText(t *testing.T) {
	_, err := planWorkFromInstructionLine("これは地図の指示ではないテキストです")
	if err == nil {
		t.Fatal("planWorkFromInstructionLine() expected error, got nil")
	}
}

func TestRunHelpPrintsUsage(t *testing.T) {
	out := captureStdout(t, func() {
		if err := run([]string{"--help"}); err != nil {
			t.Fatalf("run(--help) error = %v", err)
		}
	})

	if !strings.Contains(out, "nereid submit") || !strings.Contains(out, "nereid prompt") {
		t.Fatalf("help output did not include expected usage, got:\n%s", out)
	}
}

func TestRunKubectlNonZeroExitReturnsError(t *testing.T) {
	_, _ = setupFakeKubectl(t, 7)

	err := runKubectl("get", "work")
	if err == nil {
		t.Fatal("runKubectl() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "kubectl [get work] failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildTimestampedNameReturnsUUIDv7(t *testing.T) {
	got := buildTimestampedName("Overpass Parks TOKYO!!", time.Date(2026, 2, 15, 6, 33, 13, 0, time.UTC))
	assertUUIDv7WorkName(t, got)
}

func TestGenerateWorkIDv7(t *testing.T) {
	got, err := generateWorkIDv7()
	if err != nil {
		t.Fatalf("generateWorkIDv7() error = %v", err)
	}
	assertUUIDv7WorkName(t, got)
}

func TestGenerateWorkIDv7ReturnsErrorWhenGeneratorFails(t *testing.T) {
	old := newUUIDv7Func
	newUUIDv7Func = func() (uuid.UUID, error) { return uuid.UUID{}, errors.New("boom") }
	t.Cleanup(func() { newUUIDv7Func = old })

	if _, err := generateWorkIDv7(); err == nil {
		t.Fatal("generateWorkIDv7() expected error, got nil")
	}
}

func TestArtifactURLForWorkRespectsEnv(t *testing.T) {
	t.Setenv("NEREID_ARTIFACT_BASE_URL", "https://example.invalid/base/")
	got := artifactURLForWork("abc-123")
	want := "https://example.invalid/base/abc-123/"
	if got != want {
		t.Fatalf("artifactURLForWork() got=%q want=%q", got, want)
	}
}

func TestUserPromptAnnotationValueTrimsAndTruncates(t *testing.T) {
	in := strings.Repeat("x", maxUserPromptBytes+64)
	got := userPromptAnnotationValue("  " + in + "  ")
	if got == "" {
		t.Fatal("userPromptAnnotationValue() returned empty")
	}
	if len([]byte(got)) != maxUserPromptBytes {
		t.Fatalf("annotation length got=%d want=%d", len([]byte(got)), maxUserPromptBytes)
	}
}

func assertUUIDv7WorkName(t *testing.T, workName string) {
	t.Helper()
	if workName == "" {
		t.Fatal("workName is empty")
	}
	id, err := uuid.Parse(workName)
	if err != nil {
		t.Fatalf("workName %q is not uuid: %v", workName, err)
	}
	if got := id.Version(); got != 7 {
		t.Fatalf("workName %q version got=%d want=7", workName, got)
	}
}

func writeWorkSpec(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "work.yaml")
	content := `apiVersion: nereid.yuiseki.net/v1alpha1
kind: Work
metadata:
  name: ` + name + `
spec:
  kind: overpassql.map.v1
  title: "test"
  overpass:
    endpoint: "https://overpass-api.de/api/interpreter"
    query: |
      [out:json];
      node(35.6,139.7,35.7,139.8);
      out;
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write work spec: %v", err)
	}
	return path
}

func setupFakeKubectl(t *testing.T, exitCode int) (string, string) {
	t.Helper()

	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "kubectl-args.txt")
	stdinFile := filepath.Join(tmp, "kubectl-stdin.txt")
	script := filepath.Join(tmp, "kubectl")
	content := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$KUBECTL_ARGS_FILE"
if [ -n "${KUBECTL_STDIN_FILE:-}" ]; then
  cat > "$KUBECTL_STDIN_FILE"
fi
exit "${KUBECTL_EXIT_CODE:-0}"
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}

	t.Setenv("KUBECTL_ARGS_FILE", argsFile)
	t.Setenv("KUBECTL_STDIN_FILE", stdinFile)
	t.Setenv("KUBECTL_EXIT_CODE", strconv.Itoa(exitCode))
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile, stdinFile
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	raw := strings.Split(strings.TrimSpace(string(data)), "\n")
	lines := make([]string, 0, len(raw))
	for _, v := range raw {
		if v != "" {
			lines = append(lines, v)
		}
	}
	return lines
}

func assertLinesEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("line count mismatch got=%d want=%d\n got=%v\nwant=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line[%d] mismatch got=%q want=%q", i, got[i], want[i])
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(out)
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return string(out)
}
