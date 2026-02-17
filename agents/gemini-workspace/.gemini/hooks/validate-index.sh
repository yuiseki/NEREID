#!/usr/bin/env bash
set -eu

HOOK_INPUT="$(cat || true)"
IS_RETRY="false"
if printf '%s' "${HOOK_INPUT}" | tr -d '\n' | grep -Eq '"stop_hook_active"[[:space:]]*:[[:space:]]*true'; then
  IS_RETRY="true"
fi

PROJECT_DIR="${GEMINI_PROJECT_DIR:-${GEMINI_CWD:-$(pwd)}}"
INDEX_FILE="${PROJECT_DIR}/index.html"
PROMPT_FILE="${PROJECT_DIR}/user-input.txt"
STATE_FILE="${PROJECT_DIR}/.gemini/.validate-index-fail-count"
RUNTIME_PATTERN="cannot read properties of undefined \\(reading 'lon'\\)|cannot read properties of undefined \\(reading 'lat'\\)|typeerror: cannot read properties of undefined"

emit_allow() {
  printf '{"decision":"allow"}\n'
}

emit_deny() {
  local msg="$1"
  msg=$(printf '%s' "${msg}" | sed 's/\\/\\\\/g; s/"/\\"/g')
  printf '{"decision":"deny","reason":"%s"}\n' "${msg}"
}

emit_stop() {
  local msg="$1"
  msg=$(printf '%s' "${msg}" | sed 's/\\/\\\\/g; s/"/\\"/g')
  printf '{"continue":false,"stopReason":"%s"}\n' "${msg}"
}

fail_validation() {
  local msg="$1"
  local count=0
  if [ -f "${STATE_FILE}" ]; then
    count="$(cat "${STATE_FILE}" 2>/dev/null || printf '0')"
    case "${count}" in
      ''|*[!0-9]*) count=0 ;;
    esac
  fi
  count=$((count + 1))
  printf '%s\n' "${count}" > "${STATE_FILE}"

  if [ "${count}" -ge 2 ] || [ "${IS_RETRY}" = "true" ]; then
    emit_stop "index.html validation still failing after retry: ${msg}"
  else
    emit_deny "index.html validation failed: ${msg}"
  fi
  exit 0
}

if [ ! -s "${INDEX_FILE}" ]; then
  fail_validation "index.html not found or empty. Create ./index.html with rendered result."
fi

if ! grep -Eqi '<!doctype html|<html[[:space:]>]|<body[[:space:]>]|<main[[:space:]>]|<div[[:space:]>]|<section[[:space:]>]|<canvas[[:space:]>]|<svg[[:space:]>]|<script[[:space:]>]|<h1[[:space:]>]|<p[[:space:]>]' "${INDEX_FILE}"; then
  fail_validation "index.html does not look like renderable HTML."
fi

if grep -Eqi 'data-nereid-bootstrap="1"|Gemini CLI is preparing artifact output' "${INDEX_FILE}"; then
  fail_validation "index.html is still bootstrap placeholder. Replace it with final output."
fi

if grep -Eqi 'YOUR_MAPLIBRE_GL_ACCESS_TOKEN|YOUR_MAPTILER_KEY|mapboxgl\.accessToken|maptiler\.com/.*key=' "${INDEX_FILE}"; then
  fail_validation "index.html contains MapLibre token placeholders or token setup. tile.yuiseki.net styles must be used without access tokens."
fi

if [ -f "${PROMPT_FILE}" ] && grep -Eqi '地図|マップ|表示|show|display|render|visualize' "${PROMPT_FILE}"; then
  has_map=false
  if grep -Eqi 'maplibre|leaflet|openlayers|deck\.gl|<canvas|<svg' "${INDEX_FILE}"; then
    has_map=true
  fi
  if grep -Eqi "id=['\"](map|root)['\"]|class=['\"][^'\"]*map[^'\"]*['\"]" "${INDEX_FILE}"; then
    has_map=true
  fi
  if grep -Eqi '<script[[:space:]]+type=["'\'']module["'\'']' "${INDEX_FILE}"; then
    has_map=true
  fi
  if [ "${has_map}" != "true" ]; then
    fail_validation "map-oriented request detected, but index.html has no map visualization markers."
  fi
fi

if [ -f "${PROMPT_FILE}" ] && grep -Eqi '公園|park' "${PROMPT_FILE}"; then
  has_park_data=false
  park_files=""
  for path in \
    "${PROJECT_DIR}/parks.geojson" \
    "${PROJECT_DIR}/parks.json" \
    "${PROJECT_DIR}/parks_feature_collection.geojson" \
    "${PROJECT_DIR}/overpass.json" \
    "${PROJECT_DIR}/poi.json"
  do
    if [ -s "${path}" ]; then
      has_park_data=true
      park_files="${park_files} ${path}"
      break
    fi
  done
  if [ "${has_park_data}" != "true" ]; then
    fail_validation "park request detected, but no non-empty park dataset artifact was produced."
  fi
  for path in ${park_files}; do
    if grep -Eqi 'placeholder|dummy|サンプル|仮データ' "${path}"; then
      fail_validation "park dataset appears fabricated (${path##*/} contains placeholder/dummy markers)."
    fi
  done
fi

for path in \
  "${PROJECT_DIR}/index.html" \
  "${PROJECT_DIR}/gemini-output.txt" \
  "${PROJECT_DIR}/agent.log" \
  "${PROJECT_DIR}/dialogue.txt" \
  "${PROJECT_DIR}/logs/agent.log" \
  "${PROJECT_DIR}/logs/dialogue.txt"
do
  if [ -f "${path}" ] && grep -Eqi "${RUNTIME_PATTERN}" "${path}"; then
    fail_validation "runtime error signature detected in ${path##*/}."
  fi
done

rm -f "${STATE_FILE}" 2>/dev/null || true
emit_allow
