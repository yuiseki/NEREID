#!/usr/bin/env bash
set -eu

HOOK_INPUT="$(cat || true)"
IS_RETRY="false"
if printf '%s' "${HOOK_INPUT}" | tr -d '\n' | grep -Eq '"stop_hook_active"[[:space:]]*:[[:space:]]*true'; then
  IS_RETRY="true"
fi

PROJECT_DIR="${GEMINI_PROJECT_DIR:-${GEMINI_CWD:-$(pwd)}}"
INDEX_FILE="${PROJECT_DIR}/index.html"
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
  if [ "${IS_RETRY}" = "true" ]; then
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

emit_allow
