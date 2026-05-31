package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"sigs.k8s.io/yaml"
)

var nowFunc = time.Now
var newUUIDv7Func = uuid.NewV7

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError("subcommand is required")
	}

	switch args[0] {
	case "submit":
		return runSubmit(args[1:])
	case "watch":
		return runWatch(args[1:])
	case "prompt":
		return runPrompt(args[1:])
	case "-h", "--help", "help":
		fmt.Fprintln(os.Stdout, usageText())
		return nil
	default:
		return usageError(fmt.Sprintf("unknown subcommand: %s", args[0]))
	}
}

func runSubmit(args []string) error {
	if len(args) == 0 {
		return usageError("submit requires a work spec path")
	}

	grantName, kubectlOpts, err := splitGrantFlag(args[1:])
	if err != nil {
		return err
	}

	body, workName, err := buildTimestampedWorkSpec(args[0], nowFunc().UTC(), grantName)
	if err != nil {
		return err
	}

	kubectlArgs := []string{"create", "-f", "-"}
	kubectlArgs = append(kubectlArgs, kubectlOpts...)
	if err := runKubectlWithInput(body, kubectlArgs...); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "artifactUrl=%s\n", artifactURLForWork(workName))
	return nil
}

func runWatch(args []string) error {
	if len(args) == 0 {
		return usageError("watch requires a work name")
	}

	kubectlArgs := []string{
		"get",
		"work",
		args[0],
		"-w",
		"-o",
		"custom-columns=NAME:.metadata.name,PHASE:.status.phase,ARTIFACT:.status.artifactUrl",
	}
	kubectlArgs = append(kubectlArgs, args[1:]...)
	return runKubectl(kubectlArgs...)
}

func runPrompt(args []string) error {
	if len(args) == 0 {
		return usageError("prompt requires instruction text or a path to a text file")
	}

	source := args[0]
	grantName, kubectlOpts, err := splitGrantFlag(args[1:])
	if err != nil {
		return err
	}

	instructionText, err := readInstructionText(source)
	if err != nil {
		return err
	}

	plans, err := planWorksWithPlanner(context.Background(), instructionText)
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		return fmt.Errorf("no executable instructions found")
	}

	baseTime := nowFunc().UTC()
	for i, plan := range plans {
		injectGrantRef(plan.spec, grantName)
		body, workName, buildErr := buildGeneratedWorkSpec(plan.baseName, plan.spec, baseTime.Add(time.Duration(i)*time.Second), instructionText)
		if buildErr != nil {
			return buildErr
		}

		kubectlArgs := []string{"create", "-f", "-"}
		kubectlArgs = append(kubectlArgs, kubectlOpts...)
		if err := runKubectlWithInput(body, kubectlArgs...); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "artifactUrl=%s\n", artifactURLForWork(workName))
	}

	return nil
}

func runKubectl(args ...string) error {
	return runKubectlWithInput(nil, args...)
}

func runKubectlWithInput(input []byte, args ...string) error {
	cmd := exec.Command("kubectl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	} else {
		cmd.Stdin = os.Stdin
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("kubectl %v failed: %w", args, err)
		}
		return fmt.Errorf("failed to execute kubectl %v: %w", args, err)
	}
	return nil
}

func usageError(msg string) error {
	return fmt.Errorf("%s\n\n%s", msg, usageText())
}

func usageText() string {
	return `Usage:
  nereid submit <work-spec.yaml> [--grant <grant-name>] [kubectl create options...]
  nereid watch <work-name> [kubectl get options...]
  nereid prompt <instruction-text|instruction-file.txt> [--grant <grant-name>] [kubectl create options...]

Examples:
  WORK_NAME=$(nereid submit examples/works/overpassql.yaml -n nereid -o name | cut -d/ -f2)
  nereid watch "$WORK_NAME" -n nereid
  nereid prompt examples/instructions/trident-ja.txt -n nereid --dry-run=server -o name`
}

