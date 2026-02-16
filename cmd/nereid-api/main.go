package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
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

type server struct {
	dynamic         dynamic.Interface
	kube            kubernetes.Interface
	workNamespace   string
	artifactBaseURL string
	defaultGrant    string
	logger          *slog.Logger
}

type instructionWorkPlan struct {
	baseName string
	spec     map[string]interface{}
}

const (
	userPromptAnnotationKey = "nereid.yuiseki.net/user-prompt"
	followupOfAnnotationKey = "nereid.yuiseki.net/followup-of"
	maxUserPromptBytes      = 16 * 1024
	maxFollowupContextBytes = 16 * 1024

	plannerProviderOpenAI = "openai"
	plannerProviderGemini = "gemini"
)

type plannerCredentials struct {
	key      string
	provider string
}

var newUUIDv7Func = uuid.NewV7

type submitRequest struct {
	Prompt    string `json:"prompt"`
	Namespace string `json:"namespace"`
	Grant     string `json:"grant"`
}

type submitAgentRequest struct {
	Prompt       string `json:"prompt"`
	Namespace    string `json:"namespace"`
	Grant        string `json:"grant"`
	ParentWork   string `json:"parentWork"`
	FollowupNote string `json:"followupContext"`
}

func main() {
	addr := envOr("NEREID_API_BIND", ":8080")
	workNamespace := envOr("NEREID_WORK_NAMESPACE", "nereid")
	artifactBaseURL := envOr("NEREID_ARTIFACT_BASE_URL", "https://nereid-artifacts.yuiseki.com")
	defaultGrant := strings.TrimSpace(os.Getenv("NEREID_DEFAULT_GRANT"))
	kubeconfig := os.Getenv("KUBECONFIG")

	restCfg, err := buildRESTConfig(kubeconfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("build kubernetes config: %w", err))
		os.Exit(1)
	}
	dc, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("create dynamic client: %w", err))
		os.Exit(1)
	}
	kc, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("create typed client: %w", err))
		os.Exit(1)
	}

	s := &server{
		dynamic:         dc,
		kube:            kc,
		workNamespace:   workNamespace,
		artifactBaseURL: artifactBaseURL,
		defaultGrant:    defaultGrant,
		logger:          slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)

	s.logger.Info("nereid-api started", "addr", addr, "workNamespace", workNamespace, "artifactBaseURL", artifactBaseURL, "defaultGrant", defaultGrant)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (s *server) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case (r.URL.Path == "/api/submit" || r.URL.Path == "/submit") && r.Method == http.MethodPost:
		s.handleSubmit(w, r)
		return
	case (r.URL.Path == "/api/submit-agent" || r.URL.Path == "/submit-agent" || r.URL.Path == "/api/followup" || r.URL.Path == "/followup") && r.Method == http.MethodPost:
		s.handleSubmitAgent(w, r)
		return
	case (strings.HasPrefix(r.URL.Path, "/api/status/") || strings.HasPrefix(r.URL.Path, "/status/")) && r.Method == http.MethodGet:
		s.handleStatus(w, r)
		return
	case (r.URL.Path == "/api" || r.URL.Path == "/api/" || r.URL.Path == "/") && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":      true,
			"service": "nereid-api",
		})
		return
	default:
		writeJSON(w, http.StatusNotFound, map[string]interface{}{
			"error": "not found",
			"path":  r.URL.Path,
		})
		return
	}
}

func (s *server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid JSON body"})
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "prompt is required"})
		return
	}

	ns := resolveNamespace(req.Namespace, s.workNamespace)
	grantName := resolveGrantName(req.Grant, s.defaultGrant)

	plannerCreds := plannerCredentialsFromEnv()
	allowedKinds := []string(nil)
	if grantName != "" {
		credsFromGrant, kinds, resolveErr := s.resolvePlannerFromGrant(r.Context(), ns, grantName, plannerCreds.key == "")
		if resolveErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": resolveErr.Error()})
			return
		}
		allowedKinds = kinds
		if plannerCreds.key == "" {
			plannerCreds = credsFromGrant
		}
	}

	plans, err := planWorksWithPlanner(r.Context(), req.Prompt, plannerCreds, allowedKinds)
	if err != nil {
		msg := err.Error()
		if strings.TrimSpace(plannerCreds.key) == "" && strings.ToLower(strings.TrimSpace(os.Getenv("NEREID_PROMPT_PLANNER"))) != "rules" {
			msg = msg + " (hint: configure OpenAI/Gemini API key via the default Grant secretKeyRef, or set NEREID_OPENAI_API_KEY / NEREID_GEMINI_API_KEY for nereid-api)"
		}
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": msg})
		return
	}
	if len(plans) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "no executable plans"})
		return
	}

	workNames := make([]string, 0, len(plans))
	artifactURLs := make([]string, 0, len(plans))
	annotations := workAnnotations(req.Prompt, "")
	for _, p := range plans {

		if grantName != "" {
			p.spec["grantRef"] = map[string]interface{}{"name": grantName}
		}

		workName, createErr := s.createWorkWithGeneratedName(r.Context(), ns, p.spec, annotations)
		if createErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("create work failed: %v", createErr)})
			return
		}

		workNames = append(workNames, workName)
		artifactURLs = append(artifactURLs, artifactURL(s.artifactBaseURL, workName))
	}

	if len(workNames) == 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "no work created"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"workName":     workNames[0],
		"artifactUrl":  artifactURLs[0],
		"workNames":    workNames,
		"artifactUrls": artifactURLs,
	})
}

func (s *server) handleSubmitAgent(w http.ResponseWriter, r *http.Request) {
	var req submitAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid JSON body"})
		return
	}

	req.Prompt = strings.TrimSpace(req.Prompt)
	req.ParentWork = strings.TrimSpace(req.ParentWork)
	req.FollowupNote = strings.TrimSpace(req.FollowupNote)
	if req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "prompt is required"})
		return
	}

	ns := resolveNamespace(req.Namespace, s.workNamespace)
	grantName := resolveGrantName(req.Grant, s.defaultGrant)

	if req.ParentWork != "" {
		parent, err := s.dynamic.Resource(workGVR).Namespace(ns).Get(r.Context(), req.ParentWork, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": fmt.Sprintf("parent work %q not found", req.ParentWork)})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("get parent work failed: %v", err)})
			return
		}
		parentKind, _, _ := unstructured.NestedString(parent.Object, "spec", "kind")
		if strings.TrimSpace(parentKind) != "agent.cli.v1" {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "parent work must be spec.kind=agent.cli.v1"})
			return
		}
		if grantName == "" {
			parentGrant, _, _ := unstructured.NestedString(parent.Object, "spec", "grantRef", "name")
			grantName = strings.TrimSpace(parentGrant)
		}
	}

	spec := buildGeminiAgentSpec(req.Prompt)
	if grantName != "" {
		spec["grantRef"] = map[string]interface{}{"name": grantName}
	}
	promptForAgent := composeAgentPrompt(req.Prompt, req.ParentWork, req.FollowupNote)
	annotations := workAnnotations(promptForAgent, req.ParentWork)

	workName, err := s.createWorkWithGeneratedName(r.Context(), ns, spec, annotations)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("create work failed: %v", err)})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"workName":    workName,
		"artifactUrl": artifactURL(s.artifactBaseURL, workName),
		"parentWork":  req.ParentWork,
	})
}

