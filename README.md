# NEREID

`NEREID` is a Cloud Playground (self-hostable) built on k8n, Kueue, and Kyverno, where AI agents can freely request and use compute resources.

Current implemented flow:

1. Users submit a `Work` custom resource.
2. `nereid-controller` watches `Work` objects.
3. The controller creates a suspended `Job` in `nereid-work` for Kueue admission.
4. The controller updates `Work.status.phase` and `Work.status.artifactUrl`.
5. Artifacts are served from a dedicated host (default: `nereid-artifacts.yuiseki.com`) at `/<work>/`.
6. Old entries under `/var/lib/nereid/artifacts` are pruned after 30 days (configurable).

Supported `Work.spec.kind`:

- `overpassql.map.v1`
- `maplibre.style.v1`
- `duckdb.map.v1` (current scaffold)
- `gdal.rastertile.v1` (`gdalinfo` -> `gdal_translate` -> `gdalwarp` -> `gdal2tiles.py` -> web map)
- `laz.3dtiles.v1` (`pdal info` -> axis-order/CRS conversion -> `py3dtiles convert` -> Cesium preview)
- `agent.cli.v1` (run coding-agent CLIs such as Codex CLI / Gemini CLI in Kueue-admitted Jobs)

Note: `charts/nereid/templates/example-job.yaml` is a legacy single-Job scaffold.
For multi-usecase expansion, use `Work` + `nereid-controller` (kind-based job generation).

## CLI

`cmd/nereid` is a thin kubectl wrapper.

`submit` always rewrites `metadata.name` to a UUIDv7-based Work ID and uses `kubectl create`, so repeated submissions do not overwrite previous `Work` objects.
After successful submit, it also prints `artifactUrl=https://nereid-artifacts.yuiseki.com/<work>/` for easier human/agent logs.

`prompt` accepts instruction text (or a `.txt` file with bullet lines), and submits generated `Work` objects via `kubectl create`.
By default (`NEREID_PROMPT_PLANNER=auto`), it uses an LLM planner when OpenAI or Gemini API keys are set, and falls back to rule-based planning when LLM is unavailable.

Planner-related environment variables:

- `NEREID_PROMPT_PLANNER=auto|llm|rules` (default: `auto`)
- `NEREID_OPENAI_API_KEY` (or `OPENAI_API_KEY`)
- `NEREID_GEMINI_API_KEY` (or `GEMINI_API_KEY`)
- `NEREID_LLM_BASE_URL` (default: OpenAI `https://api.openai.com/v1`, Gemini `https://generativelanguage.googleapis.com/v1beta/openai`)
- `NEREID_LLM_MODEL` (optional override for any provider)
- `NEREID_GEMINI_MODEL` / `GEMINI_MODEL` (optional Gemini-specific model override; default `gemini-2.0-flash`)

Build a local binary:

```bash
go build -o ./bin/nereid ./cmd/nereid
```

Run with the built binary:

```bash
WORK_NAME=$(./bin/nereid submit examples/works/overpassql.yaml -n nereid -o name | cut -d/ -f2)
./bin/nereid watch "$WORK_NAME" -n nereid

WORK_NAME=$(./bin/nereid submit examples/works/geotiff.yaml -n nereid -o name | cut -d/ -f2)
./bin/nereid watch "$WORK_NAME" -n nereid

WORK_NAME=$(./bin/nereid submit examples/works/laz.yaml -n nereid -o name | cut -d/ -f2)
./bin/nereid watch "$WORK_NAME" -n nereid

./bin/nereid prompt examples/instructions/trident-ja.txt -n nereid --dry-run=server -o name

cat examples/instructions/trident-ja.txt | ./bin/nereid prompt - -n nereid --dry-run=server -o name
```

Agent CLI workload examples:

```bash
WORK_NAME=$(./bin/nereid submit examples/works/gemini-cli.yaml -n nereid -o name | cut -d/ -f2)
./bin/nereid watch "$WORK_NAME" -n nereid

WORK_NAME=$(./bin/nereid submit examples/works/codex-cli.yaml -n nereid -o name | cut -d/ -f2)
./bin/nereid watch "$WORK_NAME" -n nereid
```

