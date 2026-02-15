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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var workGVR = schema.GroupVersionResource{
	Group:    "nereid.yuiseki.net",
	Version:  "v1alpha1",
	Resource: "works",
}

type server struct {
	dynamic         dynamic.Interface
	workNamespace   string
	artifactBaseURL string
	defaultGrant    string
	logger          *slog.Logger
}

type instructionWorkPlan struct {
	baseName string
	spec     map[string]interface{}
}

func main() {
	addr := envOr("NEREID_API_BIND", ":8080")
	workNamespace := envOr("NEREID_WORK_NAMESPACE", "nereid")
	artifactBaseURL := envOr("NEREID_ARTIFACT_BASE_URL", "http://nereid-artifacts.yuiseki.com")
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

	s := &server{
		dynamic:         dc,
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
	var req struct {
		Prompt    string `json:"prompt"`
		Namespace string `json:"namespace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid JSON body"})
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "prompt is required"})
		return
	}

	ns := s.workNamespace
	if strings.TrimSpace(req.Namespace) != "" {
		ns = strings.TrimSpace(req.Namespace)
	}

	plans, err := planWorksWithPlanner(r.Context(), req.Prompt)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	if len(plans) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "no executable plans"})
		return
	}

	now := time.Now().UTC()
	workNames := make([]string, 0, len(plans))
	artifactURLs := make([]string, 0, len(plans))
	for i, p := range plans {
		workName := buildTimestampedName(p.baseName, now.Add(time.Duration(i)*time.Second))

		if s.defaultGrant != "" {
			p.spec["grantRef"] = map[string]interface{}{"name": s.defaultGrant}
		}

		obj := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "nereid.yuiseki.net/v1alpha1",
				"kind":       "Work",
				"metadata": map[string]interface{}{
					"name": workName,
				},
				"spec": p.spec,
			},
		}

		if _, createErr := s.dynamic.Resource(workGVR).Namespace(ns).Create(r.Context(), obj, metav1.CreateOptions{}); createErr != nil {
			if apierrors.IsAlreadyExists(createErr) {
				continue
			}
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

func buildTimestampedName(base string, now time.Time) string {
	prefix := now.UTC().Format("20060102-1504")
	base = sanitizeName(base)
	if base == "" {
		base = "work"
	}

	const maxLen = 63
	maxBase := maxLen - len(prefix) - 1
	if maxBase < 1 {
		maxBase = 1
	}
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
	}
	if base == "" {
		base = "work"
	}
	return prefix + "-" + base
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

func planWorksWithPlanner(ctx context.Context, text string) ([]instructionWorkPlan, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("NEREID_PROMPT_PLANNER")))
	if mode == "" {
		mode = "auto"
	}

	switch mode {
	case "rules", "rule":
		return planWorksFromInstructionText(text)
	case "llm":
		return planWorksWithLLM(ctx, text)
	case "auto":
		if plannerAPIKey() == "" {
			return planWorksFromInstructionText(text)
		}
		plans, err := planWorksWithLLM(ctx, text)
		if err == nil {
			return plans, nil
		}
		return planWorksFromInstructionText(text)
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

func plannerAPIKey() string {
	if v := strings.TrimSpace(os.Getenv("NEREID_OPENAI_API_KEY")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
}

func plannerBaseURL() string {
	base := strings.TrimSpace(os.Getenv("NEREID_LLM_BASE_URL"))
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return strings.TrimRight(base, "/")
}

func plannerModel() string {
	model := strings.TrimSpace(os.Getenv("NEREID_LLM_MODEL"))
	if model == "" {
		model = "gpt-4o-mini"
	}
	return model
}

func plannerSystemPrompt() string {
	return `You are NEREID Prompt Planner.
Convert the user's mapping instructions into executable NEREID Work specs.
Output MUST be JSON only:
{"works":[{"baseName":"short-kebab-case","spec":{...}}]}
Allowed spec.kind: overpassql.map.v1, maplibre.style.v1, duckdb.map.v1, gdal.rastertile.v1, laz.3dtiles.v1.`
}

func planWorksWithLLM(ctx context.Context, text string) ([]instructionWorkPlan, error) {
	key := plannerAPIKey()
	if key == "" {
		return nil, errors.New("llm planner requires NEREID_OPENAI_API_KEY or OPENAI_API_KEY")
	}

	reqBody := map[string]interface{}{
		"model": plannerModel(),
		"messages": []map[string]string{
			{"role": "system", "content": plannerSystemPrompt()},
			{"role": "user", "content": text},
		},
		"temperature":     0.1,
		"response_format": map[string]string{"type": "json_object"},
	}
	rawReq, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode planner request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, plannerBaseURL()+"/chat/completions", strings.NewReader(string(rawReq)))
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
	default:
		return fmt.Errorf("unsupported spec.kind=%q", kind)
	}
	return nil
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
