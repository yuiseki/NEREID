package controller

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

var workGVR = schema.GroupVersionResource{
	Group:    "nereid.yuiseki.net",
	Version:  "v1alpha1",
	Resource: "works",
}

var grantGVR = schema.GroupVersionResource{
	Group:    "nereid.yuiseki.net",
	Version:  "v1alpha1",
	Resource: "grants",
}

const (
	userPromptAnnotationKey = "nereid.yuiseki.net/user-prompt"

	legacyKindAgentImage = "node:22-bookworm-slim"
)

type Config struct {
	WorkNamespace     string
	JobNamespace      string
	LocalQueueName    string
	RuntimeClassName  string
	ArtifactsHostPath string
	ArtifactBaseURL   string
	ArtifactRetention time.Duration
	ResyncInterval    time.Duration
}

type Controller struct {
	dynamic dynamic.Interface
	kube    kubernetes.Interface
	cfg     Config
	logger  *slog.Logger
	nowFunc func() time.Time
}

func New(dynamicClient dynamic.Interface, kubeClient kubernetes.Interface, cfg Config, logger *slog.Logger) *Controller {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.ArtifactRetention <= 0 {
		cfg.ArtifactRetention = 30 * 24 * time.Hour
	}
	return &Controller{
		dynamic: dynamicClient,
		kube:    kubeClient,
		cfg:     cfg,
		logger:  logger,
		nowFunc: time.Now,
	}
}

func (c *Controller) Run(ctx context.Context) error {
	c.logger.Info("controller started",
		"workNamespace", c.cfg.WorkNamespace,
		"jobNamespace", c.cfg.JobNamespace,
		"localQueueName", c.cfg.LocalQueueName,
	)

	if err := c.reconcileAll(ctx); err != nil {
		c.logger.Error("initial reconcile failed", "error", err)
	}

	ticker := time.NewTicker(c.cfg.ResyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("controller stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := c.reconcileAll(ctx); err != nil {
				c.logger.Error("reconcile loop failed", "error", err)
			}
		}
	}
}

func (c *Controller) reconcileAll(ctx context.Context) error {
	if err := c.pruneArtifacts(); err != nil {
		c.logger.Error("artifact prune failed", "error", err)
	}

	ns := c.cfg.WorkNamespace
	if ns == "" {
		ns = metav1.NamespaceAll
	}

	list, err := c.dynamic.Resource(workGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list works: %w", err)
	}

	for i := range list.Items {
		work := &list.Items[i]
		if err := c.reconcileWork(ctx, work); err != nil {
			c.logger.Error("reconcile work failed",
				"work", work.GetName(),
				"namespace", work.GetNamespace(),
				"error", err,
			)
		}
	}
	return nil
}

func (c *Controller) reconcileWork(ctx context.Context, work *unstructured.Unstructured) error {
	kind, _, err := unstructured.NestedString(work.Object, "spec", "kind")
	if err != nil {
		return c.updateWorkStatus(ctx, work, "Error", fmt.Sprintf("failed to read spec.kind: %v", err), "")
	}

	grantName, _, err := unstructured.NestedString(work.Object, "spec", "grantRef", "name")
	if err != nil {
		return c.updateWorkStatus(ctx, work, "Error", fmt.Sprintf("failed to read spec.grantRef.name: %v", err), "")
	}
	grantName = strings.TrimSpace(grantName)

	var grant *unstructured.Unstructured
	if grantName != "" {
		obj, getErr := c.dynamic.Resource(grantGVR).Namespace(work.GetNamespace()).Get(ctx, grantName, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			return c.updateWorkStatus(ctx, work, "Error", fmt.Sprintf("grant %q not found", grantName), "")
		}
		if getErr != nil {
			return c.updateWorkStatus(ctx, work, "Error", fmt.Sprintf("failed to get grant %q: %v", grantName, getErr), "")
		}
		grant = obj
		if validateErr := c.validateGrantForWork(ctx, work, kind, grant); validateErr != nil {
			return c.updateWorkStatus(ctx, work, "Error", validateErr.Error(), "")
		}
	}

	jobName := makeJobName(work.GetName())
	job, err := c.kube.BatchV1().Jobs(c.cfg.JobNamespace).Get(ctx, jobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		newJob, buildErr := c.buildJob(work, jobName, kind)
		if buildErr != nil {
			return c.updateWorkStatus(ctx, work, "Error", buildErr.Error(), "")
		}
		if grant != nil {
			if applyErr := c.applyGrantToJob(ctx, newJob, grant); applyErr != nil {
				return c.updateWorkStatus(ctx, work, "Error", applyErr.Error(), "")
			}
		}
		if _, createErr := c.kube.BatchV1().Jobs(c.cfg.JobNamespace).Create(ctx, newJob, metav1.CreateOptions{}); createErr != nil {
			return c.updateWorkStatus(ctx, work, "Error", fmt.Sprintf("failed to create job: %v", createErr), "")
		}
		c.logger.Info("created job for work",
			"work", work.GetName(),
			"workNamespace", work.GetNamespace(),
			"job", jobName,
			"jobNamespace", c.cfg.JobNamespace,
		)
		return c.updateWorkStatus(ctx, work, "Submitted", "job created", artifactURL(c.cfg.ArtifactBaseURL, work.GetName()))
	}
	if err != nil {
		return fmt.Errorf("get job %s/%s: %w", c.cfg.JobNamespace, jobName, err)
	}

	phase, message := phaseFromJob(job)
	if phase == "Succeeded" {
		if validationMessage, validationErr := c.validateSucceededWorkArtifacts(work.GetName()); validationErr != nil {
			c.logger.Warn("artifact validation skipped due error", "work", work.GetName(), "error", validationErr)
		} else if validationMessage != "" {
			phase = "Failed"
			message = validationMessage
		}
	}
	url := artifactURL(c.cfg.ArtifactBaseURL, work.GetName())
	return c.updateWorkStatus(ctx, work, phase, message, url)
}