func buildTimestampedWorkSpec(path string, now time.Time, grantName string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read work spec %q: %w", path, err)
	}

	var obj map[string]interface{}
	if err := yaml.Unmarshal(data, &obj); err != nil {
		return nil, "", fmt.Errorf("parse work spec %q: %w", path, err)
	}

	kind, _ := obj["kind"].(string)
	if kind != "Work" {
		return nil, "", fmt.Errorf("unsupported kind %q in %s; expected Work", kind, path)
	}

	meta, _ := obj["metadata"].(map[string]interface{})
	if meta == nil {
		meta = map[string]interface{}{}
	}

	workName, err := generateWorkIDv7()
	if err != nil {
		return nil, "", err
	}
	meta["name"] = workName
	delete(meta, "resourceVersion")
	delete(meta, "uid")
	delete(meta, "generation")
	delete(meta, "managedFields")
	delete(meta, "creationTimestamp")
	obj["metadata"] = meta

	if spec, _ := obj["spec"].(map[string]interface{}); spec != nil {
		injectGrantRef(spec, grantName)
		obj["spec"] = spec
	}

	out, err := yaml.Marshal(obj)
	if err != nil {
		return nil, "", fmt.Errorf("encode timestamped work spec: %w", err)
	}
	return out, workName, nil
}

func injectGrantRef(spec map[string]interface{}, grantName string) {
	grantName = strings.TrimSpace(grantName)
	if grantName == "" || spec == nil {
		return
	}
	spec["grantRef"] = map[string]interface{}{"name": grantName}
}

func splitGrantFlag(args []string) (string, []string, error) {
	var grant string
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--grant=") {
			if grant != "" {
				return "", nil, usageError("--grant specified multiple times")
			}
			grant = strings.TrimPrefix(a, "--grant=")
			if strings.TrimSpace(grant) == "" {
				return "", nil, usageError("--grant requires a non-empty value")
			}
			continue
		}
		if a == "--grant" {
			if grant != "" {
				return "", nil, usageError("--grant specified multiple times")
			}
			if i+1 >= len(args) {
				return "", nil, usageError("--grant requires a value")
			}
			grant = args[i+1]
			i++
			if strings.TrimSpace(grant) == "" {
				return "", nil, usageError("--grant requires a non-empty value")
			}
			continue
		}
		out = append(out, a)
	}
	return grant, out, nil
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

func artifactURLForWork(workName string) string {
	base := os.Getenv("NEREID_ARTIFACT_BASE_URL")
	if base == "" {
		base = "http://nereid-artifacts.yuiseki.com"
	}
	base = strings.TrimRight(base, "/")
	return base + "/" + workName + "/"
}

type instructionWorkPlan struct {
	baseName string
	spec     map[string]interface{}
}

const (
	userPromptAnnotationKey = "nereid.yuiseki.net/user-prompt"
	maxUserPromptBytes      = 16 * 1024

	plannerProviderOpenAI = "openai"
	plannerProviderGemini = "gemini"
	plannerProviderLocal  = "local"
)

type plannerCredentials struct {
	key      string
	provider string
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
		// Prefer deterministic rules when they match, and use LLM as a fallback for
		// broader/unmatched prompts.
		rulesPlans, rulesErr := planWorksFromInstructionText(text)
		if rulesErr == nil {
			return rulesPlans, nil
		}
		if plannerAPIKey() == "" {
			return nil, rulesErr
		}
		plans, err := planWorksWithLLM(ctx, text)
		if err == nil {
			return plans, nil
		}
		return nil, fmt.Errorf("rules planner failed: %v; llm planner failed: %v", rulesErr, err)
	default:
		return nil, fmt.Errorf("unsupported NEREID_PROMPT_PLANNER=%q (use auto|llm|rules)", mode)
	}
}

func readInstructionText(source string) (string, error) {
	if source == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read instruction text from stdin: %w", err)
		}
		return string(b), nil
	}

	if info, err := os.Stat(source); err == nil && !info.IsDir() {
		b, readErr := os.ReadFile(source)
		if readErr != nil {
			return "", fmt.Errorf("read instruction file %q: %w", source, readErr)
		}
		return string(b), nil
	}

	// Fallback: treat argument as inline instruction text.
	return source, nil
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
	return plannerCredentialsFromEnv().key
}

func plannerCredentialsFromEnv() plannerCredentials {
	if v := strings.TrimSpace(os.Getenv("NEREID_LLM_API_KEY")); v != "" {
		return plannerCredentials{key: v, provider: plannerProviderLocal}
	}
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
	if strings.TrimSpace(os.Getenv("NEREID_LLM_BASE_URL")) != "" {
		return plannerCredentials{key: "", provider: plannerProviderLocal}
	}
	return plannerCredentials{}
}