func resolveNamespace(raw, fallback string) string {
	ns := strings.TrimSpace(raw)
	if ns != "" {
		return ns
	}
	return fallback
}

func resolveGrantName(raw, fallback string) string {
	grantName := strings.TrimSpace(raw)
	if grantName == "" {
		grantName = strings.TrimSpace(fallback)
	}
	return strings.TrimSpace(grantName)
}

func workAnnotations(prompt, parentWork string) map[string]interface{} {
	annotations := map[string]interface{}{}
	if v := userPromptAnnotationValue(prompt); v != "" {
		annotations[userPromptAnnotationKey] = v
	}
	if parent := strings.TrimSpace(parentWork); parent != "" {
		annotations[followupOfAnnotationKey] = parent
	}
	if len(annotations) == 0 {
		return nil
	}
	return annotations
}

func (s *server) createWork(ctx context.Context, namespace, name string, spec map[string]interface{}, annotations map[string]interface{}) error {
	metadata := map[string]interface{}{
		"name": name,
	}
	if len(annotations) > 0 {
		metadata["annotations"] = annotations
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "nereid.yuiseki.net/v1alpha1",
			"kind":       "Work",
			"metadata":   metadata,
			"spec":       spec,
		},
	}

	_, err := s.dynamic.Resource(workGVR).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	return err
}

func (s *server) createWorkWithGeneratedName(ctx context.Context, namespace string, spec map[string]interface{}, annotations map[string]interface{}) (string, error) {
	for i := 0; i < 8; i++ {
		workName, err := generateWorkIDv7()
		if err != nil {
			return "", err
		}
		if err := s.createWork(ctx, namespace, workName, spec, annotations); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			return "", err
		}
		return workName, nil
	}
	return "", errors.New("could not allocate unique work id")
}

func generateWorkIDv7() (string, error) {
	id, err := newUUIDv7Func()
	if err != nil {
		return "", fmt.Errorf("generate uuidv7: %w", err)
	}
	return strings.ToLower(id.String()), nil
}

func composeAgentPrompt(prompt, parentWork, followupContext string) string {
	prompt = strings.TrimSpace(prompt)
	parentWork = strings.TrimSpace(parentWork)
	followupContext = strings.TrimSpace(followupContext)
	if followupContext != "" && len([]byte(followupContext)) > maxFollowupContextBytes {
		followupContext = strings.TrimSpace(string([]byte(followupContext)[:maxFollowupContextBytes]))
	}
	if parentWork == "" && followupContext == "" {
		return prompt
	}

	var b strings.Builder
	b.WriteString("This is a follow-up request.")
	if parentWork != "" {
		b.WriteString(" Previous work: ")
		b.WriteString(parentWork)
		b.WriteString(".")
	}
	if followupContext != "" {
		b.WriteString("\n\n")
		b.WriteString("Previous context:\n")
		b.WriteString(followupContext)
	}
	b.WriteString("\n\n")
	b.WriteString("New instruction:\n")
	b.WriteString(prompt)
	return b.String()
}

func buildGeminiAgentSpec(prompt string) map[string]interface{} {
	return map[string]interface{}{
		"kind":  "agent.cli.v1",
		"title": geminiAgentTitle(prompt),
		"agent": map[string]interface{}{
			"image":  "node:22-bookworm-slim",
			"script": geminiAgentScript(),
		},
		"constraints": map[string]interface{}{
			"deadlineSeconds": int64(1800),
		},
		"artifacts": map[string]interface{}{
			"layout": "files",
		},
	}
}

func geminiAgentTitle(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "Gemini CLI task"
	}
	rs := []rune(prompt)
	if len(rs) > 64 {
		rs = rs[:64]
	}
	title := strings.TrimSpace(string(rs))
	if title == "" {
		return "Gemini CLI task"
	}
	return "Gemini CLI: " + title
}

