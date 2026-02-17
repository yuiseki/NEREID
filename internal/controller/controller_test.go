package controller

import (
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMakeJobNameStableAndBounded(t *testing.T) {
	name := makeJobName(strings.Repeat("x", 120))
	if len(name) > 63 {
		t.Fatalf("job name length exceeded: %d", len(name))
	}
	if !strings.HasPrefix(name, "work-") {
		t.Fatalf("job name prefix mismatch: %s", name)
	}
}

func TestLegacyKindAgentImageForJobPrefersLegacyOverride(t *testing.T) {
	t.Setenv("NEREID_AGENT_IMAGE", "ghcr.io/yuiseki/nereid-agent-runtime:base")
	t.Setenv("NEREID_LEGACY_AGENT_IMAGE", "ghcr.io/yuiseki/nereid-agent-runtime:legacy")
	if got := legacyKindAgentImageForJob(); got != "ghcr.io/yuiseki/nereid-agent-runtime:legacy" {
		t.Fatalf("legacyKindAgentImageForJob() got=%q", got)
	}
}

func TestLegacyKindAgentImageForJobFallsBackToSharedOverride(t *testing.T) {
	t.Setenv("NEREID_AGENT_IMAGE", "ghcr.io/yuiseki/nereid-agent-runtime:base")
	t.Setenv("NEREID_LEGACY_AGENT_IMAGE", "")
	if got := legacyKindAgentImageForJob(); got != "ghcr.io/yuiseki/nereid-agent-runtime:base" {
		t.Fatalf("legacyKindAgentImageForJob() got=%q", got)
	}
}

func TestLegacyKindAgentImageForJobDefaults(t *testing.T) {
	t.Setenv("NEREID_AGENT_IMAGE", "")
	t.Setenv("NEREID_LEGACY_AGENT_IMAGE", "")
	if got := legacyKindAgentImageForJob(); got != legacyKindAgentImage {
		t.Fatalf("legacyKindAgentImageForJob() got=%q want=%q", got, legacyKindAgentImage)
	}
}

func TestIsTerminalWorkPhase(t *testing.T) {
	tests := []struct {
		phase string
		want  bool
	}{
		{phase: "", want: false},
		{phase: "Submitted", want: false},
		{phase: "Queued", want: false},
		{phase: "Running", want: false},
		{phase: "Succeeded", want: true},
		{phase: "Failed", want: true},
		{phase: "Error", want: true},
		{phase: "Canceled", want: true},
		{phase: "Cancelled", want: true},
	}
	for _, tt := range tests {
		if got := isTerminalWorkPhase(tt.phase); got != tt.want {
			t.Fatalf("isTerminalWorkPhase(%q)=%v want=%v", tt.phase, got, tt.want)
		}
	}
}

func TestPruneArtifactsRemovesEntriesOlderThanRetention(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "old-work")
	newPath := filepath.Join(root, "new-work")
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatalf("mkdir old: %v", err)
	}
	if err := os.MkdirAll(newPath, 0o755); err != nil {
		t.Fatalf("mkdir new: %v", err)
	}

	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	oldTime := now.Add(-31 * 24 * time.Hour)
	newTime := now.Add(-5 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatalf("chtimes new: %v", err)
	}

	c := &Controller{
		cfg: Config{
			ArtifactsHostPath: root,
			ArtifactRetention: 30 * 24 * time.Hour,
		},
		logger:  slog.Default(),
		nowFunc: func() time.Time { return now },
	}

	if err := c.pruneArtifacts(); err != nil {
		t.Fatalf("pruneArtifacts() error = %v", err)
	}

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old path should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new path should remain, stat err=%v", err)
	}
}