func plannerBaseURL() string {
	base := strings.TrimSpace(os.Getenv("NEREID_LLM_BASE_URL"))
	if base != "" {
		return strings.TrimRight(base, "/")
	}

	switch plannerCredentialsFromEnv().provider {
	case plannerProviderGemini:
		return "https://generativelanguage.googleapis.com/v1beta/openai"
	default:
		return "https://api.openai.com/v1"
	}
}

func plannerModel() string {
	model := strings.TrimSpace(os.Getenv("NEREID_LLM_MODEL"))
	if model != "" {
		return model
	}

	if plannerCredentialsFromEnv().provider == plannerProviderGemini {
		if v := strings.TrimSpace(os.Getenv("NEREID_GEMINI_MODEL")); v != "" {
			return v
		}
		if v := strings.TrimSpace(os.Getenv("GEMINI_MODEL")); v != "" {
			return v
		}
		return "gemini-2.5-pro"
	}
	return "gpt-4o-mini"
}

func planWorksWithLLM(ctx context.Context, text string) ([]instructionWorkPlan, error) {
	key := plannerAPIKey()
	if key == "" && strings.TrimSpace(os.Getenv("NEREID_LLM_BASE_URL")) == "" {
		return nil, errors.New("llm planner requires NEREID_OPENAI_API_KEY/OPENAI_API_KEY, NEREID_GEMINI_API_KEY/GEMINI_API_KEY, or NEREID_LLM_BASE_URL for local LLM")
	}

	reqBody := map[string]interface{}{
		"model": plannerModel(),
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": plannerSystemPrompt(),
			},
			{
				"role":    "user",
				"content": text,
			},
		},
		"temperature":     0.1,
		"response_format": map[string]string{"type": "json_object"},
	}

	rawReq, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode planner request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, plannerBaseURL()+"/chat/completions", bytes.NewReader(rawReq))
	if err != nil {
		return nil, fmt.Errorf("create planner request: %w", err)
	}
	if key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
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
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	return parsePlannerWorks(content)
}

func plannerSystemPrompt() string {
	return `You are NEREID Prompt Planner. Output valid JSON only. No markdown, no explanation.

Example input: "show parks in Tokyo on a map"
Example output:
{"works":[{"baseName":"tokyo-parks","spec":{"kind":"overpassql.map.v1","title":"Parks in Tokyo","overpass":{"endpoint":"https://overpass-api.de/api/interpreter","query":"[out:json][timeout:25];\n(way[\"leisure\"=\"park\"](35.5,139.6,35.85,139.95);\nrelation[\"leisure\"=\"park\"](35.5,139.6,35.85,139.95);\n);\nout body;\n>;\nout skel qt;"},"render":{"viewport":{"center":[139.69,35.69],"zoom":12}}}}]}

Rules:
- For "show X on a map" requests use kind=overpassql.map.v1 with a valid Overpass QL query.
- Use leisure=park for parks, amenity=restaurant for restaurants, etc.
- Allowed spec.kind: overpassql.map.v1, maplibre.style.v1, duckdb.map.v1, gdal.rastertile.v1, laz.3dtiles.v1, agent.cli.v1.
- Write concise Overpass QL. Do NOT repeat filter conditions.
- Return only valid JSON.`
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
		plans = append(plans, instructionWorkPlan{
			baseName: base,
			spec:     w.Spec,
		})
	}
	return plans, nil
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

	// Accept LLM variations.
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

var reThinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