func (c *Controller) buildJob(work *unstructured.Unstructured, jobName, kind string) (*batchv1.Job, error) {
	switch kind {
	case "overpassql.map.v1", "maplibre.style.v1", "duckdb.map.v1", "gdal.rastertile.v1", "laz.3dtiles.v1":
		legacySpec, found, err := unstructured.NestedMap(work.Object, "spec")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec for legacy kind bridge: %v", err)
		}
		if !found || len(legacySpec) == 0 {
			return nil, fmt.Errorf("spec is required for legacy kind bridge")
		}
		bridgeScript, err := buildLegacyKindBridgeScript(kind, legacySpec)
		if err != nil {
			return nil, err
		}
		userPrompt := legacyKindBridgePrompt(kind, legacySpec)
		return c.buildScriptJob(work, jobName, legacyKindAgentImage, buildAgentScript(work.GetName(), bridgeScript, userPrompt)), nil

	case "agent.cli.v1":
		userPrompt := workUserPrompt(work)

		image, _, err := nestedStringAny(work.Object, "spec", "agent", "image")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.agent.image: %v", err)
		}
		image = strings.TrimSpace(image)
		if image == "" {
			return nil, fmt.Errorf("spec.agent.image is required")
		}

		script, _, err := nestedStringAny(work.Object, "spec", "agent", "script")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.agent.script: %v", err)
		}
		script = strings.TrimSpace(script)

		command, _, err := nestedStringSlice(work.Object, "spec", "agent", "command")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.agent.command: %v", err)
		}
		args, _, err := nestedStringSlice(work.Object, "spec", "agent", "args")
		if err != nil {
			return nil, fmt.Errorf("failed to read spec.agent.args: %v", err)
		}

		if script == "" && len(command) == 0 {
			return nil, fmt.Errorf("spec.agent.script or spec.agent.command is required")
		}

		if script != "" {
			return c.buildScriptJob(work, jobName, image, buildAgentScript(work.GetName(), script, userPrompt)), nil
		}
		return c.buildScriptJob(work, jobName, image, buildAgentCommandScript(work.GetName(), command, args, userPrompt)), nil

	default:
		return nil, fmt.Errorf("unsupported spec.kind=%q", kind)
	}
}

func (c *Controller) buildScriptJob(work *unstructured.Unstructured, jobName, image, script string) *batchv1.Job {
	suspend := true
	hostPathType := corev1.HostPathDirectory
	workName := work.GetName()
	workNamespace := work.GetNamespace()
	deadlineSeconds := extractDeadlineSeconds(work)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: c.cfg.JobNamespace,
			Labels: map[string]string{
				"kueue.x-k8s.io/queue-name": c.cfg.LocalQueueName,
				"nereid.yuiseki.net/work":   workName,
			},
			Annotations: map[string]string{
				"nereid.yuiseki.net/work-name":      workName,
				"nereid.yuiseki.net/work-namespace": workNamespace,
			},
		},
		Spec: batchv1.JobSpec{
			Suspend:               &suspend,
			BackoffLimit:          int32Ptr(0),
			ActiveDeadlineSeconds: &deadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"nereid.yuiseki.net/work": workName,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "task",
							Image:   image,
							Command: []string{"sh", "-lc"},
							Args:    []string{script},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    mustParseQuantity("100m"),
									corev1.ResourceMemory: mustParseQuantity("128Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    mustParseQuantity("500m"),
									corev1.ResourceMemory: mustParseQuantity("512Mi"),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "artifacts",
									MountPath: "/artifacts",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "artifacts",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: c.cfg.ArtifactsHostPath,
									Type: &hostPathType,
								},
							},
						},
					},
				},
			},
		},
	}

	if c.cfg.RuntimeClassName != "" {
		job.Spec.Template.Spec.RuntimeClassName = &c.cfg.RuntimeClassName
	}

	return job
}