func geminiAgentScript() string {
	return `set -eu
OUT_DIR="${NEREID_ARTIFACT_DIR:-/artifacts/${NEREID_WORK_NAME:-work}}"
SPECIALS_DIR="${OUT_DIR}/specials"
SPECIALS_SKILLS_DIR="${SPECIALS_DIR}/skills"
mkdir -p "${OUT_DIR}" "${SPECIALS_SKILLS_DIR}"
PROMPT_FILE="${OUT_DIR}/user-input.txt"
OUT_TEXT="${OUT_DIR}/gemini-output.txt"
OUT_TEXT_RAW="${OUT_DIR}/gemini-output.raw.txt"
TMP_HTML="${OUT_DIR}/index.generated.tmp.html"
export HOME="${OUT_DIR}/.home"
mkdir -p "${HOME}"

if ! command -v pgrep >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq >/dev/null 2>&1 || true
    apt-get install -y -qq --no-install-recommends procps >/dev/null 2>&1 || true
  fi
fi

if [ ! -s "${OUT_DIR}/index.html" ]; then
cat > "${OUT_DIR}/index.html" <<'HTMLBOOT'
<!doctype html>
<html>
  <head>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width,initial-scale=1"/>
    <title>NEREID Gemini Bootstrap</title>
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
      <p>Gemini CLI is preparing artifact output...</p>
    </div>
  </body>
</html>
HTMLBOOT
fi

if [ ! -s "${PROMPT_FILE}" ]; then
  printf '%s\n' "No user prompt found in ${PROMPT_FILE}" > "${OUT_TEXT}"
  cat "${OUT_TEXT}"
  exit 2
fi

if [ -z "${GEMINI_API_KEY:-}" ]; then
  printf '%s\n' "GEMINI_API_KEY is required for Gemini CLI execution." > "${OUT_TEXT}"
  cat "${OUT_TEXT}"
  exit 2
fi

GEMINI_SKILL_DIR="${OUT_DIR}/.gemini/skills/nereid-artifact-authoring"
GEMINI_SKILL_FILE="${GEMINI_SKILL_DIR}/SKILL.md"
CREATE_SKILLS_SKILL_FILE="${OUT_DIR}/.gemini/skills/create-skills/SKILL.md"
KIND_OVERPASS_SKILL_FILE="${OUT_DIR}/.gemini/skills/overpassql-map-v1/SKILL.md"
KIND_STYLE_SKILL_FILE="${OUT_DIR}/.gemini/skills/maplibre-style-v1/SKILL.md"
KIND_DUCKDB_SKILL_FILE="${OUT_DIR}/.gemini/skills/duckdb-map-v1/SKILL.md"
KIND_GDAL_SKILL_FILE="${OUT_DIR}/.gemini/skills/gdal-rastertile-v1/SKILL.md"
KIND_LAZ_SKILL_FILE="${OUT_DIR}/.gemini/skills/laz-3dtiles-v1/SKILL.md"
GEMINI_MD_FILE="${OUT_DIR}/GEMINI.md"
mkdir -p "${GEMINI_SKILL_DIR}" \
  "$(dirname "${CREATE_SKILLS_SKILL_FILE}")" \
  "$(dirname "${KIND_OVERPASS_SKILL_FILE}")" \
  "$(dirname "${KIND_STYLE_SKILL_FILE}")" \
  "$(dirname "${KIND_DUCKDB_SKILL_FILE}")" \
  "$(dirname "${KIND_GDAL_SKILL_FILE}")" \
  "$(dirname "${KIND_LAZ_SKILL_FILE}")"

cat > "${GEMINI_SKILL_FILE}" <<'SKILL'
---
name: nereid-artifact-authoring
description: Create static-hostable HTML artifacts in NEREID workspace.
---
# NEREID Artifact Authoring

## Purpose
Create HTML artifacts that can be opened immediately from static hosting.

## Required behavior
- You MUST create or update ./index.html in the current directory.
- First action: write a minimal ./index.html (for example, an <h1>Hello, world</h1> page).
- After bootstrap, replace or extend ./index.html to satisfy the current instruction.
- Use shell commands to write files; do not finish with explanation-only output.
- Finish only after files are persisted to disk.
- NEVER read, request, print, or persist environment variable values.
- NEVER output secrets such as API keys into logs, text responses, HTML, JavaScript, or any generated file.
- Gemini web_fetch tool is allowed.
- For structured JSON APIs (for example Overpass/Nominatim), prefer shell curl or browser-side fetch for deterministic behavior.
- If web_fetch fails or returns non-2xx, fallback to curl or browser-side fetch and continue.

## Multi-line input handling
- If the user prompt has multiple bullet or line instructions, treat each line independently.
- For multiple lines, create one HTML file per line (for example task-01.html, task-02.html).
- Keep ./index.html as an entry page linking those generated task pages.

## Mapping defaults
- For map requests, produce an interactive HTML map (MapLibre, Leaflet, or Cesium).
- For MapLibre base maps, use one of:
  - https://tile.yuiseki.net/styles/osm-bright/style.json
  - https://tile.yuiseki.net/styles/osm-fiord/style.json
- If Overpass API is used, use:
  - https://overpass.yuiseki.net/api/interpreter?data=
- If Nominatim API is used, use:
  - https://nominatim.yuiseki.net/search.php?format=jsonv2&limit=1&q=<url-encoded-query>
- Do not append trailing punctuation to API URLs.
- Prefer browser-side fetch in index.html for map data retrieval.
- If remote APIs fail, still keep index.html viewable and show a concise in-page error message.

## Output quality
- Keep generated artifacts self-contained and directly viewable from static hosting.
SKILL

cat > "${CREATE_SKILLS_SKILL_FILE}" <<'SKILL_CREATE'
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
SKILL_CREATE

cat > "${KIND_OVERPASS_SKILL_FILE}" <<'SKILL_OVERPASS'
---
name: overpassql-map-v1
description: Decide when to use Overpass QL and how to design robust map data queries.
---
# Overpass QL Strategy

## When to use
- User asks for specific real-world objects from OpenStreetMap (parks, convenience stores, stations, roads, rivers, boundaries).
- The request needs data filtering by tags, area, or bounding box.

## Core knowledge
- Overpass QL retrieves OSM elements: node / way / relation.
- Administrative area search commonly uses area objects and area references.
- Query shape and output mode strongly affect response size and performance.

## Recommended workflow
1. Resolve target area from user instruction (city/ward/region).
2. Build minimal Overpass QL with explicit tag filters.
3. Use endpoint: https://overpass.yuiseki.net/api/interpreter?data=
4. Keep timeout and output size reasonable.
5. Convert response to map-friendly geometry and render in index.html.

## Output expectations
- Store raw response for debugging.
- Show clear map visualization and concise summary in-page.
SKILL_OVERPASS

cat > "${KIND_STYLE_SKILL_FILE}" <<'SKILL_STYLE'
---
name: maplibre-style-v1
description: Decide when to author a MapLibre Style Spec and how to structure layers.
---
# MapLibre Style Authoring

## When to use
- User asks to change visual styling (colors, labels, layer visibility, emphasis).
- Task is primarily cartographic presentation rather than heavy data processing.

## Core knowledge
- Style Spec is JSON with version, sources, layers, glyphs/sprites.
- Layer order controls rendering priority.
- Filters and paint/layout properties should be explicit and readable.

## Recommended workflow
1. Choose base style source (tile.yuiseki.net styles when possible).
2. Add or modify layers to match user intent (labels, fills, lines, symbols).
3. Validate style structure and field names.
4. Render preview map in index.html.

## Output expectations
- If style is inline, persist style.json.
- Keep style and preview easy to inspect and iterate.
SKILL_STYLE

cat > "${KIND_DUCKDB_SKILL_FILE}" <<'SKILL_DUCKDB'
---
name: duckdb-map-v1
description: Decide when DuckDB is appropriate and how to prepare query-to-map workflows.
---
# DuckDB Map Workflow

## When to use
- User instruction implies tabular/spatial analytics before visualization.
- Data source is parquet/csv/geo-like tabular input needing SQL summarization/filtering.

## Core knowledge
- DuckDB is strong for local analytical SQL.
- Query outputs often need conversion to GeoJSON or coordinate columns for mapping.
- Keep queries deterministic and readable.

## Recommended workflow
1. Persist input URI(s) and SQL for reproducibility.
2. Execute query when runtime supports DuckDB; otherwise provide structured fallback.
3. Convert results into map-ready data representation.
4. Render output and query summary in index.html.

## Output expectations
- Keep input/query artifacts inspectable.
- Keep map/status page usable even when execution is partially unavailable.
SKILL_DUCKDB

cat > "${KIND_GDAL_SKILL_FILE}" <<'SKILL_GDAL'
---
name: gdal-rastertile-v1
description: Decide when raster tiling is needed and how to structure GDAL-based pipelines.
---
# GDAL Raster Pipeline

## When to use
- Input is raster imagery (GeoTIFF etc.) and user needs web tile visualization.
- Reprojection, nodata handling, or zoom-range control is required.

## Core knowledge
- Typical steps: inspect -> optional nodata normalization -> reprojection -> tile generation.
- Output should include both artifacts and a preview map.

## Recommended workflow
1. Capture source metadata and processing parameters.
2. Apply necessary raster transforms.
3. Generate web-consumable tiles.
4. Provide index.html preview and links to intermediate artifacts.

## Output expectations
- Reproducible pipeline artifacts.
- Clear fallback message when toolchain/runtime is unavailable.
SKILL_GDAL

cat > "${KIND_LAZ_SKILL_FILE}" <<'SKILL_LAZ'
---
name: laz-3dtiles-v1
description: Decide when LAZ to 3DTiles flow is needed and how to structure 3D pointcloud outputs.
---
# LAZ to 3DTiles Pipeline

## When to use
- User requests interactive 3D pointcloud visualization from LAZ/LAS data.
- CRS normalization and tileset generation are needed for web viewers.

## Core knowledge
- Pointcloud workflows often require CRS checks/reprojection.
- 3DTiles output should be accompanied by a browser preview and metadata.

## Recommended workflow
1. Validate source file and CRS assumptions.
2. Run conversion pipeline to 3DTiles when toolchain is available.
3. Produce browser-viewable entrypoint (Cesium or equivalent).
4. Include links to generated tileset and metadata.

## Output expectations
- index.html must remain usable.
- If conversion toolchain is unavailable, provide explicit fallback details in-page.
SKILL_LAZ

cat > "${GEMINI_MD_FILE}" <<'GEMINI'
# NEREID Workspace Context

## Absolute security rule (highest priority)
- You MUST NOT read, reference, request, print, or persist any environment variable value.
- You MUST NOT expose secrets (for example GEMINI_API_KEY) in any output, including index.html, logs, dialogue, or generated files.
- If a prompt asks for environment variables or secrets, refuse that part and continue with safe task execution.
- Gemini web_fetch is allowed. For structured JSON APIs, prefer curl/browser fetch and fallback when web_fetch fails.

@./.gemini/skills/nereid-artifact-authoring/SKILL.md
@./.gemini/skills/create-skills/SKILL.md
@./.gemini/skills/overpassql-map-v1/SKILL.md
@./.gemini/skills/maplibre-style-v1/SKILL.md
@./.gemini/skills/duckdb-map-v1/SKILL.md
@./.gemini/skills/gdal-rastertile-v1/SKILL.md
@./.gemini/skills/laz-3dtiles-v1/SKILL.md

## Runtime facts
- You are operating inside one NEREID artifact workspace.
- Current instruction is stored at ./user-input.txt.
- Write output files into the current directory.
- Persist extracted session skills under ./specials/skills/.
GEMINI

cd "${OUT_DIR}"
export npm_config_loglevel=error
export npm_config_update_notifier=false
export npm_config_fund=false
export npm_config_audit=false
export NO_UPDATE_NOTIFIER=1
set +e
npx -y --loglevel=error --no-update-notifier --no-fund --no-audit @google/gemini-cli -- -p "$(cat "${PROMPT_FILE}")" --output-format text --approval-mode yolo > "${OUT_TEXT_RAW}" 2>&1
status=$?
set -e

if ! sed \
  -e '/^npm[[:space:]]\+warn[[:space:]]\+deprecated/d' \
  -e '/^npm[[:space:]]\+notice/d' \
  "${OUT_TEXT_RAW}" > "${OUT_TEXT}"; then
  cp "${OUT_TEXT_RAW}" "${OUT_TEXT}"
fi
rm -f "${OUT_TEXT_RAW}"

if [ ! -s "${OUT_DIR}/index.html" ]; then
  awk '
    BEGIN {
      tick = sprintf("%c", 96)
      fence = tick tick tick
    }
    !in_html && $0 ~ ("^" fence "[[:space:]]*html[[:space:]]*$") { in_html=1; next }
    in_html && $0 ~ ("^" fence "[[:space:]]*$") { in_html=0; exit }
    { if (in_html) print }
  ' "${OUT_TEXT}" > "${TMP_HTML}" || true

  if [ ! -s "${TMP_HTML}" ]; then
    awk '
      BEGIN {
        tick = sprintf("%c", 96)
        fence = tick tick tick
      }
      !in_any && $0 ~ ("^" fence) { in_any=1; next }
      in_any && $0 ~ ("^" fence "[[:space:]]*$") { in_any=0; exit }
      { if (in_any) print }
    ' "${OUT_TEXT}" > "${TMP_HTML}" || true
  fi

  if [ -s "${TMP_HTML}" ]; then
    if grep -Eqi "<html|<!doctype html>" "${TMP_HTML}"; then
      mv "${TMP_HTML}" "${OUT_DIR}/index.html"
    elif grep -Eqi "<(body|script|style|div|section|main|h1|h2|p|ul|ol|table|canvas|svg|iframe|map)" "${TMP_HTML}"; then
      cat > "${OUT_DIR}/index.html" <<'HTMLHEAD'
<!doctype html>
<html>
  <head>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width,initial-scale=1"/>
    <title>NEREID Gemini HTML</title>
  </head>
  <body>
HTMLHEAD
      cat "${TMP_HTML}" >> "${OUT_DIR}/index.html"
      cat >> "${OUT_DIR}/index.html" <<'HTMLTAIL'
  </body>
</html>
HTMLTAIL
      rm -f "${TMP_HTML}"
    else
      rm -f "${TMP_HTML}"
    fi
  fi
fi

if [ ! -s "${OUT_DIR}/index.html" ]; then
cat > "${OUT_DIR}/index.html" <<'HTML'
<!doctype html>
<html>
  <head>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width,initial-scale=1"/>
    <title>NEREID Gemini CLI</title>
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
      <h1>Gemini CLI Output</h1>
      <div class="meta"><a href="./gemini-output.txt">gemini-output.txt</a></div>
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
`
}