func extractJSONText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Strip chain-of-thought blocks emitted by reasoning models (e.g. Qwen3.5).
	s = strings.TrimSpace(reThinkBlock.ReplaceAllString(s, ""))
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
		// Allow through; controller validates detailed required fields.
	case "static.geojson.v1":
		files, _ := spec["staticFiles"].([]interface{})
		if len(files) == 0 {
			return errors.New(`spec.staticFiles is required for static.geojson.v1`)
		}
	case "valhalla.route.v1":
		routing, _ := spec["routing"].(map[string]interface{})
		if routing == nil {
			return errors.New(`spec.routing is required for valhalla.route.v1`)
		}
		from, _ := routing["from"].(string)
		to, _ := routing["to"].(string)
		if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
			return errors.New(`spec.routing.from and spec.routing.to are required`)
		}
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
    "maplibre": {
      "type": "vector",
      "url": "https://demotiles.maplibre.org/tiles/tiles.json"
    }
  },
  "glyphs": "https://demotiles.maplibre.org/font/{fontstack}/{range}.pbf",
  "layers": [
    { "id": "background", "type": "background", "paint": { "background-color": "#efe9dc" } },
    { "id": "countries-fill", "type": "fill", "source": "maplibre", "source-layer": "countries", "paint": { "fill-color": "#f8f8f8", "fill-opacity": 0.7 } },
    { "id": "countries-boundary", "type": "line", "source": "maplibre", "source-layer": "countries", "paint": { "line-color": "#8a8a8a", "line-width": 1 } },
    { "id": "geolines", "type": "line", "source": "maplibre", "source-layer": "geolines", "paint": { "line-color": "#4da3ff", "line-width": 1 } },
    {
      "id": "geolines-label",
      "type": "symbol",
      "source": "maplibre",
      "source-layer": "geolines",
      "layout": { "text-field": ["coalesce", ["get", "name_ja"], ["get", "name"], ["get", "name_en"]], "text-size": 11 },
      "paint": { "text-color": "#ffd400", "text-halo-color": "#111111", "text-halo-width": 1.0 }
    },
    {
      "id": "countries-label",
      "type": "symbol",
      "source": "maplibre",
      "source-layer": "centroids",
      "layout": { "text-field": ["coalesce", ["get", "name_ja"], ["get", "name"], ["get", "name_en"]], "text-size": 12 },
      "paint": { "text-color": "#0050ff", "text-halo-color": "#ffffff", "text-halo-width": 1.2 }
    }
  ]
}`,
					},
					"validate": true,
				},
				"render": map[string]interface{}{
					"viewport": map[string]interface{}{
						"center": []float64{0.0, 20.0},
						"zoom":   1.7,
					},
				},
				"constraints": map[string]interface{}{
					"deadlineSeconds": int64(300),
				},
				"artifacts": map[string]interface{}{
					"layout": "style",
				},
			},
		}, nil

	case containsAll(normalized, "人口密度", "国") && containsAny(normalized, "一番高い", "最も高い"):
		return instructionWorkPlan{
			baseName: "highest-pop-density-country",
			spec: map[string]interface{}{
				"kind":  "maplibre.style.v1",
				"title": "Highest population density country (Natural Earth estimate): Bangladesh",
				"style": map[string]interface{}{
					"sourceStyle": map[string]interface{}{
						"mode": "inline",
						"json": `{
  "version": 8,
  "sources": {
    "maplibre": {
      "type": "vector",
      "url": "https://demotiles.maplibre.org/tiles/tiles.json"
    }
  },
  "glyphs": "https://demotiles.maplibre.org/font/{fontstack}/{range}.pbf",
  "layers": [
    { "id": "background", "type": "background", "paint": { "background-color": "#f2efe7" } },
    { "id": "countries-base", "type": "fill", "source": "maplibre", "source-layer": "countries", "paint": { "fill-color": "#dddddd", "fill-opacity": 0.7 } },
    {
      "id": "country-bangladesh-highlight",
      "type": "fill",
      "source": "maplibre",
      "source-layer": "countries",
      "filter": ["==", ["coalesce", ["get", "name_en"], ["get", "name"]], "Bangladesh"],
      "paint": { "fill-color": "#e74c3c", "fill-opacity": 0.75 }
    },
    { "id": "countries-boundary", "type": "line", "source": "maplibre", "source-layer": "countries", "paint": { "line-color": "#666666", "line-width": 0.8 } },
    {
      "id": "countries-label",
      "type": "symbol",
      "source": "maplibre",
      "source-layer": "centroids",
      "layout": { "text-field": ["coalesce", ["get", "name_en"], ["get", "name"]], "text-size": 11 },
      "paint": { "text-color": "#222222", "text-halo-color": "#ffffff", "text-halo-width": 1.1 }
    }
  ]
}`,
					},
					"validate": true,
				},
				"render": map[string]interface{}{
					"viewport": map[string]interface{}{
						"center": []float64{90.3563, 23.6849},
						"zoom":   5.0,
					},
				},
				"constraints": map[string]interface{}{
					"deadlineSeconds": int64(300),
				},
				"artifacts": map[string]interface{}{
					"layout": "style",
				},
			},
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
  "sources": {
    "maplibre": {
      "type": "vector",
      "url": "https://demotiles.maplibre.org/tiles/tiles.json"
    }
  },
  "glyphs": "https://demotiles.maplibre.org/font/{fontstack}/{range}.pbf",
  "layers": [
    { "id": "background", "type": "background", "paint": { "background-color": "#f2efe7" } },
    { "id": "countries-base", "type": "fill", "source": "maplibre", "source-layer": "countries", "paint": { "fill-color": "#dddddd", "fill-opacity": 0.7 } },
    {
      "id": "country-russia-highlight",
      "type": "fill",
      "source": "maplibre",
      "source-layer": "countries",
      "filter": ["==", ["coalesce", ["get", "name_en"], ["get", "name"]], "Russia"],
      "paint": { "fill-color": "#e74c3c", "fill-opacity": 0.55 }
    },
    {
      "id": "country-japan-reference",
      "type": "fill",
      "source": "maplibre",
      "source-layer": "countries",
      "filter": ["==", ["coalesce", ["get", "name_en"], ["get", "name"]], "Japan"],
      "paint": { "fill-color": "#2980b9", "fill-opacity": 0.4 }
    },
    { "id": "countries-boundary", "type": "line", "source": "maplibre", "source-layer": "countries", "paint": { "line-color": "#666666", "line-width": 0.8 } },
    {
      "id": "countries-label",
      "type": "symbol",
      "source": "maplibre",
      "source-layer": "centroids",
      "layout": { "text-field": ["coalesce", ["get", "name_en"], ["get", "name"]], "text-size": 11 },
      "paint": { "text-color": "#222222", "text-halo-color": "#ffffff", "text-halo-width": 1.1 }
    }
  ]
}`,
					},
					"validate": true,
				},
				"render": map[string]interface{}{
					"viewport": map[string]interface{}{
						"center": []float64{120.0, 50.0},
						"zoom":   2.2,
					},
				},
				"constraints": map[string]interface{}{
					"deadlineSeconds": int64(300),
				},
				"artifacts": map[string]interface{}{
					"layout": "style",
				},
			},
		}, nil

	// Valhalla routing patterns
	case containsAny(normalized, "から", "まで") && containsAny(normalized, "ルート", "経路", "道順") ||
		containsAny(normalized, "から", "まで") && containsAny(normalized, "歩く", "歩いて", "走る", "自転車", "車で", "行き方"):
		from, to, costing := extractRoutingParams(normalized)
		return instructionWorkPlan{
			baseName: "route",
			spec: map[string]interface{}{
				"kind":  "valhalla.route.v1",
				"title": fmt.Sprintf("%s から %s への%sルート", from, to, costingLabel(costing)),
				"routing": map[string]interface{}{
					"from":    from,
					"to":      to,
					"costing": costing,
				},
				"constraints": map[string]interface{}{"deadlineSeconds": int64(300)},
				"artifacts":   map[string]interface{}{"layout": "map"},
			},
		}, nil

	// tile.yuiseki.net thematic styles
	case containsAny(normalized, "紛争", "コンフリクト", "武力衝突"):
		return tileYuisekiNetPlan("conflicts", "UCDP 武力紛争データ", 20, 10, 2.0), nil
	case containsAny(normalized, "生物多様性", "保護区", "biodiversity"):
		return tileYuisekiNetPlan("biodiversity", "世界の生物多様性・保護区", 0, 20, 1.7), nil
	case containsAny(normalized, "平和維持", "国連ミッション", "peacekeeping"):
		return tileYuisekiNetPlan("peacekeeping_network", "国連平和維持ネットワーク", 20, 10, 2.0), nil
	case containsAny(normalized, "海底ケーブル", "通信インフラ", "submarine cable") && containsAny(normalized, "インフラ", "グローバル"):
		return tileYuisekiNetPlan("global_connectivity", "グローバル通信インフラ・海底ケーブル", 0, 20, 1.7), nil
	case containsAny(normalized, "エネルギー転換", "再生可能エネルギー", "洋上風力"):
		return tileYuisekiNetPlan("energy_transition", "エネルギー転換・再生可能エネルギー", 0, 20, 1.7), nil
	case containsAny(normalized, "防災", "自然災害") && containsAny(normalized, "表示", "マップ", "地図"):
		return tileYuisekiNetPlan("disaster_prevention", "防災・自然災害リスクマップ", 0, 20, 1.7), nil
	case containsAny(normalized, "水ストレス", "水資源"):
		return tileYuisekiNetPlan("water_stress", "世界の水ストレス・水資源", 0, 20, 1.7), nil

	// static GeoJSON from z.yuiseki.net
	case containsAny(normalized, "地震", "earthquake") && containsAny(normalized, "最近", "最新", "今月"):
		return staticGeoJSONPlan("usgs-earthquakes", "最新の地震データ (USGS M4.5+)",
			[]staticFileSpec{{
				URL:          "https://z.yuiseki.net/static/geojson/usgs_m45_month.geojson",
				LayerID:      "usgs-earthquakes",
				Name:         "地震 (M4.5+, 直近30日)",
				Emoji:        "🔴",
				Color:        "rgba(255,87,34,0.6)",
				OutlineColor: "#b71c1c",
				ShowMarker:   true,
			}}, 0, 20, 1.7), nil
	case containsAll(normalized, "プレート", "境界"):
		return staticGeoJSONPlan("tectonic-plates", "プレートテクトニクス境界線",
			[]staticFileSpec{{
				URL:          "https://z.yuiseki.net/static/geojson/tectonicplates_GeoJSON_PB2002_boundaries.json",
				LayerID:      "tectonic-plates",
				Name:         "プレート境界線",
				Emoji:        "🌋",
				Color:        "rgba(156,39,176,0.8)",
				OutlineColor: "#4a148c",
				ShowMarker:   false,
			}}, 0, 20, 1.7), nil
	case containsAny(normalized, "海底ケーブル") && !containsAny(normalized, "インフラ"):
		return staticGeoJSONPlan("submarine-cables", "世界の海底ケーブル",
			[]staticFileSpec{{
				URL:          "https://z.yuiseki.net/static/geojson/cable-geo.json",
				LayerID:      "submarine-cables",
				Name:         "海底ケーブル",
				Emoji:        "🔌",
				Color:        "rgba(33,150,243,0.8)",
				OutlineColor: "#0d47a1",
				ShowMarker:   false,
			}}, 0, 20, 1.7), nil

	// foil4g PMTiles datasets (Source Cooperative)
	case containsAny(normalized, "人口密度", "人口分布", "kontur") &&
		!containsAny(normalized, "一番高い", "最も高い", "国"):
		return foil4gPMTilesPlan("kontur-population", "Kontur 世界人口密度 (H3 Hex)",
			"https://data.source.coop/smartmaps/foil4gr1/kpop.pmtiles",
			buildKonturPopulationStyle()), nil
	case containsAny(normalized, "建物", "building", "フットプリント") &&
		containsAny(normalized, "google", "グーグル", "世界", "global"):
		return foil4gPMTilesPlan("google-open-buildings", "Google Open Buildings",
			"https://data.source.coop/cholmes/google-open-buildings/google-open-buildings.pmtiles",
			buildGoogleOpenBuildingsStyle()), nil
	case containsAny(normalized, "携帯", "基地局", "セル", "opencellid"):
		return foil4gPMTilesPlan("opencellid", "OpenCelliD 携帯基地局",
			"https://data.source.coop/smartmaps/opencellid/cellid.pmtiles",
			buildOpenCellIdStyle()), nil
	case containsAny(normalized, "overture", "オーバーチャー") &&
		containsAny(normalized, "建物", "building", "POI", "表示", "マップ"):
		return foil4gPMTilesPlan("overture-maps", "Overture Maps",
			"https://z.yuiseki.net/static/overture/overture.pmtiles",
			buildOvertureMapsStyle()), nil
	}

	return instructionWorkPlan{}, fmt.Errorf("unsupported instruction line: %q", line)
}