func legacyKindBridgePrompt(kind string, spec map[string]interface{}) string {
	title, _ := spec["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" {
		return "Legacy kind bridge: " + kind
	}
	return "Legacy kind bridge: " + kind + " / " + title
}

func buildLegacyKindBridgeScript(kind string, spec map[string]interface{}) (string, error) {
	skillSlug := legacyKindSkillSlug(kind)
	skillDoc, err := legacyKindSkillDoc(kind)
	if err != nil {
		return "", err
	}

	specJSON, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal legacy spec for %s: %w", kind, err)
	}

	commonSkillB64 := base64.StdEncoding.EncodeToString([]byte(legacyCommonSkillDoc()))
	createSkillsB64 := base64.StdEncoding.EncodeToString([]byte(legacyCreateSkillsSkillDoc()))
	osmableSkillB64 := base64.StdEncoding.EncodeToString([]byte(legacyOsmableSkillDoc()))
	kindSkillB64 := base64.StdEncoding.EncodeToString([]byte(skillDoc))
	specB64 := base64.StdEncoding.EncodeToString(specJSON)
	promptB64 := base64.StdEncoding.EncodeToString([]byte(legacyKindBridgePromptText(kind)))

	return fmt.Sprintf(`set -eu
OUT_DIR="${NEREID_ARTIFACT_DIR:-/artifacts/${NEREID_WORK_NAME:-work}}"
SPECIALS_DIR="${OUT_DIR}/specials"
SPECIALS_SKILLS_DIR="${SPECIALS_DIR}/skills"
mkdir -p "${OUT_DIR}" "${SPECIALS_SKILLS_DIR}"
OUT_TEXT="${OUT_DIR}/gemini-output.txt"
OUT_TEXT_RAW="${OUT_DIR}/gemini-output.raw.txt"
PROMPT_FILE="${OUT_DIR}/legacy-kind-prompt.txt"
SPEC_FILE="${OUT_DIR}/legacy-work-spec.json"
COMMON_SKILL_DIR="${OUT_DIR}/.gemini/skills/nereid-artifact-authoring"
CREATE_SKILLS_DIR="${OUT_DIR}/.gemini/skills/create-skills"
OSMABLE_SKILL_DIR="${OUT_DIR}/.gemini/skills/osmable-v1"
KIND_SKILL_DIR="${OUT_DIR}/.gemini/skills/%s"
GEMINI_MD_FILE="${OUT_DIR}/GEMINI.md"

export HOME="${OUT_DIR}/.home"
mkdir -p "${HOME}" "${COMMON_SKILL_DIR}" "${CREATE_SKILLS_DIR}" "${OSMABLE_SKILL_DIR}" "${KIND_SKILL_DIR}"

if [ ! -s "${OUT_DIR}/index.html" ]; then
cat > "${OUT_DIR}/index.html" <<'HTMLBOOT'
<!doctype html>
<html>
  <head>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width,initial-scale=1"/>
    <title>NEREID Legacy Kind Bootstrap</title>
    <style>
      html, body { margin: 0; padding: 0; background: #f7fafc; color: #1f2d3d; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace; }
      .wrap { max-width: 980px; margin: 0 auto; padding: 14px; }
      h1 { margin: 0 0 10px 0; font-size: 18px; }
      p { margin: 0; font-size: 13px; color: #355a83; }
    </style>
  </head>
  <body>
    <div class="wrap">
      <h1>Hello, world</h1>
      <p>Gemini CLI is bridging a legacy kind specification...</p>
    </div>
  </body>
</html>
HTMLBOOT
fi

if [ -z "${GEMINI_API_KEY:-}" ]; then
  printf '%%s\n' "GEMINI_API_KEY is required for Gemini CLI execution." > "${OUT_TEXT}"
  cat "${OUT_TEXT}"
  exit 2
fi

if ! command -v pgrep >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq >/dev/null 2>&1 || true
    apt-get install -y -qq --no-install-recommends procps >/dev/null 2>&1 || true
  fi
fi

COMMON_SKILL_B64=%q
printf '%%s' "${COMMON_SKILL_B64}" | base64 -d > "${COMMON_SKILL_DIR}/SKILL.md"

CREATE_SKILLS_B64=%q
printf '%%s' "${CREATE_SKILLS_B64}" | base64 -d > "${CREATE_SKILLS_DIR}/SKILL.md"

OSMABLE_SKILL_B64=%q
printf '%%s' "${OSMABLE_SKILL_B64}" | base64 -d > "${OSMABLE_SKILL_DIR}/SKILL.md"

KIND_SKILL_B64=%q
printf '%%s' "${KIND_SKILL_B64}" | base64 -d > "${KIND_SKILL_DIR}/SKILL.md"

SPEC_B64=%q
printf '%%s' "${SPEC_B64}" | base64 -d > "${SPEC_FILE}"

PROMPT_B64=%q
printf '%%s' "${PROMPT_B64}" | base64 -d > "${PROMPT_FILE}"

cat > "${GEMINI_MD_FILE}" <<'GEMINI'
# NEREID Legacy Kind Bridge

## Absolute security rule (highest priority)
- You MUST NOT read, reference, request, print, or persist any environment variable value.
- You MUST NOT expose secrets (for example GEMINI_API_KEY) in any output, including index.html, logs, dialogue, or generated files.
- Gemini web_fetch is allowed for normal web pages.
- For structured JSON APIs (Overpass/Nominatim), DO NOT use web_fetch. Use curl/browser fetch directly.
- Never call Overpass with raw query in ?data=. URL-encode query or use curl --data-urlencode.

## Skill policy
- Workspace skills are available under ./.gemini/skills/.
- Rely on Gemini skill discovery and activate_skill for progressive disclosure.
- Do not pre-load all skill bodies at startup unless strictly required.

## Runtime facts
- Legacy work specification is available at ./legacy-work-spec.json
- Current instruction is available at ./legacy-kind-prompt.txt
- Persist output artifacts in the current directory.
- Persist extracted session skills under ./specials/skills/.
GEMINI

cd "${OUT_DIR}"
export npm_config_loglevel=error
export npm_config_update_notifier=false
export npm_config_fund=false
export npm_config_audit=false
export NO_UPDATE_NOTIFIER=1
GEMINI_CLI_MODEL="${NEREID_GEMINI_MODEL:-${GEMINI_MODEL:-gemini-2.5-flash}}"
set +e
npx -y --loglevel=error --no-update-notifier --no-fund --no-audit @google/gemini-cli -- -p "$(cat "${PROMPT_FILE}")" --model "${GEMINI_CLI_MODEL}" --output-format text --approval-mode yolo > "${OUT_TEXT_RAW}" 2>&1
status=$?
set -e

if ! sed \
  -e '/^npm[[:space:]]\+warn[[:space:]]\+deprecated/d' \
  -e '/^npm[[:space:]]\+notice/d' \
  -e '/^YOLO mode is enabled\. All tool calls will be automatically approved\.$/d' \
  -e '/^Hook registry initialized with [0-9][0-9]* hook entries$/d' \
  "${OUT_TEXT_RAW}" > "${OUT_TEXT}"; then
  cp "${OUT_TEXT_RAW}" "${OUT_TEXT}"
fi
rm -f "${OUT_TEXT_RAW}"

if [ ! -s "${OUT_DIR}/index.html" ]; then
cat > "${OUT_DIR}/index.html" <<'HTML'
<!doctype html>
<html>
  <head>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width,initial-scale=1"/>
    <title>NEREID Legacy Kind Bridge</title>
    <style>
      html, body { margin: 0; padding: 0; background: #f7fafc; color: #1f2d3d; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace; }
      .wrap { max-width: 1200px; margin: 0 auto; padding: 14px; }
      h1 { margin: 0 0 10px 0; font-size: 16px; }
      pre { white-space: pre-wrap; word-break: break-word; background: #fff; border: 1px solid #d5deea; border-radius: 10px; padding: 12px; min-height: 50vh; }
      .meta { margin: 0 0 10px 0; font-size: 12px; color: #355a83; }
    </style>
  </head>
  <body>
    <div class="wrap">
      <h1>Legacy Kind Bridge Output</h1>
      <div class="meta"><a href="./gemini-output.txt">gemini-output.txt</a> / <a href="./legacy-work-spec.json">legacy-work-spec.json</a></div>
      <pre id="out">Loading...</pre>
    </div>
    <script>
      fetch("./gemini-output.txt?ts=" + Date.now(), { cache: "no-store" })
        .then((r) => r.ok ? r.text() : Promise.reject(new Error("HTTP " + r.status)))
        .then((t) => { document.getElementById("out").textContent = t || "(empty)"; })
        .catch((e) => { document.getElementById("out").textContent = "load failed: " + e.message; });
    </script>
  </body>
</html>
HTML
fi

cat "${OUT_TEXT}"
exit "${status}"
`, skillSlug, commonSkillB64, createSkillsB64, osmableSkillB64, kindSkillB64, specB64, promptB64), nil
}

func legacyKindSkillSlug(kind string) string {
	slug := strings.NewReplacer(".", "-", "_", "-", "/", "-").Replace(strings.ToLower(strings.TrimSpace(kind)))
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "kind"
	}
	return slug
}

func legacyCommonSkillDoc() string {
	return `---
name: nereid-artifact-authoring
description: Create static-hostable HTML artifacts in NEREID workspace.
---
# NEREID Artifact Authoring

## Required behavior
- You MUST create or update ./index.html in the current directory.
- First action: write a minimal ./index.html (for example, an <h1>Hello, world</h1> page).
- After bootstrap, replace or extend ./index.html to satisfy the current instruction.
- Use shell commands to write files; do not finish with explanation-only output.
- Finish only after files are persisted to disk.

## Security
- NEVER read, request, print, or persist environment variable values.
- NEVER output secrets such as API keys into logs, text responses, HTML, JavaScript, or any generated file.
- Gemini web_fetch tool is allowed for normal HTML pages.
- For structured JSON APIs (for example Overpass/Nominatim), DO NOT use Gemini web_fetch. Use shell curl or browser-side fetch directly.
- Never pass raw Overpass QL in a URL query string such as .../api/interpreter?data=[out:json]....
- For Overpass requests, always URL-encode data (for example encodeURIComponent(query)) or use curl -G --data-urlencode.
- If a structured API call fails, retry with curl/browser fetch; do not retry with web_fetch.
- For OSM geocoding/AOI/POI/routing, prefer npx -y osmable ... for deterministic execution.

## Mapping defaults
- For map requests, produce an interactive HTML map (MapLibre, Leaflet, or Cesium).
- For MapLibre base maps, use one of:
  - https://tile.yuiseki.net/styles/osm-bright/style.json
  - https://tile.yuiseki.net/styles/osm-fiord/style.json
- If Overpass API is used, call one of:
  - https://overpass.yuiseki.net/api/interpreter?data=<url-encoded-overpass-ql>
  - curl -sS -G --data-urlencode "data=<overpass-ql>" https://overpass.yuiseki.net/api/interpreter
- If Nominatim API is used, use:
  - https://nominatim.yuiseki.net/search.php?format=jsonv2&limit=1&q=<url-encoded-query>
`
}