func TestValidateSucceededWorkArtifactsFailsWhenIndexMissing(t *testing.T) {
	root := t.TempDir()
	workName := "work-no-index"
	if err := os.MkdirAll(filepath.Join(root, workName), 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	c := &Controller{
		cfg: Config{
			ArtifactsHostPath: root,
		},
	}

	msg, err := c.validateSucceededWorkArtifacts(workName)
	if err != nil {
		t.Fatalf("validateSucceededWorkArtifacts() error = %v", err)
	}
	if !strings.Contains(msg, "index.html not found") {
		t.Fatalf("validateSucceededWorkArtifacts() msg=%q want contains %q", msg, "index.html not found")
	}
}

func TestValidateSucceededWorkArtifactsFailsOnRuntimeSignature(t *testing.T) {
	root := t.TempDir()
	workName := "work-runtime-error"
	workDir := filepath.Join(root, workName)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "index.html"), []byte("<!doctype html><html><body>ok</body></html>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "gemini-output.txt"), []byte("TypeError: Cannot read properties of undefined (reading 'lon')"), 0o644); err != nil {
		t.Fatalf("write gemini-output.txt: %v", err)
	}

	c := &Controller{
		cfg: Config{
			ArtifactsHostPath: root,
		},
	}

	msg, err := c.validateSucceededWorkArtifacts(workName)
	if err != nil {
		t.Fatalf("validateSucceededWorkArtifacts() error = %v", err)
	}
	if !strings.Contains(msg, "reading 'lon'") {
		t.Fatalf("validateSucceededWorkArtifacts() msg=%q want runtime signature", msg)
	}
}

func TestValidateSucceededWorkArtifactsPassesWhenNoKnownRuntimeSignature(t *testing.T) {
	root := t.TempDir()
	workName := "work-clean"
	workDir := filepath.Join(root, workName)
	if err := os.MkdirAll(filepath.Join(workDir, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "index.html"), []byte("<!doctype html><html><body>ok</body></html>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "agent.log"), []byte("all good"), 0o644); err != nil {
		t.Fatalf("write agent.log: %v", err)
	}

	c := &Controller{
		cfg: Config{
			ArtifactsHostPath: root,
		},
	}

	msg, err := c.validateSucceededWorkArtifacts(workName)
	if err != nil {
		t.Fatalf("validateSucceededWorkArtifacts() error = %v", err)
	}
	if msg != "" {
		t.Fatalf("validateSucceededWorkArtifacts() msg=%q want empty", msg)
	}
}

func TestBuildJobLegacyKindsBridgeToGeminiAgent(t *testing.T) {
	kinds := []string{
		"overpassql.map.v1",
		"maplibre.style.v1",
		"duckdb.map.v1",
		"gdal.rastertile.v1",
		"laz.3dtiles.v1",
	}
	kindSkills := map[string]string{
		"overpassql.map.v1":  "overpassql-map",
		"maplibre.style.v1":  "maplibre-style",
		"duckdb.map.v1":      "duckdb-map",
		"gdal.rastertile.v1": "gdal-rastertile",
		"laz.3dtiles.v1":     "laz-3dtiles",
	}

	for _, legacyKind := range kinds {
		t.Run(legacyKind, func(t *testing.T) {
			work := &unstructured.Unstructured{Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":      "legacy-kind-sample",
					"namespace": "nereid",
				},
				"spec": map[string]interface{}{
					"kind":  legacyKind,
					"title": "legacy sample",
				},
			}}

			c := &Controller{
				cfg: Config{
					JobNamespace:      "nereid-work",
					LocalQueueName:    "nereid-localq",
					ArtifactsHostPath: "/var/lib/nereid/artifacts",
				},
			}

			job, err := c.buildJob(work, "work-legacy-kind-sample", legacyKind)
			if err != nil {
				t.Fatalf("buildJob() error = %v", err)
			}
			if got := job.Spec.Template.Spec.Containers[0].Image; got != legacyKindAgentImage {
				t.Fatalf("unexpected image got=%q want=%q", got, legacyKindAgentImage)
			}

			wrapper := job.Spec.Template.Spec.Containers[0].Args[0]
			for _, needle := range []string{
				"SCRIPT_B64=",
				"user-input.txt",
				"dialogue.txt",
				"agent.log",
				"SPECIALS_DIR=\"${OUT_DIR}/specials\"",
				"SPECIALS_SKILLS_DIR=\"${SPECIALS_DIR}/skills\"",
				"https://nereid.yuiseki.net/works/legacy-kind-sample",
			} {
				if !strings.Contains(wrapper, needle) {
					t.Fatalf("wrapper script missing %q\nscript:\n%s", needle, wrapper)
				}
			}

			embedded := decodeEmbeddedAgentScript(t, wrapper)
			kindSkill := kindSkills[legacyKind]
			for _, needle := range []string{
				"legacy-work-spec.json",
				"GEMINI_MD_FILE",
				"@google/gemini-cli",
				"GEMINI_CLI_MODEL=\"${NEREID_GEMINI_MODEL:-${GEMINI_MODEL:-gemini-2.5-pro}}\"",
				"--model \"${GEMINI_CLI_MODEL}\"",
				"WARNING: The following project-level hooks have been detected in this workspace:",
				"legacy-kind-prompt.txt",
				"KIND_SKILL_FILE=\"${OUT_DIR}/.gemini/skills/" + kindSkill + "/SKILL.md\"",
				"TEMPLATE_ROOT=\"${NEREID_GEMINI_TEMPLATE_ROOT:-/opt/nereid/gemini-workspace}\"",
				"Gemini workspace template missing: ${TEMPLATE_ROOT}/.gemini",
				"Gemini workspace template missing: ${TEMPLATE_ROOT}/GEMINI.md",
				"cp -a \"${TEMPLATE_ROOT}/.\" \"${OUT_DIR}/\"",
				"rm -rf \"${OUT_DIR}/node_modules\" \"${OUT_DIR}/dist\"",
			} {
				if !strings.Contains(embedded, needle) {
					t.Fatalf("embedded script missing %q\nscript:\n%s", needle, embedded)
				}
			}
			prompt := decodeEmbeddedB64Var(t, embedded, "PROMPT_B64")
			if !strings.Contains(prompt, "Primary skill: "+kindSkill) {
				t.Fatalf("prompt missing primary skill hint %q\nprompt:\n%s", kindSkill, prompt)
			}
			if !strings.Contains(prompt, "especially "+kindSkill+".") {
				t.Fatalf("prompt missing skill activation hint %q\nprompt:\n%s", kindSkill, prompt)
			}
			if !strings.Contains(prompt, "make build") {
				t.Fatalf("prompt missing make build reference\nprompt:\n%s", prompt)
			}
			for _, needle := range []string{
				"OSMABLE_SKILL_B64=",
				"create_npx_wrapper",
				"INDEX_VALIDATE_HOOK_FILE=",
				"apt-get install -y -qq --no-install-recommends procps curl wget ca-certificates git",
			} {
				if strings.Contains(embedded, needle) {
					t.Fatalf("embedded script should not contain %q\nscript:\n%s", needle, embedded)
				}
			}
		})
	}
}