type staticFileSpec struct {
	URL          string
	LayerID      string
	Name         string
	Emoji        string
	Color        string
	OutlineColor string
	ShowMarker   bool
}

func foil4gPMTilesPlan(baseName, title, pmtilesURL string, styleJSON string) instructionWorkPlan {
	return instructionWorkPlan{
		baseName: baseName + "-map",
		spec: map[string]interface{}{
			"kind":  "maplibre.style.v1",
			"title": title,
			"style": map[string]interface{}{
				"sourceStyle": map[string]interface{}{
					"mode": "inline",
					"json": styleJSON,
				},
				"validate": false,
			},
			"render":      map[string]interface{}{"viewport": map[string]interface{}{"center": []float64{0, 20}, "zoom": 2.0}},
			"constraints": map[string]interface{}{"deadlineSeconds": int64(300)},
			"artifacts":   map[string]interface{}{"layout": "style"},
		},
	}
}

func buildKonturPopulationStyle() string {
	return `{
  "version": 8,
  "glyphs": "https://demotiles.maplibre.org/font/{fontstack}/{range}.pbf",
  "sources": {
    "base": { "type": "vector", "url": "https://demotiles.maplibre.org/tiles/tiles.json" },
    "kpop": { "type": "vector", "url": "pmtiles://https://data.source.coop/smartmaps/foil4gr1/kpop.pmtiles", "attribution": "Kontur Population" }
  },
  "layers": [
    { "id": "background", "type": "background", "paint": { "background-color": "#1a1a2e" } },
    { "id": "countries-fill", "type": "fill", "source": "base", "source-layer": "countries", "paint": { "fill-color": "#16213e", "fill-opacity": 0.8 } },
    { "id": "countries-line", "type": "line", "source": "base", "source-layer": "countries", "paint": { "line-color": "#0f3460", "line-width": 0.5 } },
    {
      "id": "kpop-fill", "type": "fill", "source": "kpop", "source-layer": "kpop",
      "paint": {
        "fill-color": ["interpolate", ["linear"], ["number", ["get", "pop"], 0],
          0, "rgba(0,0,128,0)", 100, "rgba(0,100,255,0.4)", 1000, "rgba(0,255,200,0.6)",
          10000, "rgba(255,200,0,0.7)", 100000, "rgba(255,50,0,0.85)"],
        "fill-opacity": 0.85
      }
    },
    { "id": "countries-label", "type": "symbol", "source": "base", "source-layer": "centroids",
      "layout": { "text-field": ["coalesce", ["get", "name_en"], ["get", "name"]], "text-size": 10 },
      "paint": { "text-color": "#cccccc", "text-halo-color": "#000000", "text-halo-width": 1 } }
  ]
}`
}