func legacyCreateSkillsSkillDoc() string {
	return `---
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
`
}

func legacyOsmableSkillDoc() string {
	return `---
name: osmable-v1
description: Use osmable CLI for deterministic OSM geocoding, AOI, POI, and routing workflows.
---
# osmable Workflow

## When to use
- Instruction involves OSM data retrieval or geospatial operations (Nominatim/Overpass/Valhalla).
- Deterministic command output is preferred over fragile ad-hoc API calls.

## Core rules
1. Prefer npx -y osmable ... for geocode/aoi/poi/route tasks.
2. Use default text output for concise logs.
3. Use --format json or --format geojson when machine-readable output is required.
4. Run npx -y osmable doctor before critical flows.

## Common commands
- Geocode: npx -y osmable geocode "東京都台東区" --format json
- AOI: npx -y osmable aoi resolve "東京都台東区" --format geojson > aoi.geojson
- POI count: npx -y osmable poi count --tag leisure=park --within "東京都台東区" --format json
- POI fetch: npx -y osmable poi fetch --tag leisure=park --within "東京都台東区" --format geojson > parks.geojson
- Route: npx -y osmable route --from "上野駅" --to "浅草寺" --mode pedestrian --format json > route.json

## Failure and fallback
- If osmable fails, capture stderr and exit code in artifacts.
- Fall back to direct curl/browser fetch only when osmable cannot satisfy the task.
`
}

func legacyKindSkillDoc(kind string) (string, error) {
	switch kind {
	case "overpassql.map.v1":
		return `---
name: overpassql-map-v1
description: Decide when to use Overpass QL and how to design robust map data queries.
---
# Overpass QL Strategy

## When to use
- User asks for specific real-world objects from OpenStreetMap.
- Instruction requires tag filtering, administrative area filtering, or bbox search.

## Core knowledge
- Overpass QL works with node / way / relation.
- Query design strongly affects response size and runtime.
- Administrative areas are often resolved via area references.

## Recommended workflow
1. Read target area intent from instruction and/or spec.
2. Build explicit tag filters with bounded scope.
3. Prefer npx -y osmable poi count/fetch for deterministic OSM retrieval.
4. If manual Overpass execution is required, use browser fetch or curl (not web_fetch).
5. Use endpoint https://overpass.yuiseki.net/api/interpreter with URL-encoded data parameter.
6. Persist raw response and render map-friendly output.

## Output expectations
- Keep index.html usable.
- Keep raw response inspectable.
`, nil
	case "maplibre.style.v1":
		return `---
name: maplibre-style-v1
description: Decide when to author MapLibre Style Spec and how to structure style layers.
---
# MapLibre Style Authoring

## When to use
- User asks to change map appearance (colors, labels, emphasis, visibility).
- Task is cartographic styling rather than heavy data extraction.

## Core knowledge
- Style Spec uses sources/layers/glyphs/sprites.
- Layer order determines visual priority.
- Filters and paint/layout should be explicit.

## Recommended workflow
1. Choose base style source.
2. Apply requested style changes with minimal, clear layers.
3. Validate structure and render preview in index.html.

## Output expectations
- If style is inline, persist style.json.
- Keep style/output easy to inspect.
`, nil
	case "duckdb.map.v1":
		return `---
name: duckdb-map-v1
description: Decide when DuckDB is suitable and how to prepare query-to-map workflows.
---
# DuckDB Map Workflow

## When to use
- Instruction implies tabular/spatial analytics before visualization.
- Query logic is central to the requested result.

## Core knowledge
- DuckDB is optimized for analytical SQL workloads.
- Query outputs often need conversion to map-ready geometry/coordinates.

## Recommended workflow
1. Persist input references and SQL for reproducibility.
2. Execute query when runtime supports DuckDB.
3. Convert output into map-usable structure.
4. Render result/status in index.html.

## Output expectations
- Keep inputs/query inspectable.
- Provide fallback status if execution is unavailable.
`, nil
	case "gdal.rastertile.v1":
		return `---
name: gdal-rastertile-v1
description: Decide when raster tiling is needed and how to structure GDAL-based pipelines.
---
# GDAL Raster Pipeline

## When to use
- Input is raster imagery and output should be web-tilable map layers.
- Reprojection/nodata/zoom tuning is required.

## Core knowledge
- Typical flow: inspect -> transform -> reproject -> tile.
- Intermediate artifacts are useful for debugging.

## Recommended workflow
1. Capture source metadata and processing params.
2. Apply raster transforms.
3. Generate tiles and preview entrypoint.

## Output expectations
- Keep index.html usable.
- Provide clear fallback details when toolchain is unavailable.
`, nil
	case "laz.3dtiles.v1":
		return `---
name: laz-3dtiles-v1
description: Decide when LAZ to 3DTiles flow is needed and how to structure 3D pointcloud outputs.
---
# LAZ to 3DTiles Pipeline

## When to use
- Instruction requests interactive 3D pointcloud visualization from LAZ/LAS data.
- CRS handling and tileset conversion are required.

## Core knowledge
- CRS validation/reprojection is often required before conversion.
- 3DTiles output should include viewer entrypoint and metadata.

## Recommended workflow
1. Validate source/CRS assumptions.
2. Execute conversion pipeline when toolchain is available.
3. Render 3D preview entrypoint and expose generated assets.

## Output expectations
- Keep index.html usable.
- Provide clear fallback details when toolchain is unavailable.
`, nil
	default:
		return "", fmt.Errorf("unsupported legacy kind for bridge: %q", kind)
	}
}

func legacyKindBridgePromptText(kind string) string {
	return fmt.Sprintf(`Execute a legacy NEREID work specification bridge.

Target kind: %s

Steps:
1. Read ./legacy-work-spec.json and follow imported skills from GEMINI.md.
2. Reproduce the legacy kind behavior in static artifacts.
3. Always keep ./index.html renderable from static hosting.
4. If an external toolchain is unavailable, show concise fallback status in-page and still finish with usable artifacts.
5. Never read or expose environment variables or secrets.
`, kind)
}

