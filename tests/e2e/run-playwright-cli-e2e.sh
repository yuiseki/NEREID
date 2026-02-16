#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-https://nereid.yuiseki.net}"
BASE_URL="${BASE_URL%/}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
SESSION="nereid-e2e-${RUN_ID//[^0-9a-zA-Z]/}-$$"
REPORT_DIR="tests/results/${RUN_ID}"
mkdir -p "${REPORT_DIR}"

TOTAL=0
PASS=0
FAIL=0
LAST_RESULT=""

PROMPTS=(
  "東京都台東区のラーメン屋を表示してください。"
  "東京都文京区の公園を表示してください。"
  "東京都渋谷区の図書館を表示してください。"
  "東京都千代田区の交番を表示してください。"
  "東京都港区の病院を表示してください。"
  "東京都墨田区のコンビニを表示してください。"
  "東京都江東区の公園を表示してください。"
  "東京都新宿区の駅を表示してください。"
  "東京都台東区と東京都文京区の公園を色分けして表示してください。"
  "東京都台東区の公園を表示して、クリックで名前を表示してください。"
)

cleanup() {
  playwright-cli -s "${SESSION}" close >/dev/null 2>&1 || true
}
trap cleanup EXIT

run_code() {
  local code="$1"
  local log_file="$2"
  local output
  output="$(playwright-cli -s "${SESSION}" run-code "${code}" 2>&1 | tee "${log_file}")"
  if printf '%s\n' "${output}" | rg -q '^### Error'; then
    return 1
  fi
  awk '/^### Result$/{getline; print; exit}' <<<"${output}"
}

run_case() {
  local case_id="$1"
  local title="$2"
  local code="$3"
  local log_file="${REPORT_DIR}/${case_id}.log"

  TOTAL=$((TOTAL + 1))
  printf '[%s] %s ... ' "${case_id}" "${title}"

  local result
  if result="$(run_code "${code}" "${log_file}")"; then
    PASS=$((PASS + 1))
    LAST_RESULT="${result}"
    printf '%s\n' "${result}" > "${REPORT_DIR}/${case_id}.result.json"
    printf 'PASS\t%s\t%s\n' "${case_id}" "${title}" >> "${REPORT_DIR}/summary.tsv"
    echo 'PASS'
  else
    FAIL=$((FAIL + 1))
    LAST_RESULT=""
    printf 'FAIL\t%s\t%s\n' "${case_id}" "${title}" >> "${REPORT_DIR}/summary.tsv"
    echo 'FAIL'
  fi
}

js_escape_single() {
  printf '%s' "$1" | sed "s/'/\\\\'/g"
}

playwright-cli -s "${SESSION}" open "${BASE_URL}/" > "${REPORT_DIR}/00-open.log" 2>&1

for i in "${!PROMPTS[@]}"; do
  case_no=$((i + 1))
  prompt="${PROMPTS[$i]}"
  prompt_js="$(js_escape_single "${prompt}")"
  case_id=$(printf '%02d-prompt' "${case_no}")
  title="Prompt case #${case_no}"

  CASE_JS=$(cat <<JS
async (page) => {
  const prompt = '${prompt_js}';
  await page.goto('${BASE_URL}/', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#prompt-input', { timeout: 15000 });
  await page.fill('#prompt-input', prompt);
  await page.click('#submit-btn');
  await page.waitForURL(/\/works\//, { timeout: 45000 });

  const url = page.url();
  const pathMatch = url.match(/\/works\/([^/?#]+)/);
  const queryMatch = url.match(/[?&]work=([^&]+)/);
  const workId = pathMatch ? decodeURIComponent(pathMatch[1]) : (queryMatch ? decodeURIComponent(queryMatch[1]) : '');
  if (!/^[a-z0-9][a-z0-9-]{10,252}$/.test(workId)) {
    throw new Error('invalid workId: ' + workId + ' url=' + url);
  }

  await page.waitForSelector('#embed-meta', { timeout: 15000 });
  const meta = (await page.locator('#embed-meta').textContent() || '').trim();
  if (!meta.includes(workId)) {
    throw new Error('embed meta does not include workId: ' + meta);
  }

  const openHref = (await page.locator('#embed-open').getAttribute('href') || '').trim();
  if (!openHref.includes(workId)) {
    throw new Error('embed open href does not include workId: ' + openHref);
  }

  const known = ['Submitted', 'Queued', 'Running', 'Succeeded', 'Failed', 'Error'];
  let st = null;
  let phase = '';
  for (let i = 0; i < 10; i++) {
    const res = await page.request.get('${BASE_URL}/api/status/' + encodeURIComponent(workId) + '?ts=' + Date.now());
    if (!res.ok()) throw new Error('status api failed: ' + res.status());
    st = await res.json();
    phase = String((st && st.phase) || '');
    if (phase !== '') break;
    await page.waitForTimeout(500);
  }

  if (!st) throw new Error('status response is empty');
  if (phase !== '' && !known.includes(phase)) {
    throw new Error('unknown phase: ' + phase);
  }

  return {
    prompt,
    workId,
    phase: phase || 'Pending',
    message: String((st && st.message) || ''),
    artifactUrl: String(st.artifactUrl || ''),
    embedMeta: meta
  };
}
JS
)

  run_case "${case_id}" "${title}" "${CASE_JS}"
done

{
  echo "# NEREID Prompt-Variation E2E Summary"
  echo
  echo "- Run ID: ${RUN_ID}"
  echo "- Base URL: ${BASE_URL}"
  echo "- Session: ${SESSION}"
  echo "- Total: ${TOTAL}"
  echo "- Passed: ${PASS}"
  echo "- Failed: ${FAIL}"
  echo
  echo "## Case Results"
  echo
  for i in "${!PROMPTS[@]}"; do
    case_no=$((i + 1))
    case_id=$(printf '%02d-prompt' "${case_no}")
    result_file="${REPORT_DIR}/${case_id}.result.json"
    if [ -f "${result_file}" ]; then
      line=$(node -e 'const j=require("fs").readFileSync(process.argv[1],"utf8"); const o=JSON.parse(j); process.stdout.write(`${o.workId}\t${o.phase}\t${o.prompt}`);' "${result_file}" 2>/dev/null || true)
      work_id=$(printf '%s' "${line}" | cut -f1)
      phase=$(printf '%s' "${line}" | cut -f2)
      prompt=$(printf '%s' "${line}" | cut -f3-)
      echo "- ${case_id}: ${phase} / ${work_id} / ${prompt}"
    else
      echo "- ${case_id}: FAILED"
    fi
  done
} > "${REPORT_DIR}/summary.md"

printf 'Total=%d Passed=%d Failed=%d\n' "${TOTAL}" "${PASS}" "${FAIL}"
printf 'ReportDir=%s\n' "${REPORT_DIR}"

if [ "${FAIL}" -ne 0 ]; then
  exit 1
fi