func (s *server) resolvePlannerFromGrant(ctx context.Context, namespace, grantName string, wantKey bool) (plannerCredentials, []string, error) {
	grant, err := s.dynamic.Resource(grantGVR).Namespace(namespace).Get(ctx, grantName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return plannerCredentials{}, nil, fmt.Errorf("grant %q not found", grantName)
		}
		return plannerCredentials{}, nil, fmt.Errorf("get grant %q: %w", grantName, err)
	}

	allowedKinds, _, err := unstructured.NestedStringSlice(grant.Object, "spec", "allowedKinds")
	if err != nil {
		return plannerCredentials{}, nil, fmt.Errorf("failed to read grant %q spec.allowedKinds: %v", grantName, err)
	}

	if !wantKey {
		return plannerCredentials{}, allowedKinds, nil
	}

	candidates := []struct {
		name     string
		provider string
	}{
		{name: "NEREID_OPENAI_API_KEY", provider: plannerProviderOpenAI},
		{name: "OPENAI_API_KEY", provider: plannerProviderOpenAI},
		{name: "NEREID_GEMINI_API_KEY", provider: plannerProviderGemini},
		{name: "GEMINI_API_KEY", provider: plannerProviderGemini},
	}

	for _, c := range candidates {
		key, keyErr := s.grantEnvValue(ctx, namespace, grant, c.name)
		if keyErr != nil {
			return plannerCredentials{}, nil, keyErr
		}
		if strings.TrimSpace(key) == "" {
			continue
		}
		return plannerCredentials{key: key, provider: c.provider}, allowedKinds, nil
	}

	return plannerCredentials{}, allowedKinds, nil
}