func buildAgentScript(workName, userScript, userPrompt string) string {
	scriptB64 := base64.StdEncoding.EncodeToString([]byte(userScript))
	promptB64 := base64.StdEncoding.EncodeToString([]byte(userPrompt))
	return fmt.Sprintf(`set -eu
WORK=%q
OUT_DIR="/artifacts/${WORK}"
LOGS_DIR="${OUT_DIR}/logs"
SPECIALS_DIR="${OUT_DIR}/specials"
SPECIALS_SKILLS_DIR="${SPECIALS_DIR}/skills"
mkdir -p "${OUT_DIR}" "${LOGS_DIR}" "${SPECIALS_SKILLS_DIR}"
START_TIME_FILE="${LOGS_DIR}/start-time.txt"
INSTRUCTIONS_CSV="${LOGS_DIR}/instructions.csv"
if [ ! -s "${START_TIME_FILE}" ]; then
  date +%%s > "${START_TIME_FILE}" || true
fi
if [ ! -s "${INSTRUCTIONS_CSV}" ]; then
  printf 'timestamp_unix,role,text\n' > "${INSTRUCTIONS_CSV}" || true
fi

SCRIPT_B64=%q
printf '%%s' "${SCRIPT_B64}" | base64 -d > /tmp/nereid-agent.sh
chmod +x /tmp/nereid-agent.sh

PROMPT_B64=%q
if [ -n "${PROMPT_B64}" ]; then
  printf '%%s' "${PROMPT_B64}" | base64 -d > "${OUT_DIR}/user-input.txt"
  prompt_csv=$(awk 'BEGIN{first=1} { gsub(/"/, "\"\""); if (!first) printf "\\n"; printf "%%s", $0; first=0 } END{}' "${OUT_DIR}/user-input.txt")
  printf '%%s,USER,"%%s"\n' "$(date +%%s)" "${prompt_csv}" >> "${INSTRUCTIONS_CSV}" || true
fi

export NEREID_WORK_NAME="${WORK}"
export NEREID_ARTIFACT_DIR="${OUT_DIR}"

set +e
/bin/sh /tmp/nereid-agent.sh > "${OUT_DIR}/agent.log" 2>&1
status=$?
set -e

{
  if [ -f "${OUT_DIR}/user-input.txt" ]; then
    printf '[USER]\n'
    cat "${OUT_DIR}/user-input.txt"
    printf '\n\n'
  fi
  printf '[AGENT]\n'
  cat "${OUT_DIR}/agent.log"
} > "${OUT_DIR}/dialogue.txt"
cp "${OUT_DIR}/agent.log" "${LOGS_DIR}/agent.log" 2>/dev/null || true
cp "${OUT_DIR}/dialogue.txt" "${LOGS_DIR}/dialogue.txt" 2>/dev/null || true
cp "${OUT_DIR}/user-input.txt" "${LOGS_DIR}/user-input.txt" 2>/dev/null || true

if [ ! -f "${OUT_DIR}/index.html" ]; then
  cat > "${OUT_DIR}/index.html" <<'HTML'
<!doctype html>
<html>
  <head><meta charset="utf-8"/><title>NEREID agent.cli.v1</title></head>
  <body>
    <h1>NEREID agent.cli.v1</h1>
    <p>script mode</p>
    <ul>
      <li><a href="./user-input.txt">user-input.txt</a></li>
      <li><a href="./dialogue.txt">dialogue.txt</a></li>
      <li><a href="./agent.log">agent.log</a></li>
      <li><a href="./logs/start-time.txt">logs/start-time.txt</a></li>
      <li><a href="./logs/instructions.csv">logs/instructions.csv</a></li>
      <li><a href="./specials/">specials/</a></li>
      <li><a href="./specials/skills/">specials/skills/</a></li>
      <li><a href="https://nereid.yuiseki.net/works/%s">open work</a></li>
    </ul>
  </body>
</html>
HTML
fi

exit "${status}"
`, workName, scriptB64, promptB64, workName)
}

