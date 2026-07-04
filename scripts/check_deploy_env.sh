#!/usr/bin/env bash
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

errors=0

require_value() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    echo "missing required deployment setting: ${name}" >&2
    errors=1
  fi
}

require_value ORCH_AUTH_TOKEN
require_value ORCH_DEFAULT_RECIPIENTS

if [[ -n "${ORCH_SMTP_HOST:-}" || -n "${ORCH_SMTP_USERNAME:-}" || -n "${ORCH_SMTP_PASSWORD:-}" || -n "${ORCH_SMTP_FROM:-}" ]]; then
  require_value ORCH_SMTP_HOST
  require_value ORCH_SMTP_USERNAME
  require_value ORCH_SMTP_PASSWORD
  require_value ORCH_SMTP_FROM
else
  echo "email delivery is not configured; alerts will only be simulated" >&2
  errors=1
fi

if (( errors != 0 )); then
  exit 1
fi

docker compose --profile app config --quiet
echo "deployment environment is configured"