func buildGoogleOpenBuildingsStyle() string {
	return `{
  "version": 8,
  "sources": {
    "base": { "type": "vector", "url": "https://demotiles.maplibre.org/tiles/tiles.json" },
    "gob": { "type": "vector", "url": "pmtiles://https://data.source.coop/cholmes/google-open-buildings/google-open-buildings.pmtiles", "attribution": "Google Open Buildings" }
  },
  "layers": [
    { "id": "background", "type": "background", "paint": { "background-color": "#f5f5f5" } },
    { "id": "countries-fill", "type": "fill", "source": "base", "source-layer": "countries", "paint": { "fill-color": "#e8e8e8" } },
    { "id": "countries-line", "type": "line", "source": "base", "source-layer": "countries", "paint": { "line-color": "#aaaaaa", "line-width": 0.5 } },
    { "id": "buildings-fill", "type": "fill", "source": "gob", "source-layer": "buildings",
      "paint": { "fill-color": "rgba(117,169,160,0.8)", "fill-outline-color": "rgba(80,130,120,0.9)" } }
  ]
}`
}

func buildOpenCellIdStyle() string {
	return `{
  "version": 8,
  "sources": {
    "base": { "type": "vector", "url": "https://demotiles.maplibre.org/tiles/tiles.json" },
    "cells": { "type": "vector", "url": "pmtiles://https://data.source.coop/smartmaps/opencellid/cellid.pmtiles", "attribution": "OpenCelliD" }
  },
  "layers": [
    { "id": "background", "type": "background", "paint": { "background-color": "#0d1117" } },
    { "id": "countries-fill", "type": "fill", "source": "base", "source-layer": "countries", "paint": { "fill-color": "#161b22", "fill-opacity": 0.9 } },
    { "id": "countries-line", "type": "line", "source": "base", "source-layer": "countries", "paint": { "line-color": "#30363d", "line-width": 0.5 } },
    { "id": "cells-circle", "type": "circle", "source": "cells", "source-layer": "a",
      "paint": { "circle-radius": 3, "circle-color": "rgba(141,211,199,0.7)", "circle-blur": 0.5 } }
  ]
}`
}