func buildAgentCommandScript(workName string, command, args []string, userPrompt string) string {
	all := append(append([]string{}, command...), args...)
	quoted := make([]string, 0, len(all))
	for _, p := range all {
		quoted = append(quoted, shellQuote(p))
	}
	commandLine := strings.Join(quoted, " ")
	commandTextB64 := base64.StdEncoding.EncodeToString([]byte(strings.Join(all, " ")))
	promptB64 := base64.StdEncoding.EncodeToString([]byte(userPrompt))

	return fmt.Sprintf(`set -eu
WORK=%q
OUT_DIR="/artifacts/${WORK}"
LOGS_DIR="${OUT_DIR}/logs"
SPECIALS_DIR="${OUT_DIR}/specials"
SPECIALS_SKILLS_DIR="${SPECIALS_DIR}/skills"
mkdir -p "${OUT_DIR}" "${LOGS_DIR}" "${SPECIALS_SKILLS_DIR}"
START_TIME_FILE="${LOGS_DIR}/start-time.txt"
INSTRUCTIONS_CSV="${LOGS_DIR}/instructions.csv"
if [ ! -s "${START_TIME_FILE}" ]; then
  date +%%s > "${START_TIME_FILE}" || true
fi
if [ ! -s "${INSTRUCTIONS_CSV}" ]; then
  printf 'timestamp_unix,role,text\n' > "${INSTRUCTIONS_CSV}" || true
fi

export NEREID_WORK_NAME="${WORK}"
export NEREID_ARTIFACT_DIR="${OUT_DIR}"

CMD_TEXT_B64=%q
printf '%%s' "${CMD_TEXT_B64}" | base64 -d > "${OUT_DIR}/command.txt"

PROMPT_B64=%q
if [ -n "${PROMPT_B64}" ]; then
  printf '%%s' "${PROMPT_B64}" | base64 -d > "${OUT_DIR}/user-input.txt"
  prompt_csv=$(awk 'BEGIN{first=1} { gsub(/"/, "\"\""); if (!first) printf "\\n"; printf "%%s", $0; first=0 } END{}' "${OUT_DIR}/user-input.txt")
  printf '%%s,USER,"%%s"\n' "$(date +%%s)" "${prompt_csv}" >> "${INSTRUCTIONS_CSV}" || true
fi

set +e
%s > "${OUT_DIR}/agent.log" 2>&1
status=$?
set -e

{
  if [ -f "${OUT_DIR}/user-input.txt" ]; then
    printf '[USER]\n'
    cat "${OUT_DIR}/user-input.txt"
    printf '\n\n'
  fi
  printf '[AGENT]\n'
  cat "${OUT_DIR}/agent.log"
} > "${OUT_DIR}/dialogue.txt"
cp "${OUT_DIR}/agent.log" "${LOGS_DIR}/agent.log" 2>/dev/null || true
cp "${OUT_DIR}/dialogue.txt" "${LOGS_DIR}/dialogue.txt" 2>/dev/null || true
cp "${OUT_DIR}/user-input.txt" "${LOGS_DIR}/user-input.txt" 2>/dev/null || true

if [ ! -f "${OUT_DIR}/index.html" ]; then
  cat > "${OUT_DIR}/index.html" <<'HTML'
<!doctype html>
<html>
  <head><meta charset="utf-8"/><title>NEREID agent.cli.v1</title></head>
  <body>
    <h1>NEREID agent.cli.v1</h1>
    <p>command mode</p>
    <ul>
      <li><a href="./user-input.txt">user-input.txt</a></li>
      <li><a href="./dialogue.txt">dialogue.txt</a></li>
      <li><a href="./command.txt">command.txt</a></li>
      <li><a href="./agent.log">agent.log</a></li>
      <li><a href="./logs/start-time.txt">logs/start-time.txt</a></li>
      <li><a href="./logs/instructions.csv">logs/instructions.csv</a></li>
      <li><a href="./specials/">specials/</a></li>
      <li><a href="./specials/skills/">specials/skills/</a></li>
      <li><a href="https://nereid.yuiseki.net/works/%s">open work</a></li>
    </ul>
  </body>
</html>
HTML
fi

exit "${status}"
`, workName, commandTextB64, promptB64, commandLine, workName)
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func (c *Controller) updateWorkStatus(ctx context.Context, work *unstructured.Unstructured, phase, message, artifact string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest, err := c.dynamic.Resource(workGVR).Namespace(work.GetNamespace()).Get(ctx, work.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}

		currentPhase, _, _ := unstructured.NestedString(latest.Object, "status", "phase")
		currentMessage, _, _ := unstructured.NestedString(latest.Object, "status", "message")
		currentArtifact, _, _ := unstructured.NestedString(latest.Object, "status", "artifactUrl")
		if currentPhase == phase && currentMessage == message && currentArtifact == artifact {
			return nil
		}

		if err := unstructured.SetNestedField(latest.Object, phase, "status", "phase"); err != nil {
			return err
		}
		if message != "" {
			if err := unstructured.SetNestedField(latest.Object, message, "status", "message"); err != nil {
				return err
			}
		} else {
			unstructured.RemoveNestedField(latest.Object, "status", "message")
		}
		if artifact != "" {
			if err := unstructured.SetNestedField(latest.Object, artifact, "status", "artifactUrl"); err != nil {
				return err
			}
		} else {
			unstructured.RemoveNestedField(latest.Object, "status", "artifactUrl")
		}

		_, err = c.dynamic.Resource(workGVR).Namespace(work.GetNamespace()).UpdateStatus(ctx, latest, metav1.UpdateOptions{})
		return err
	})
}

func phaseFromJob(job *batchv1.Job) (string, string) {
	if job.Status.Succeeded > 0 {
		return "Succeeded", "job completed"
	}
	if job.Status.Failed > 0 {
		return "Failed", "job failed"
	}
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		return "Queued", "waiting for kueue admission"
	}
	if job.Status.Active > 0 {
		return "Running", "job is running"
	}
	return "Submitted", "job submitted"
}

func (c *Controller) validateSucceededWorkArtifacts(workName string) (string, error) {
	root := strings.TrimSpace(c.cfg.ArtifactsHostPath)
	if root == "" {
		return "", nil
	}
	workDir := filepath.Join(root, workName)
	indexPath := filepath.Join(workDir, "index.html")
	info, err := os.Stat(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "artifact validation failed: index.html not found", nil
		}
		return "", fmt.Errorf("stat %q: %w", indexPath, err)
	}
	if info.IsDir() || info.Size() == 0 {
		return "artifact validation failed: index.html is empty", nil
	}

	// Detect known runtime fault signatures from agent output files.
	logPaths := []string{
		filepath.Join(workDir, "agent.log"),
		filepath.Join(workDir, "gemini-output.txt"),
		filepath.Join(workDir, "dialogue.txt"),
		filepath.Join(workDir, "logs", "agent.log"),
		filepath.Join(workDir, "logs", "dialogue.txt"),
	}
	for _, p := range logPaths {
		b, readErr := os.ReadFile(p)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return "", fmt.Errorf("read %q: %w", p, readErr)
		}
		if signature := detectArtifactRuntimeErrorSignature(string(b)); signature != "" {
			return "artifact runtime validation failed: " + signature, nil
		}
	}
	return "", nil
}

func detectArtifactRuntimeErrorSignature(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "cannot read properties of undefined (reading 'lon')"):
		return "Cannot read properties of undefined (reading 'lon')"
	case strings.Contains(lower, "cannot read properties of undefined (reading 'lat')"):
		return "Cannot read properties of undefined (reading 'lat')"
	case strings.Contains(lower, "typeerror: cannot read properties of undefined"):
		return "TypeError: cannot read properties of undefined"
	default:
		return ""
	}
}

func makeJobName(workName string) string {
	const prefix = "work-"
	const maxLen = 63
	maxBody := maxLen - len(prefix)

	workName = sanitizeDNSLabel(workName)
	if workName == "" {
		workName = "work"
	}
	if len(workName) <= maxBody {
		return prefix + workName
	}

	hash := sha1.Sum([]byte(workName))
	suffix := hex.EncodeToString(hash[:])[:8]
	bodyMax := maxBody - len(suffix) - 1
	if bodyMax < 1 {
		bodyMax = 1
	}

	body := strings.Trim(workName[:bodyMax], "-")
	if body == "" {
		body = "work"
	}
	return prefix + body + "-" + suffix
}

func artifactURL(base, workName string) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/", base, workName)
}