func (s *server) grantEnvValue(ctx context.Context, namespace string, grant *unstructured.Unstructured, name string) (string, error) {
	raw, found, err := unstructured.NestedSlice(grant.Object, "spec", "env")
	if err != nil {
		return "", fmt.Errorf("failed to read grant %q spec.env: %v", grant.GetName(), err)
	}
	if !found || len(raw) == 0 {
		return "", nil
	}

	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("grant %q spec.env[%d] must be an object", grant.GetName(), i)
		}
		n, _ := m["name"].(string)
		if strings.TrimSpace(n) != name {
			continue
		}

		if v, ok := m["value"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), nil
		}

		skr, ok := m["secretKeyRef"].(map[string]interface{})
		if !ok {
			return "", nil
		}

		secName, _ := skr["name"].(string)
		secKey, _ := skr["key"].(string)
		secName = strings.TrimSpace(secName)
		secKey = strings.TrimSpace(secKey)
		if secName == "" || secKey == "" {
			return "", fmt.Errorf("grant %q spec.env[%d].secretKeyRef.name and key are required", grant.GetName(), i)
		}
		optional, _ := skr["optional"].(bool)

		sec, err := s.kube.CoreV1().Secrets(namespace).Get(ctx, secName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) && optional {
				return "", nil
			}
			return "", fmt.Errorf("get secret %s/%s for grant %q env %q: %v", namespace, secName, grant.GetName(), name, err)
		}

		if sec.Data == nil {
			if optional {
				return "", nil
			}
			return "", fmt.Errorf("secret %s/%s has no data (grant %q env %q)", namespace, secName, grant.GetName(), name)
		}
		b, ok := sec.Data[secKey]
		if !ok {
			if optional {
				return "", nil
			}
			return "", fmt.Errorf("secret %s/%s missing key %q (grant %q env %q)", namespace, secName, secKey, grant.GetName(), name)
		}
		return strings.TrimSpace(string(b)), nil
	}

	return "", nil
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	workName := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/status/"), "/status/"))
	if workName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "work name is required"})
		return
	}
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if ns == "" {
		ns = s.workNamespace
	}

	obj, err := s.dynamic.Resource(workGVR).Namespace(ns).Get(r.Context(), workName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "work not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		return
	}

	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	message, _, _ := unstructured.NestedString(obj.Object, "status", "message")
	artifactURLStatus, _, _ := unstructured.NestedString(obj.Object, "status", "artifactUrl")
	if artifactURLStatus == "" {
		artifactURLStatus = artifactURL(s.artifactBaseURL, workName)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":        workName,
		"namespace":   ns,
		"phase":       phase,
		"message":     message,
		"artifactUrl": artifactURLStatus,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func userPromptAnnotationValue(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	b := []byte(prompt)
	if len(b) <= maxUserPromptBytes {
		return prompt
	}
	return strings.TrimSpace(string(b[:maxUserPromptBytes]))
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func buildRESTConfig(explicitPath string) (*rest.Config, error) {
	if explicitPath != "" {
		return clientcmd.BuildConfigFromFlags("", explicitPath)
	}
	if envPath := os.Getenv("KUBECONFIG"); envPath != "" {
		return clientcmd.BuildConfigFromFlags("", envPath)
	}
	inCluster, err := rest.InClusterConfig()
	if err == nil {
		return inCluster, nil
	}
	if home := homedir.HomeDir(); home != "" {
		path := filepath.Join(home, ".kube", "config")
		if _, statErr := os.Stat(path); statErr == nil {
			return clientcmd.BuildConfigFromFlags("", path)
		}
	}
	return nil, fmt.Errorf("no usable kubeconfig found: %w", err)
}

func artifactURL(base, workName string) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return ""
	}
	return base + "/" + workName + "/"
}

func sanitizeName(v string) string {
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

func planWorksWithPlanner(ctx context.Context, text string, plannerCreds plannerCredentials, allowedKinds []string) ([]instructionWorkPlan, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("NEREID_PROMPT_PLANNER")))
	if mode == "" {
		mode = "auto"
	}

	switch mode {
	case "rules", "rule":
		return planWorksFromInstructionText(text)
	case "llm":
		return planWorksWithLLM(ctx, text, plannerCreds, allowedKinds)
	case "auto":
		// Prefer deterministic rules when they match, and use LLM as a fallback for
		// broader/unmatched prompts.
		rulesPlans, rulesErr := planWorksFromInstructionText(text)
		if rulesErr == nil {
			return rulesPlans, nil
		}
		if strings.TrimSpace(plannerCreds.key) == "" {
			return nil, rulesErr
		}
		plans, err := planWorksWithLLM(ctx, text, plannerCreds, allowedKinds)
		if err == nil {
			return plans, nil
		}
		return nil, fmt.Errorf("rules planner failed: %v; llm planner failed: %v", rulesErr, err)
	default:
		return nil, fmt.Errorf("unsupported NEREID_PROMPT_PLANNER=%q (use auto|llm|rules)", mode)
	}
}