func decodeEmbeddedAgentScript(t *testing.T, wrapper string) string {
	t.Helper()
	const marker = "SCRIPT_B64=\""
	start := strings.Index(wrapper, marker)
	if start < 0 {
		t.Fatalf("SCRIPT_B64 marker not found in wrapper script:\n%s", wrapper)
	}
	start += len(marker)
	end := strings.Index(wrapper[start:], "\"")
	if end < 0 {
		t.Fatalf("SCRIPT_B64 closing quote not found in wrapper script:\n%s", wrapper)
	}
	b64 := wrapper[start : start+end]
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode SCRIPT_B64: %v", err)
	}
	return string(decoded)
}

func decodeEmbeddedB64Var(t *testing.T, script, varName string) string {
	t.Helper()
	marker := varName + "=\""
	start := strings.Index(script, marker)
	if start < 0 {
		t.Fatalf("%s marker not found in script:\n%s", varName, script)
	}
	start += len(marker)
	end := strings.Index(script[start:], "\"")
	if end < 0 {
		t.Fatalf("%s closing quote not found in script:\n%s", varName, script)
	}
	b64 := script[start : start+end]
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode %s: %v", varName, err)
	}
	return string(decoded)
}

func TestBuildJobAgentCLIGeneratesCommandWrapperScript(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "agent-cli-sample",
			"namespace": "nereid",
			"annotations": map[string]interface{}{
				userPromptAnnotationKey: "東京都台東区の公園を表示してください。",
			},
		},
		"spec": map[string]interface{}{
			"kind":  "agent.cli.v1",
			"title": "agent sample",
			"agent": map[string]interface{}{
				"image": "node:22-bookworm-slim",
				"command": []interface{}{
					"npx",
					"-y",
					"@google/gemini-cli",
					"--help",
				},
			},
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
	}

	job, err := c.buildJob(work, "work-agent-cli-sample", "agent.cli.v1")
	if err != nil {
		t.Fatalf("buildJob() error = %v", err)
	}
	if got := job.Spec.Template.Spec.Containers[0].Image; got != "node:22-bookworm-slim" {
		t.Fatalf("unexpected image got=%q want=%q", got, "node:22-bookworm-slim")
	}
	script := job.Spec.Template.Spec.Containers[0].Args[0]
	for _, needle := range []string{
		"NEREID_WORK_NAME",
		"NEREID_ARTIFACT_DIR",
		"LOGS_DIR=\"${OUT_DIR}/logs\"",
		"SPECIALS_DIR=\"${OUT_DIR}/specials\"",
		"SPECIALS_SKILLS_DIR=\"${SPECIALS_DIR}/skills\"",
		"start-time.txt",
		"instructions.csv",
		"timestamp_unix,role,text",
		"command.txt",
		"user-input.txt",
		"dialogue.txt",
		"agent.log",
		"./specials/skills/",
		"https://nereid.yuiseki.net/works/agent-cli-sample",
		"'@google/gemini-cli'",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q\nscript:\n%s", needle, script)
		}
	}
}