func (c *Controller) validateGrantForWork(ctx context.Context, work *unstructured.Unstructured, kind string, grant *unstructured.Unstructured) error {
	if grant == nil {
		return nil
	}
	grantName := grant.GetName()

	enabled, found, err := unstructured.NestedBool(grant.Object, "spec", "enabled")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.enabled: %v", grantName, err)
	}
	if found && !enabled {
		return fmt.Errorf("grant %q is disabled", grantName)
	}

	expiresAt, _, err := unstructured.NestedString(grant.Object, "spec", "expiresAt")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.expiresAt: %v", grantName, err)
	}
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt != "" {
		ts, parseErr := time.Parse(time.RFC3339, expiresAt)
		if parseErr != nil {
			return fmt.Errorf("grant %q has invalid spec.expiresAt=%q (expected RFC3339): %v", grantName, expiresAt, parseErr)
		}
		now := c.nowFunc().UTC()
		if now.After(ts) {
			return fmt.Errorf("grant %q expired at %s", grantName, ts.UTC().Format(time.RFC3339))
		}
	}

	allowedKinds, _, err := unstructured.NestedStringSlice(grant.Object, "spec", "allowedKinds")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.allowedKinds: %v", grantName, err)
	}
	if len(allowedKinds) > 0 {
		ok := false
		for _, k := range allowedKinds {
			if strings.TrimSpace(k) == kind {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("grant %q does not allow spec.kind=%q", grantName, kind)
		}
	}

	maxUses, found, err := unstructured.NestedInt64(grant.Object, "spec", "maxUses")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.maxUses: %v", grantName, err)
	}
	if found && maxUses > 0 {
		jobs, listErr := c.kube.BatchV1().Jobs(c.cfg.JobNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("nereid.yuiseki.net/grant=%s", grantName),
		})
		if listErr != nil {
			return fmt.Errorf("list jobs for grant %q maxUses: %w", grantName, listErr)
		}
		used := int64(len(jobs.Items))
		if used >= maxUses {
			return fmt.Errorf("grant %q exhausted: maxUses=%d used=%d", grantName, maxUses, used)
		}
	}

	return nil
}

func workUserPrompt(work *unstructured.Unstructured) string {
	if work == nil {
		return ""
	}
	annotations := work.GetAnnotations()
	if len(annotations) == 0 {
		return ""
	}
	return strings.TrimSpace(annotations[userPromptAnnotationKey])
}

func (c *Controller) applyGrantToJob(ctx context.Context, job *batchv1.Job, grant *unstructured.Unstructured) error {
	if job == nil || grant == nil {
		return nil
	}
	grantName := strings.TrimSpace(grant.GetName())

	if job.Labels == nil {
		job.Labels = map[string]string{}
	}
	if grantName != "" {
		job.Labels["nereid.yuiseki.net/grant"] = grantName
	}

	queueName, _, err := unstructured.NestedString(grant.Object, "spec", "kueue", "localQueueName")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.kueue.localQueueName: %v", grantName, err)
	}
	queueName = strings.TrimSpace(queueName)
	if queueName != "" {
		job.Labels["kueue.x-k8s.io/queue-name"] = queueName
	}

	runtimeClassName, _, err := unstructured.NestedString(grant.Object, "spec", "runtimeClassName")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.runtimeClassName: %v", grantName, err)
	}
	runtimeClassName = strings.TrimSpace(runtimeClassName)
	if runtimeClassName != "" {
		job.Spec.Template.Spec.RuntimeClassName = &runtimeClassName
	}

	if len(job.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("job has no containers")
	}
	container := &job.Spec.Template.Spec.Containers[0]
	if container.Resources.Requests == nil {
		container.Resources.Requests = corev1.ResourceList{}
	}
	if container.Resources.Limits == nil {
		container.Resources.Limits = corev1.ResourceList{}
	}

	reqCPU, _, err := nestedStringAny(grant.Object, "spec", "resources", "requests", "cpu")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.resources.requests.cpu: %v", grantName, err)
	}
	reqMem, _, err := nestedStringAny(grant.Object, "spec", "resources", "requests", "memory")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.resources.requests.memory: %v", grantName, err)
	}
	limCPU, _, err := nestedStringAny(grant.Object, "spec", "resources", "limits", "cpu")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.resources.limits.cpu: %v", grantName, err)
	}
	limMem, _, err := nestedStringAny(grant.Object, "spec", "resources", "limits", "memory")
	if err != nil {
		return fmt.Errorf("failed to read grant %q spec.resources.limits.memory: %v", grantName, err)
	}

	if strings.TrimSpace(reqCPU) != "" {
		q, parseErr := resource.ParseQuantity(reqCPU)
		if parseErr != nil {
			return fmt.Errorf("grant %q invalid spec.resources.requests.cpu=%q: %v", grantName, reqCPU, parseErr)
		}
		container.Resources.Requests[corev1.ResourceCPU] = q
	}
	if strings.TrimSpace(reqMem) != "" {
		q, parseErr := resource.ParseQuantity(reqMem)
		if parseErr != nil {
			return fmt.Errorf("grant %q invalid spec.resources.requests.memory=%q: %v", grantName, reqMem, parseErr)
		}
		container.Resources.Requests[corev1.ResourceMemory] = q
	}
	if strings.TrimSpace(limCPU) != "" {
		q, parseErr := resource.ParseQuantity(limCPU)
		if parseErr != nil {
			return fmt.Errorf("grant %q invalid spec.resources.limits.cpu=%q: %v", grantName, limCPU, parseErr)
		}
		container.Resources.Limits[corev1.ResourceCPU] = q
	}
	if strings.TrimSpace(limMem) != "" {
		q, parseErr := resource.ParseQuantity(limMem)
		if parseErr != nil {
			return fmt.Errorf("grant %q invalid spec.resources.limits.memory=%q: %v", grantName, limMem, parseErr)
		}
		container.Resources.Limits[corev1.ResourceMemory] = q
	}

	envVars, err := grantEnvVars(ctx, c.kube, grant)
	if err != nil {
		return err
	}
	if len(envVars) > 0 {
		// Override by name to avoid duplicates.
		existing := make([]corev1.EnvVar, 0, len(container.Env))
		toDrop := map[string]bool{}
		for _, ev := range envVars {
			toDrop[ev.Name] = true
		}
		for _, ev := range container.Env {
			if !toDrop[ev.Name] {
				existing = append(existing, ev)
			}
		}
		container.Env = append(existing, envVars...)
	}

	return nil
}