func buildOvertureMapsStyle() string {
	return `{
  "version": 8,
  "sources": {
    "overture": { "type": "vector", "url": "pmtiles://https://z.yuiseki.net/static/overture/overture.pmtiles", "attribution": "Overture Maps Foundation" }
  },
  "layers": [
    { "id": "background", "type": "background", "paint": { "background-color": "#f8f4f0" } },
    { "id": "building-fill", "type": "fill", "source": "overture", "source-layer": "building",
      "paint": { "fill-color": "rgba(141,211,199,0.8)", "fill-outline-color": "rgba(100,170,160,1)" } },
    { "id": "transportation-line", "type": "line", "source": "overture", "source-layer": "transportation",
      "paint": { "line-color": "rgba(255,255,179,0.8)", "line-width": 1 } }
  ]
}`
}

func tileYuisekiNetPlan(styleName, title string, lon, lat, zoom float64) instructionWorkPlan {
	return instructionWorkPlan{
		baseName: styleName + "-map",
		spec: map[string]interface{}{
			"kind":  "maplibre.style.v1",
			"title": title,
			"style": map[string]interface{}{
				"sourceStyle": map[string]interface{}{
					"mode": "url",
					"url":  "https://tile.yuiseki.net/styles/" + styleName + "/style.json",
				},
				"validate": false,
			},
			"render": map[string]interface{}{
				"viewport": map[string]interface{}{
					"center": []float64{lon, lat},
					"zoom":   zoom,
				},
			},
			"constraints": map[string]interface{}{"deadlineSeconds": int64(300)},
			"artifacts":   map[string]interface{}{"layout": "style"},
		},
	}
}