func TestBuildJobAgentCLIRequiresImage(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "agent-cli-invalid",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kind":  "agent.cli.v1",
			"title": "agent invalid",
			"agent": map[string]interface{}{
				"script": "echo hello",
			},
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
	}

	_, err := c.buildJob(work, "work-agent-cli-invalid", "agent.cli.v1")
	if err == nil {
		t.Fatal("buildJob() expected error for missing image, got nil")
	}
	if !strings.Contains(err.Error(), "spec.agent.image is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildJobUnsupportedKindReturnsError(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "unknown-kind",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kind":  "unknown.kind.v1",
			"title": "unknown",
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
	}

	_, err := c.buildJob(work, "work-unknown-kind", "unknown.kind.v1")
	if err == nil {
		t.Fatal("buildJob() expected error for unsupported kind, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported spec.kind") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApplyGrantToJobOverridesQueueRuntimeResourcesAndEnv(t *testing.T) {
	work := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "overpass-sample",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kind":  "overpassql.map.v1",
			"title": "sample",
			"overpass": map[string]interface{}{
				"endpoint": "https://overpass-api.de/api/interpreter",
				"query":    "[out:json];node(1,2,3,4);out;",
			},
		},
	}}

	c := &Controller{
		cfg: Config{
			JobNamespace:      "nereid-work",
			LocalQueueName:    "nereid-localq",
			RuntimeClassName:  "gvisor",
			ArtifactsHostPath: "/var/lib/nereid/artifacts",
		},
		kube: fake.NewSimpleClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openai",
				Namespace: "nereid",
			},
			Data: map[string][]byte{
				"api-key": []byte("secret-value"),
			},
		}),
	}

	job, err := c.buildJob(work, "work-overpass-sample", "overpassql.map.v1")
	if err != nil {
		t.Fatalf("buildJob() error = %v", err)
	}

	grant := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nereid.yuiseki.net/v1alpha1",
		"kind":       "Grant",
		"metadata": map[string]interface{}{
			"name":      "demo-grant",
			"namespace": "nereid",
		},
		"spec": map[string]interface{}{
			"kueue": map[string]interface{}{
				"localQueueName": "grant-queue",
			},
			"runtimeClassName": "kata",
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{
					"cpu":    "250m",
					"memory": "256Mi",
				},
				"limits": map[string]interface{}{
					"cpu":    "1",
					"memory": "1Gi",
				},
			},
			"env": []interface{}{
				map[string]interface{}{
					"name": "OPENAI_API_KEY",
					"secretKeyRef": map[string]interface{}{
						"name": "openai",
						"key":  "api-key",
					},
				},
			},
		},
	}}

	if err := c.applyGrantToJob(context.Background(), job, grant); err != nil {
		t.Fatalf("applyGrantToJob() error = %v", err)
	}

	if got := job.Labels["kueue.x-k8s.io/queue-name"]; got != "grant-queue" {
		t.Fatalf("queue label mismatch got=%q want=%q", got, "grant-queue")
	}
	if job.Spec.Template.Spec.RuntimeClassName == nil || *job.Spec.Template.Spec.RuntimeClassName != "kata" {
		t.Fatalf("runtimeClassName mismatch got=%v want=%q", job.Spec.Template.Spec.RuntimeClassName, "kata")
	}

	container := job.Spec.Template.Spec.Containers[0]
	cpuReq := container.Resources.Requests[corev1.ResourceCPU]
	if got := cpuReq.String(); got != "250m" {
		t.Fatalf("cpu request mismatch got=%q want=%q", got, "250m")
	}
	memReq := container.Resources.Requests[corev1.ResourceMemory]
	if got := memReq.String(); got != "256Mi" {
		t.Fatalf("memory request mismatch got=%q want=%q", got, "256Mi")
	}
	cpuLim := container.Resources.Limits[corev1.ResourceCPU]
	if got := cpuLim.String(); got != "1" {
		t.Fatalf("cpu limit mismatch got=%q want=%q", got, "1")
	}
	memLim := container.Resources.Limits[corev1.ResourceMemory]
	if got := memLim.String(); got != "1Gi" {
		t.Fatalf("memory limit mismatch got=%q want=%q", got, "1Gi")
	}

	found := false
	for _, ev := range container.Env {
		if ev.Name != "OPENAI_API_KEY" {
			continue
		}
		found = true
		if ev.ValueFrom != nil {
			t.Fatalf("OPENAI_API_KEY should be resolved to a literal value, got=%+v", ev)
		}
		if ev.Value != "secret-value" {
			t.Fatalf("OPENAI_API_KEY value mismatch got=%q want=%q", ev.Value, "secret-value")
		}
	}
	if !found {
		t.Fatal("OPENAI_API_KEY env not injected")
	}
}