func grantEnvVars(ctx context.Context, kube kubernetes.Interface, grant *unstructured.Unstructured) ([]corev1.EnvVar, error) {
	if grant == nil {
		return nil, nil
	}
	grantName := grant.GetName()
	grantNamespace := strings.TrimSpace(grant.GetNamespace())
	raw, found, err := unstructured.NestedSlice(grant.Object, "spec", "env")
	if err != nil {
		return nil, fmt.Errorf("failed to read grant %q spec.env: %v", grantName, err)
	}
	if !found || len(raw) == 0 {
		return nil, nil
	}

	out := make([]corev1.EnvVar, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("grant %q spec.env[%d] must be an object", grantName, i)
		}
		name, _ := m["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("grant %q spec.env[%d].name is required", grantName, i)
		}

		if v, ok := m["value"].(string); ok {
			out = append(out, corev1.EnvVar{Name: name, Value: v})
			continue
		}

		if skr, ok := m["secretKeyRef"].(map[string]interface{}); ok {
			sec, _ := skr["name"].(string)
			key, _ := skr["key"].(string)
			sec = strings.TrimSpace(sec)
			key = strings.TrimSpace(key)
			if sec == "" || key == "" {
				return nil, fmt.Errorf("grant %q spec.env[%d].secretKeyRef.name and key are required", grantName, i)
			}

			optional := false
			if ov, ok := skr["optional"].(bool); ok {
				optional = ov
			}

			if kube == nil {
				return nil, fmt.Errorf("grant %q requires secretKeyRef for env %q, but kube client is nil", grantName, name)
			}
			if grantNamespace == "" {
				return nil, fmt.Errorf("grant %q namespace is required to resolve secretKeyRef for env %q", grantName, name)
			}
			secret, getErr := kube.CoreV1().Secrets(grantNamespace).Get(ctx, sec, metav1.GetOptions{})
			if getErr != nil {
				if apierrors.IsNotFound(getErr) && optional {
					continue
				}
				return nil, fmt.Errorf("grant %q env %q get secret %s/%s failed: %v", grantName, name, grantNamespace, sec, getErr)
			}
			if secret.Data == nil {
				if optional {
					continue
				}
				return nil, fmt.Errorf("grant %q env %q secret %s/%s has no data", grantName, name, grantNamespace, sec)
			}
			v, ok := secret.Data[key]
			if !ok {
				if optional {
					continue
				}
				return nil, fmt.Errorf("grant %q env %q secret %s/%s missing key %q", grantName, name, grantNamespace, sec, key)
			}

			out = append(out, corev1.EnvVar{Name: name, Value: string(v)})
			continue
		}

		return nil, fmt.Errorf("grant %q spec.env[%d] must set value or secretKeyRef", grantName, i)
	}
	return out, nil
}

func extractDeadlineSeconds(work *unstructured.Unstructured) int64 {
	const fallback int64 = 600
	d, found, err := unstructured.NestedInt64(work.Object, "spec", "constraints", "deadlineSeconds")
	if err != nil || !found || d <= 0 {
		return fallback
	}
	return d
}

func extractViewport(work *unstructured.Unstructured) (lon, lat, zoom float64) {
	const (
		defaultLon  = 139.76
		defaultLat  = 35.68
		defaultZoom = 11.0
	)
	lon, lat, zoom = defaultLon, defaultLat, defaultZoom

	centerField, found, err := unstructured.NestedFieldNoCopy(work.Object, "spec", "render", "viewport", "center")
	if err == nil && found {
		if center, ok := centerField.([]interface{}); ok && len(center) == 2 {
			if v, ok := toFloat64(center[0]); ok {
				lon = v
			}
			if v, ok := toFloat64(center[1]); ok {
				lat = v
			}
		}
	}

	zoomField, found, err := unstructured.NestedFieldNoCopy(work.Object, "spec", "render", "viewport", "zoom")
	if err == nil && found {
		if v, ok := toFloat64(zoomField); ok && v > 0 {
			zoom = v
		}
	}

	return lon, lat, zoom
}

func extractTileZoomRange(work *unstructured.Unstructured) (minZoom, maxZoom int) {
	const (
		defaultMin = 0
		defaultMax = 14
	)
	minZoom, maxZoom = defaultMin, defaultMax

	minField, found, err := unstructured.NestedFieldNoCopy(work.Object, "spec", "raster", "tiles", "minZoom")
	if err == nil && found {
		if v, ok := toFloat64(minField); ok {
			minZoom = int(v)
		}
	}
	maxField, found, err := unstructured.NestedFieldNoCopy(work.Object, "spec", "raster", "tiles", "maxZoom")
	if err == nil && found {
		if v, ok := toFloat64(maxField); ok {
			maxZoom = int(v)
		}
	}

	if minZoom < 0 {
		minZoom = 0
	}
	if maxZoom < minZoom {
		maxZoom = minZoom
	}
	if maxZoom > 24 {
		maxZoom = 24
	}
	return minZoom, maxZoom
}

func extractPointcloudJobs(work *unstructured.Unstructured) int {
	const defaultJobs = 2
	jobs := defaultJobs
	v, found, err := unstructured.NestedFieldNoCopy(work.Object, "spec", "pointcloud", "py3dtiles", "jobs")
	if err != nil || !found {
		return jobs
	}
	if f, ok := toFloat64(v); ok {
		jobs = int(f)
	}
	if jobs < 1 {
		jobs = 1
	}
	if jobs > 64 {
		jobs = 64
	}
	return jobs
}

func nestedStringAny(obj map[string]interface{}, fields ...string) (string, bool, error) {
	v, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if err != nil || !found || v == nil {
		return "", found, err
	}

	switch s := v.(type) {
	case string:
		return s, true, nil
	default:
		return fmt.Sprintf("%v", s), true, nil
	}
}

func nestedStringSlice(obj map[string]interface{}, fields ...string) ([]string, bool, error) {
	v, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if err != nil || !found || v == nil {
		return nil, found, err
	}

	switch raw := v.(type) {
	case []string:
		out := make([]string, 0, len(raw))
		for _, s := range raw {
			out = append(out, strings.TrimSpace(s))
		}
		return out, true, nil
	case []interface{}:
		out := make([]string, 0, len(raw))
		for i, it := range raw {
			s, ok := it.(string)
			if !ok {
				return nil, true, fmt.Errorf("%s[%d] must be a string", strings.Join(fields, "."), i)
			}
			out = append(out, strings.TrimSpace(s))
		}
		return out, true, nil
	default:
		return nil, true, fmt.Errorf("%s must be an array of strings", strings.Join(fields, "."))
	}
}

func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint64:
		return float64(n), true
	case uint32:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func int32Ptr(v int32) *int32 {
	return &v
}

func mustParseQuantity(v string) resource.Quantity {
	return resource.MustParse(v)
}

func (c *Controller) pruneArtifacts() error {
	if c.cfg.ArtifactsHostPath == "" || c.cfg.ArtifactRetention <= 0 {
		return nil
	}

	entries, err := os.ReadDir(c.cfg.ArtifactsHostPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read artifacts root %q: %w", c.cfg.ArtifactsHostPath, err)
	}

	cutoff := c.nowFunc().Add(-c.cfg.ArtifactRetention)
	for _, entry := range entries {
		path := filepath.Join(c.cfg.ArtifactsHostPath, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			c.logger.Warn("skip artifact entry due to stat error", "path", path, "error", infoErr)
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}

		if removeErr := os.RemoveAll(path); removeErr != nil {
			c.logger.Warn("failed to remove expired artifact entry", "path", path, "error", removeErr)
			continue
		}
		c.logger.Info("pruned expired artifact entry", "path", path, "modTime", info.ModTime(), "retention", c.cfg.ArtifactRetention)
	}
	return nil
}

func sanitizeDNSLabel(v string) string {
	v = strings.ToLower(v)
	var b strings.Builder
	b.Grow(len(v))
	lastHyphen := false
	for _, r := range v {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if isLower || isDigit {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}
