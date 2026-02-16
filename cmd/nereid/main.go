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
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

var nowFunc = time.Now

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

	baseName, _ := meta["name"].(string)
	if baseName == "" {
		baseName = "work"
	}
	workName := buildTimestampedName(baseName, now)
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
		return "gemini-2.0-flash"
	}
	return "gpt-4o-mini"
}

func planWorksWithLLM(ctx context.Context, text string) ([]instructionWorkPlan, error) {
	key := plannerAPIKey()
	if key == "" {
		return nil, errors.New("llm planner requires NEREID_OPENAI_API_KEY/OPENAI_API_KEY or NEREID_GEMINI_API_KEY/GEMINI_API_KEY")
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
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	return parsePlannerWorks(content)
}

func plannerSystemPrompt() string {
	return `You are NEREID Prompt Planner.
Convert the user's mapping instructions into executable NEREID Work specs.

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
- Generate one work per instruction item when multiple items are requested.
- Allowed spec.kind: overpassql.map.v1, maplibre.style.v1, duckdb.map.v1, gdal.rastertile.v1, laz.3dtiles.v1, agent.cli.v1.
- For overpassql.map.v1, include:
  spec.title, spec.overpass.endpoint="https://overpass-api.de/api/interpreter", spec.overpass.query.
- For maplibre.style.v1, include:
  spec.title, spec.style.sourceStyle.mode, and style JSON/url.
- For agent.cli.v1, include:
  spec.title, spec.agent.image, and either spec.agent.script or spec.agent.command.
- Include spec.render.viewport.center [lon,lat] and zoom when possible.
- Include spec.constraints.deadlineSeconds and spec.artifacts.layout.
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
	}

	return instructionWorkPlan{}, fmt.Errorf("unsupported instruction line: %q", line)
}

func buildGeneratedWorkSpec(baseName string, spec map[string]interface{}, now time.Time, userPrompt string) ([]byte, string, error) {
	workName := buildTimestampedName(baseName, now)
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