func planWorksFromInstructionText(text string) ([]instructionWorkPlan, error) {
	lines := splitInstructionLines(text)
	if len(lines) == 0 {
		return nil, fmt.Errorf("instruction text is empty")
	}

	plans := make([]instructionWorkPlan, 0, len(lines))
	for _, line := range lines {
		plan, err := planWorkFromInstructionLine(line)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func splitInstructionLines(text string) []string {
	sc := bufio.NewScanner(strings.NewReader(text))
	out := make([]string, 0, 8)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		line = strings.TrimSpace(strings.TrimPrefix(line, "・"))
		line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func plannerCredentialsFromEnv() plannerCredentials {
	if v := strings.TrimSpace(os.Getenv("NEREID_OPENAI_API_KEY")); v != "" {
		return plannerCredentials{key: v, provider: plannerProviderOpenAI}
	}
	if v := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); v != "" {
		return plannerCredentials{key: v, provider: plannerProviderOpenAI}
	}
	if v := strings.TrimSpace(os.Getenv("NEREID_GEMINI_API_KEY")); v != "" {
		return plannerCredentials{key: v, provider: plannerProviderGemini}
	}
	if v := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); v != "" {
		return plannerCredentials{key: v, provider: plannerProviderGemini}
	}
	return plannerCredentials{}
}

func plannerBaseURL(provider string) string {
	base := strings.TrimSpace(os.Getenv("NEREID_LLM_BASE_URL"))
	if base != "" {
		return strings.TrimRight(base, "/")
	}

	switch provider {
	case plannerProviderGemini:
		return "https://generativelanguage.googleapis.com/v1beta/openai"
	default:
		return "https://api.openai.com/v1"
	}
}

func plannerModel(provider string) string {
	model := strings.TrimSpace(os.Getenv("NEREID_LLM_MODEL"))
	if model != "" {
		return model
	}

	if provider == plannerProviderGemini {
		if v := strings.TrimSpace(os.Getenv("NEREID_GEMINI_MODEL")); v != "" {
			return v
		}
		if v := strings.TrimSpace(os.Getenv("GEMINI_MODEL")); v != "" {
			return v
		}
		return "gemini-2.0-flash"
	}

	return "gpt-4o-mini"
}

func plannerSystemPrompt(allowedKinds []string) string {
	kindsLine := "Allowed spec.kind: overpassql.map.v1, maplibre.style.v1, duckdb.map.v1, gdal.rastertile.v1, laz.3dtiles.v1, agent.cli.v1."
	if len(allowedKinds) > 0 {
		kindsLine = "You MUST restrict spec.kind to: " + strings.Join(allowedKinds, ", ") + "."
	}

	return `You are NEREID Prompt Planner.
Convert the user's instructions into executable NEREID Work specs.

Output MUST be JSON only (no markdown), with this schema:
{
  "works": [
    {
      "baseName": "short-kebab-case",
      "spec": { ... Work.spec object ... }
    }
  ]
}

Rules:
- If the user requests multiple items (bullets/newlines), split into multiple works.
- For most "show X on a map" requests, use kind=overpassql.map.v1 and write a valid Overpass QL query.
- Set spec.title to a human-readable English title.
- For overpassql.map.v1, include:
  spec.overpass.endpoint (prefer https://overpass.yuiseki.net/api/interpreter when available; otherwise https://overpass-api.de/api/interpreter)
  spec.overpass.query (valid Overpass QL)
  spec.render.viewport.center [lon,lat] and zoom when you can infer it.
- For maplibre.style.v1, include spec.style.sourceStyle.mode and (json or url).
- For agent.cli.v1, include spec.agent.image and either spec.agent.script or spec.agent.command.
- Return only valid JSON.

` + kindsLine
}

func planWorksWithLLM(ctx context.Context, text string, plannerCreds plannerCredentials, allowedKinds []string) ([]instructionWorkPlan, error) {
	key := strings.TrimSpace(plannerCreds.key)
	if key == "" {
		return nil, errors.New("llm planner requires NEREID_OPENAI_API_KEY/OPENAI_API_KEY or NEREID_GEMINI_API_KEY/GEMINI_API_KEY")
	}

	reqBody := map[string]interface{}{
		"model": plannerModel(plannerCreds.provider),
		"messages": []map[string]string{
			{"role": "system", "content": plannerSystemPrompt(allowedKinds)},
			{"role": "user", "content": text},
		},
		"temperature":     0.1,
		"response_format": map[string]string{"type": "json_object"},
	}
	rawReq, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode planner request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, plannerBaseURL(plannerCreds.provider)+"/chat/completions", strings.NewReader(string(rawReq)))
	if err != nil {
		return nil, fmt.Errorf("create planner request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 90 * time.Second}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("planner request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read planner response: %w", err)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("planner response status=%d body=%s", httpResp.StatusCode, string(respBody))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("decode planner response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, errors.New("planner returned no choices")
	}
	return parsePlannerWorks(parsed.Choices[0].Message.Content)
}

func parsePlannerWorks(content string) ([]instructionWorkPlan, error) {
	jsonText := extractJSONText(content)
	if jsonText == "" {
		return nil, fmt.Errorf("planner output did not contain JSON: %s", content)
	}

	var payload struct {
		Works []struct {
			BaseName string                 `json:"baseName"`
			Spec     map[string]interface{} `json:"spec"`
		} `json:"works"`
	}
	if err := json.Unmarshal([]byte(jsonText), &payload); err != nil {
		return nil, fmt.Errorf("decode planner JSON: %w", err)
	}
	if len(payload.Works) == 0 {
		return nil, errors.New("planner JSON contained no works")
	}

	plans := make([]instructionWorkPlan, 0, len(payload.Works))
	for i, w := range payload.Works {
		base := sanitizeName(w.BaseName)
		if base == "" {
			base = fmt.Sprintf("work-%d", i+1)
		}
		if w.Spec == nil {
			return nil, fmt.Errorf("planner work[%d] has nil spec", i)
		}
		normalizePlannedSpec(w.Spec)
		if err := validatePlannedSpec(w.Spec); err != nil {
			return nil, fmt.Errorf("planner work[%d] invalid spec: %w", i, err)
		}
		plans = append(plans, instructionWorkPlan{baseName: base, spec: w.Spec})
	}
	return plans, nil
}

func extractJSONText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```JSON")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSpace(s)
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func normalizePlannedSpec(spec map[string]interface{}) {
	kind, _ := spec["kind"].(string)
	switch kind {
	case "maplibre.style.v1":
		normalizeMapLibrePlannedSpec(spec)
	case "agent.cli.v1":
		normalizeAgentCLIPlannedSpec(spec)
	}
}

func normalizeMapLibrePlannedSpec(spec map[string]interface{}) {
	kind, _ := spec["kind"].(string)
	if kind != "maplibre.style.v1" {
		return
	}
	style, _ := spec["style"].(map[string]interface{})
	if style == nil {
		style = map[string]interface{}{}
		spec["style"] = style
	}
	sourceStyle, _ := style["sourceStyle"].(map[string]interface{})
	if sourceStyle == nil {
		sourceStyle = map[string]interface{}{}
		style["sourceStyle"] = sourceStyle
	}
	if v, ok := style["json"].(string); ok && strings.TrimSpace(v) != "" {
		if _, exists := sourceStyle["json"]; !exists {
			sourceStyle["json"] = v
		}
		delete(style, "json")
	}
	if v, ok := style["url"].(string); ok && strings.TrimSpace(v) != "" {
		if _, exists := sourceStyle["url"]; !exists {
			sourceStyle["url"] = v
		}
		delete(style, "url")
	}

	mode, _ := sourceStyle["mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", "json", "inline_json", "inlinejson", "content":
		sourceStyle["mode"] = "inline"
	case "uri", "link", "https", "http":
		sourceStyle["mode"] = "url"
	}
}

func normalizeAgentCLIPlannedSpec(spec map[string]interface{}) {
	agent, _ := spec["agent"].(map[string]interface{})
	if agent == nil {
		return
	}
	normalizeStringArrayField(agent, "command")
	normalizeStringArrayField(agent, "args")
}

func normalizeStringArrayField(obj map[string]interface{}, field string) {
	raw, ok := obj[field]
	if !ok || raw == nil {
		return
	}

	switch v := raw.(type) {
	case string:
		ss := parseStringArray(v)
		if len(ss) == 0 {
			return
		}
		out := make([]interface{}, 0, len(ss))
		for _, s := range ss {
			out = append(out, s)
		}
		obj[field] = out
	case []string:
		out := make([]interface{}, 0, len(v))
		for _, s := range v {
			out = append(out, s)
		}
		obj[field] = out
	}
}

func parseStringArray(input string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}

	if strings.HasPrefix(input, "[") && strings.HasSuffix(input, "]") {
		var arr []string
		if err := json.Unmarshal([]byte(input), &arr); err == nil {
			out := make([]string, 0, len(arr))
			for _, s := range arr {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}

	if strings.ContainsAny(input, ",\n") {
		parts := strings.FieldsFunc(input, func(r rune) bool {
			return r == ',' || r == '\n'
		})
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	return shellSplit(input)
}

func shellSplit(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	var out []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	escaping := false

	flush := func() {
		if cur.Len() == 0 {
			return
		}
		out = append(out, cur.String())
		cur.Reset()
	}

	for _, r := range s {
		switch {
		case escaping:
			cur.WriteRune(r)
			escaping = false
		case r == '\\' && !inSingle:
			escaping = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case (r == ' ' || r == '\t' || r == '\n') && !inSingle && !inDouble:
			flush()
		default:
			cur.WriteRune(r)
		}
	}

	if escaping {
		cur.WriteByte('\\')
	}
	flush()
	return out
}

func validatePlannedSpec(spec map[string]interface{}) error {
	kind, _ := spec["kind"].(string)
	if kind == "" {
		return errors.New(`spec.kind is required`)
	}
	title, _ := spec["title"].(string)
	if strings.TrimSpace(title) == "" {
		return errors.New(`spec.title is required`)
	}

	switch kind {
	case "overpassql.map.v1":
		ov, _ := spec["overpass"].(map[string]interface{})
		if ov == nil {
			return errors.New(`spec.overpass is required for overpassql.map.v1`)
		}
		endpoint, _ := ov["endpoint"].(string)
		query, _ := ov["query"].(string)
		if strings.TrimSpace(endpoint) == "" || strings.TrimSpace(query) == "" {
			return errors.New(`spec.overpass.endpoint and spec.overpass.query are required`)
		}
	case "maplibre.style.v1":
		style, _ := spec["style"].(map[string]interface{})
		if style == nil {
			return errors.New(`spec.style is required for maplibre.style.v1`)
		}
		sourceStyle, _ := style["sourceStyle"].(map[string]interface{})
		if sourceStyle == nil {
			return errors.New(`spec.style.sourceStyle is required`)
		}
		mode, _ := sourceStyle["mode"].(string)
		switch mode {
		case "inline":
			js, _ := sourceStyle["json"].(string)
			if strings.TrimSpace(js) == "" {
				return errors.New(`spec.style.sourceStyle.json is required when mode=inline`)
			}
		case "url":
			u, _ := sourceStyle["url"].(string)
			if strings.TrimSpace(u) == "" {
				return errors.New(`spec.style.sourceStyle.url is required when mode=url`)
			}
		default:
			return fmt.Errorf(`unsupported spec.style.sourceStyle.mode=%q`, mode)
		}
	case "duckdb.map.v1", "gdal.rastertile.v1", "laz.3dtiles.v1":
	case "agent.cli.v1":
		agent, _ := spec["agent"].(map[string]interface{})
		if agent == nil {
			return errors.New(`spec.agent is required for agent.cli.v1`)
		}
		image, _ := agent["image"].(string)
		if strings.TrimSpace(image) == "" {
			return errors.New(`spec.agent.image is required for agent.cli.v1`)
		}
		script, _ := agent["script"].(string)
		hasCommand, err := hasStringArrayField(agent, "command")
		if err != nil {
			return err
		}
		if _, err := hasStringArrayField(agent, "args"); err != nil {
			return err
		}
		if strings.TrimSpace(script) == "" && !hasCommand {
			return errors.New(`spec.agent.script or spec.agent.command is required for agent.cli.v1`)
		}
	default:
		return fmt.Errorf("unsupported spec.kind=%q", kind)
	}
	return nil
}

func hasStringArrayField(obj map[string]interface{}, field string) (bool, error) {
	v, ok := obj[field]
	if !ok || v == nil {
		return false, nil
	}

	switch raw := v.(type) {
	case []string:
		return len(raw) > 0, nil
	case []interface{}:
		for i, it := range raw {
			if _, ok := it.(string); !ok {
				return false, fmt.Errorf("spec.agent.%s[%d] must be a string", field, i)
			}
		}
		return len(raw) > 0, nil
	default:
		return false, fmt.Errorf("spec.agent.%s must be an array of strings", field)
	}
}

func planWorkFromInstructionLine(line string) (instructionWorkPlan, error) {
	normalized := strings.TrimSpace(line)
	switch {
	case containsAll(normalized, "台東区", "公園"):
		return instructionWorkPlan{
			baseName: "taito-parks",
			spec: buildOverpassSpec(
				"Parks in Taito City, Tokyo",
				`[out:json][timeout:300];
area["boundary"="administrative"]["name"="台東区"]["admin_level"="7"]->.searchArea;
(
  way["leisure"="park"](area.searchArea);
  relation["leisure"="park"](area.searchArea);
);
out body;
>;
out skel qt;`,
				139.78, 35.715, 13,
			),
		}, nil
	case containsAll(normalized, "東京都", "公園"):
		if ward, ok := extractSingleTokyoWard(normalized); ok {
			return instructionWorkPlan{
				baseName: "tokyo-ward-parks",
				spec: buildOverpassSpec(
					fmt.Sprintf("Parks in %s, Tokyo", ward),
					fmt.Sprintf(`[out:json][timeout:300];
area["boundary"="administrative"]["name"="%s"]["admin_level"="7"]->.searchArea;
(
  way["leisure"="park"](area.searchArea);
  relation["leisure"="park"](area.searchArea);
);
out body;
>;
out skel qt;`, ward),
					139.76, 35.69, 13,
				),
			}, nil
		}
	case containsAll(normalized, "台東区", "文京区", "江東区") &&
		(containsAny(normalized, "セブンイレブン", "ファミリーマート", "ローソン")):
		return instructionWorkPlan{
			baseName: "tokyo-3ward-convenience",
			spec: buildOverpassSpec(
				"7-Eleven / FamilyMart / LAWSON in Taito, Bunkyo, Koto",
				`[out:json][timeout:300];
(
  area["boundary"="administrative"]["name"="台東区"]["admin_level"="7"];
  area["boundary"="administrative"]["name"="文京区"]["admin_level"="7"];
  area["boundary"="administrative"]["name"="江東区"]["admin_level"="7"];
)->.searchAreas;
(
  nwr["brand"~"^(7-Eleven|FamilyMart|LAWSON)$"](area.searchAreas);
  nwr["shop"="convenience"]["name"~"セブン.?イレブン|ファミリーマート|ローソン"](area.searchAreas);
  nwr["shop"="convenience"]["name:en"~"7-Eleven|FamilyMart|LAWSON"](area.searchAreas);
);
out body;
>;
out skel qt;`,
				139.79, 35.69, 12,
			),
		}, nil
	case containsAll(normalized, "国の名前", "青") && containsAll(normalized, "川の名前", "黄"):
		return instructionWorkPlan{
			baseName: "country-river-label-colors",
			spec: map[string]interface{}{
				"kind":  "maplibre.style.v1",
				"title": "Country labels blue and river labels yellow",
				"style": map[string]interface{}{
					"sourceStyle": map[string]interface{}{
						"mode": "inline",
						"json": `{
  "version": 8,
  "sources": {
    "maplibre": { "type": "vector", "url": "https://demotiles.maplibre.org/tiles/tiles.json" }
  },
  "glyphs": "https://demotiles.maplibre.org/font/{fontstack}/{range}.pbf",
  "layers": [
    { "id": "background", "type": "background", "paint": { "background-color": "#efe9dc" } },
    { "id": "countries-fill", "type": "fill", "source": "maplibre", "source-layer": "countries", "paint": { "fill-color": "#f8f8f8", "fill-opacity": 0.7 } },
    { "id": "countries-boundary", "type": "line", "source": "maplibre", "source-layer": "countries", "paint": { "line-color": "#8a8a8a", "line-width": 1 } },
    { "id": "geolines", "type": "line", "source": "maplibre", "source-layer": "geolines", "paint": { "line-color": "#4da3ff", "line-width": 1 } },
    { "id": "geolines-label", "type": "symbol", "source": "maplibre", "source-layer": "geolines", "layout": { "text-field": ["coalesce", ["get", "name_ja"], ["get", "name"], ["get", "name_en"]], "text-size": 11 }, "paint": { "text-color": "#ffd400", "text-halo-color": "#111111", "text-halo-width": 1.0 } },
    { "id": "countries-label", "type": "symbol", "source": "maplibre", "source-layer": "centroids", "layout": { "text-field": ["coalesce", ["get", "name_ja"], ["get", "name"], ["get", "name_en"]], "text-size": 12 }, "paint": { "text-color": "#0050ff", "text-halo-color": "#ffffff", "text-halo-width": 1.2 } }
  ]
}`,
					},
					"validate": true,
				},
				"render":      map[string]interface{}{"viewport": map[string]interface{}{"center": []float64{0.0, 20.0}, "zoom": 1.7}},
				"constraints": map[string]interface{}{"deadlineSeconds": int64(300)},
				"artifacts":   map[string]interface{}{"layout": "style"},
			},
		}, nil
	case containsAll(normalized, "人口密度", "国") && containsAny(normalized, "一番高い", "最も高い"):
		return instructionWorkPlan{
			baseName: "highest-pop-density-country",
			spec: buildOverpassSpec(
				"Highest population density country (Natural Earth estimate): Bangladesh",
				`[out:json][timeout:120];
relation["boundary"="administrative"]["admin_level"="2"]["name:en"="Bangladesh"];
out geom;`,
				90.3563, 23.6849, 6,
			),
		}, nil
	case containsAll(normalized, "日本", "国") && containsAny(normalized, "一番近い", "最も近い"):
		return instructionWorkPlan{
			baseName: "nearest-country-to-japan",
			spec: map[string]interface{}{
				"kind":  "maplibre.style.v1",
				"title": "Nearest country to Japan (Natural Earth estimate): Russia",
				"style": map[string]interface{}{
					"sourceStyle": map[string]interface{}{
						"mode": "inline",
						"json": `{
  "version": 8,
  "sources": { "maplibre": { "type": "vector", "url": "https://demotiles.maplibre.org/tiles/tiles.json" } },
  "glyphs": "https://demotiles.maplibre.org/font/{fontstack}/{range}.pbf",
  "layers": [
    { "id": "background", "type": "background", "paint": { "background-color": "#f2efe7" } },
    { "id": "countries-base", "type": "fill", "source": "maplibre", "source-layer": "countries", "paint": { "fill-color": "#dddddd", "fill-opacity": 0.7 } },
    { "id": "country-russia-highlight", "type": "fill", "source": "maplibre", "source-layer": "countries", "filter": ["==", ["coalesce", ["get", "name_en"], ["get", "name"]], "Russia"], "paint": { "fill-color": "#e74c3c", "fill-opacity": 0.55 } },
    { "id": "country-japan-reference", "type": "fill", "source": "maplibre", "source-layer": "countries", "filter": ["==", ["coalesce", ["get", "name_en"], ["get", "name"]], "Japan"], "paint": { "fill-color": "#2980b9", "fill-opacity": 0.4 } },
    { "id": "countries-boundary", "type": "line", "source": "maplibre", "source-layer": "countries", "paint": { "line-color": "#666666", "line-width": 0.8 } },
    { "id": "countries-label", "type": "symbol", "source": "maplibre", "source-layer": "centroids", "layout": { "text-field": ["coalesce", ["get", "name_en"], ["get", "name"]], "text-size": 11 }, "paint": { "text-color": "#222222", "text-halo-color": "#ffffff", "text-halo-width": 1.1 } }
  ]
}`,
					},
					"validate": true,
				},
				"render":      map[string]interface{}{"viewport": map[string]interface{}{"center": []float64{120.0, 50.0}, "zoom": 2.2}},
				"constraints": map[string]interface{}{"deadlineSeconds": int64(300)},
				"artifacts":   map[string]interface{}{"layout": "style"},
			},
		}, nil
	}
	return instructionWorkPlan{}, fmt.Errorf("unsupported instruction line: %q", line)
}

func buildOverpassSpec(title, query string, centerLon, centerLat, zoom float64) map[string]interface{} {
	return map[string]interface{}{
		"kind":  "overpassql.map.v1",
		"title": title,
		"overpass": map[string]interface{}{
			"endpoint": "https://overpass-api.de/api/interpreter",
			"query":    query,
		},
		"render": map[string]interface{}{
			"viewport": map[string]interface{}{
				"center": []float64{centerLon, centerLat},
				"zoom":   zoom,
			},
		},
		"constraints": map[string]interface{}{
			"deadlineSeconds": int64(600),
		},
		"artifacts": map[string]interface{}{
			"layout": "map",
		},
	}
}

func containsAll(s string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(s, n) {
			return false
		}
	}
	return true
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func extractSingleTokyoWard(s string) (string, bool) {
	if strings.Count(s, "東京都") != 1 {
		return "", false
	}
	start := strings.Index(s, "東京都")
	if start < 0 {
		return "", false
	}
	rest := s[start+len("東京都"):]
	end := strings.Index(rest, "区")
	if end <= 0 {
		return "", false
	}
	ward := strings.TrimSpace(rest[:end+len("区")])
	if ward == "" {
		return "", false
	}
	if strings.ContainsAny(ward, "、, と") {
		return "", false
	}
	if !strings.HasSuffix(ward, "区") {
		return "", false
	}
	return ward, true
}