`agent.cli.v1` supports:

- `spec.agent.image` (required)
- `spec.agent.script` (shell script)
- `spec.agent.command` + `spec.agent.args` (array-of-strings command mode)
- Playground UI submits Gemini tasks via `/api/submit-agent` and `/works/<work-name>` provides follow-up submission.

The controller injects `NEREID_WORK_NAME` and `NEREID_ARTIFACT_DIR` into the container, and also applies `Grant.spec.env`, so API keys such as `OPENAI_API_KEY` / `GEMINI_API_KEY` can be passed safely via Secret refs.

For `agent.cli.v1`, NEREID stores conversational artifacts when available:

- `user-input.txt` (from submitted prompt annotation)
- `agent.log` (raw agent stdout/stderr)
- `dialogue.txt` (`[USER]` + `[AGENT]` combined view)

For Gemini CLI workloads submitted from `/api/submit-agent`:

- NEREID writes runtime guidance to `GEMINI.md` in the artifact workspace.
- NEREID also writes `.gemini/skills/nereid-artifact-authoring/SKILL.md`.
- This keeps user input concise while preserving workspace-level execution rules.

Embed view:

- `https://nereid.yuiseki.net/works/<work-name>` (recommended)
- `https://nereid.yuiseki.net/embed?work=<work-name>` (legacy-compatible)
- or `https://nereid.yuiseki.net/embed?artifact=<artifact-url>`

`/works/<work-name>` and `/embed` show `user-input.txt` / `dialogue.txt` / `agent.log` and an iframe preview of the artifact.

```bash
WORK_NAME=$(nereid submit examples/works/overpassql.yaml -n nereid -o name | cut -d/ -f2)
nereid watch "$WORK_NAME" -n nereid
```

Equivalent development commands:

```bash
WORK_NAME=$(ASDF_GOLANG_VERSION=1.25.1 go run ./cmd/nereid submit examples/works/overpassql.yaml -n nereid -o name | cut -d/ -f2)
ASDF_GOLANG_VERSION=1.25.1 go run ./cmd/nereid watch "$WORK_NAME" -n nereid
```

## Controller

Run locally against your kubeconfig:

```bash
ASDF_GOLANG_VERSION=1.25.1 go run ./cmd/nereid-controller \
  --work-namespace nereid \
  --job-namespace nereid-work \
  --local-queue-name nereid-localq \
  --artifact-retention 720h
```

Deploy in-cluster with Helm by enabling:

- `controller.enabled=true`
- `images.controller=<your-controller-image>`
- `controller.artifactRetention=720h` (default 30 days)

## Artifact Isolation

Default chart behavior:

- UI/API: `https://nereid.yuiseki.net`
- Artifacts: `https://nereid-artifacts.yuiseki.com/`
- Directory listing for `/` is enabled.
- `/static/artifacts` is not exposed on the main host by default (`artifacts.exposeOnMainHost=false`).

Important values:

- `artifacts.servicePort=8080`
- `artifacts.ingress.host=nereid-artifacts.yuiseki.com`
- `artifacts.publicBaseUrl=https://nereid-artifacts.yuiseki.com`
- `grants.default.geminiSecretName=nereid-gemini` (default)

Gemini CLI credentials for workloads:

```bash
kubectl -n nereid create secret generic nereid-gemini \
  --from-literal=api-key="$GEMINI_API_KEY" \
  --dry-run=client -o yaml | kubectl apply -f -
```

## Cloudflared Example

If you want full host separation with Cloudflared, route the artifact host directly to the artifact service port:

```yaml
ingress:
  - hostname: nereid.yuiseki.net
    service: http://ingress-nginx-controller.ingress-nginx.svc.cluster.local:80
    originRequest:
      httpHostHeader: nereid.yuiseki.net

  - hostname: nereid-artifacts.yuiseki.com
    service: http://nereid-artifacts.nereid.svc.cluster.local:8080

  - service: http_status:404
```

Reference file: `examples/cloudflared/config.yaml`