func TestExtractViewportDefaultsAndOverrides(t *testing.T) {
	workDefault := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{},
	}}
	lon, lat, zoom := extractViewport(workDefault)
	if lon != 139.76 || lat != 35.68 || zoom != 11 {
		t.Fatalf("defaults mismatch lon=%f lat=%f zoom=%f", lon, lat, zoom)
	}

	workCustom := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"render": map[string]interface{}{
				"viewport": map[string]interface{}{
					"center": []interface{}{140, "36.1"},
					"zoom":   9,
				},
			},
		},
	}}
	lon, lat, zoom = extractViewport(workCustom)
	if lon != 140 || lat != 36.1 || zoom != 9 {
		t.Fatalf("custom mismatch lon=%f lat=%f zoom=%f", lon, lat, zoom)
	}
}

func TestExtractTileZoomRangeDefaultsAndBounds(t *testing.T) {
	workDefault := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{}}}
	minZoom, maxZoom := extractTileZoomRange(workDefault)
	if minZoom != 0 || maxZoom != 14 {
		t.Fatalf("defaults mismatch min=%d max=%d", minZoom, maxZoom)
	}

	workCustom := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"raster": map[string]interface{}{
				"tiles": map[string]interface{}{
					"minZoom": -5,
					"maxZoom": 99,
				},
			},
		},
	}}
	minZoom, maxZoom = extractTileZoomRange(workCustom)
	if minZoom != 0 || maxZoom != 24 {
		t.Fatalf("bounds mismatch min=%d max=%d", minZoom, maxZoom)
	}
}