func staticGeoJSONPlan(baseName, title string, files []staticFileSpec, lon, lat, zoom float64) instructionWorkPlan {
	fileSpecs := make([]map[string]interface{}, len(files))
	for i, f := range files {
		fileSpecs[i] = map[string]interface{}{
			"url":          f.URL,
			"layerId":      f.LayerID,
			"name":         f.Name,
			"emoji":        f.Emoji,
			"color":        f.Color,
			"outlineColor": f.OutlineColor,
			"showMarker":   f.ShowMarker,
		}
	}
	return instructionWorkPlan{
		baseName: baseName,
		spec: map[string]interface{}{
			"kind":        "static.geojson.v1",
			"title":       title,
			"staticFiles": fileSpecs,
			"render": map[string]interface{}{
				"viewport": map[string]interface{}{
					"center": []float64{lon, lat},
					"zoom":   zoom,
				},
			},
			"constraints": map[string]interface{}{"deadlineSeconds": int64(300)},
			"artifacts":   map[string]interface{}{"layout": "map"},
		},
	}
}

func extractRoutingParams(text string) (from, to, costing string) {
	costing = "pedestrian"
	if containsAny(text, "車で", "自動車", "ドライブ") {
		costing = "auto"
	} else if containsAny(text, "自転車", "サイクル") {
		costing = "bicycle"
	}
	parts := strings.SplitN(text, "から", 2)
	if len(parts) == 2 {
		from = strings.TrimSpace(parts[0])
		from = strings.TrimPrefix(from, "。")
		rest := parts[1]
		toparts := strings.SplitN(rest, "まで", 2)
		if len(toparts) == 2 {
			to = strings.TrimSpace(toparts[0])
		}
	}
	if from == "" {
		from = "出発地"
	}
	if to == "" {
		to = "目的地"
	}
	return
}

func costingLabel(costing string) string {
	switch costing {
	case "auto":
		return "車"
	case "bicycle":
		return "自転車"
	default:
		return "徒歩"
	}
}

func buildGeneratedWorkSpec(baseName string, spec map[string]interface{}, now time.Time, userPrompt string) ([]byte, string, error) {
	workName, err := generateWorkIDv7()
	if err != nil {
		return nil, "", err
	}
	metadata := map[string]interface{}{
		"name": workName,
	}
	if promptAnnotation := userPromptAnnotationValue(userPrompt); promptAnnotation != "" {
		metadata["annotations"] = map[string]interface{}{
			userPromptAnnotationKey: promptAnnotation,
		}
	}
	obj := map[string]interface{}{
		"apiVersion": "nereid.yuiseki.net/v1alpha1",
		"kind":       "Work",
		"metadata":   metadata,
		"spec":       spec,
	}
	out, err := yaml.Marshal(obj)
	if err != nil {
		return nil, "", fmt.Errorf("encode generated work spec: %w", err)
	}
	return out, workName, nil
}

func generateWorkIDv7() (string, error) {
	id, err := newUUIDv7Func()
	if err != nil {
		return "", fmt.Errorf("generate uuidv7: %w", err)
	}
	return strings.ToLower(id.String()), nil
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
