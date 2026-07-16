#!/usr/bin/env bash
set -euo pipefail

base_url="${ORCH_MONITOR_URL:-http://localhost:8080}"
interval_seconds="${ORCH_MONITOR_INTERVAL_SECONDS:-3600}"
default_locations='["new york", "nyc", "san francisco", "remote"]'
curl_auth=()
if [[ -n "${ORCH_AUTH_TOKEN:-}" ]]; then
  curl_auth=(-H "Authorization: Bearer ${ORCH_AUTH_TOKEN}")
fi
existing_workflows="$(curl -fsS "${base_url}/v1/workflows" "${curl_auth[@]}")"

create_monitor() {
  local slug="$1"
  local company="$2"
  local source_type="$3"
  local source_field="$4"
  local source_value="$5"
  local locations="${6:-${default_locations}}"

  local name="${slug}-job-monitor"
  if [[ "${existing_workflows}" == *"\"name\":\"${name}\""* ]]; then
    echo "Skipping existing ${name}"
    return
  fi

  local payload
  payload="{
    \"name\": \"${name}\",
    \"job_type\": \"jobs.monitor.new_grad\",
    \"max_attempts\": 2,
    \"enabled\": true,
    \"interval_seconds\": ${interval_seconds},
    \"payload\": {
      \"sources\": [
        {
          \"type\": \"${source_type}\",
          \"company\": \"${company}\",
          \"${source_field}\": \"${source_value}\"
        }
      ],
      \"keywords\": [\"new grad\", \"early career\", \"university\", \"software engineer\", \"software engineering\"],
      \"locations\": ${locations},
      \"excluded_keywords\": [\"senior\", \"staff\", \"principal\", \"manager\", \"director\"],
      \"min_score\": 20,
      \"notifications\": {
        \"mode\": \"immediate\"
      }
    },
    \"metadata\": {
      \"submitted_by\": \"seed-job-monitors\"
    }
  }"

  echo "Creating ${name} against ${base_url}"
  response="$(curl -fsS -X POST "${base_url}/v1/workflows" \
    "${curl_auth[@]}" \
    -H "Content-Type: application/json" \
    -d "${payload}")"
  echo "${response}"
  echo
  existing_workflows+="${response}"
}

create_monitor "anduril" "Anduril" "greenhouse" "board_token" "andurilindustries" \
  '["new york", "nyc", "san francisco", "costa mesa", "remote"]'
create_monitor "palantir" "Palantir" "lever" "account_name" "palantir" \
  '["new york", "nyc", "washington", "palo alto", "denver", "remote"]'
create_monitor "stripe" "Stripe" "greenhouse" "board_token" "stripe"

create_monitor "openai" "OpenAI" "ashby" "job_board_name" "openai"
create_monitor "anthropic" "Anthropic" "greenhouse" "board_token" "anthropic"
create_monitor "databricks" "Databricks" "greenhouse" "board_token" "databricks"
create_monitor "ramp" "Ramp" "ashby" "job_board_name" "ramp"
create_monitor "figma" "Figma" "greenhouse" "board_token" "figma"
create_monitor "plaid" "Plaid" "ashby" "job_board_name" "plaid"
create_monitor "perplexity" "Perplexity" "ashby" "job_board_name" "perplexity"
create_monitor "scale-ai" "Scale AI" "greenhouse" "board_token" "scaleai" \
  '["new york", "nyc", "san francisco", "st. louis", "remote"]'

create_monitor "cursor" "Cursor" "ashby" "job_board_name" "cursor"
create_monitor "xai" "xAI" "greenhouse" "board_token" "xai"
create_monitor "vercel" "Vercel" "greenhouse" "board_token" "vercel"
create_monitor "linear" "Linear" "ashby" "job_board_name" "linear"
create_monitor "replit" "Replit" "ashby" "job_board_name" "replit"
create_monitor "modal" "Modal" "ashby" "job_board_name" "modal"
create_monitor "baseten" "Baseten" "ashby" "job_board_name" "baseten"

# Selective aerospace and product-engineering companies with verified public ATS feeds.
# SpaceX's board includes Starlink roles under the same Greenhouse token.
create_monitor "spacex-starlink" "SpaceX / Starlink" "greenhouse" "board_token" "spacex" \
  '["hawthorne", "bastrop", "redmond", "sunnyvale", "starbase", "cape canaveral", "remote"]'
create_monitor "notion" "Notion" "ashby" "job_board_name" "notion"
create_monitor "robinhood" "Robinhood" "greenhouse" "board_token" "robinhood"

# Additional high-signal infrastructure, developer-platform, and aerospace
# companies. Each identifier is verified against its public ATS endpoint.
create_monitor "cloudflare" "Cloudflare" "greenhouse" "board_token" "cloudflare"
create_monitor "mongodb" "MongoDB" "greenhouse" "board_token" "mongodb"
create_monitor "cockroach-labs" "Cockroach Labs" "greenhouse" "board_token" "cockroachlabs"
create_monitor "astranis" "Astranis" "greenhouse" "board_token" "astranis" \
  '["san francisco", "remote"]'
create_monitor "zipline" "Zipline" "greenhouse" "board_token" "flyzipline" \
  '["san francisco", "south san francisco", "remote"]'
create_monitor "relativity-space" "Relativity Space" "greenhouse" "board_token" "relativity" \
  '["long beach", "los angeles", "remote"]'
create_monitor "confido" "Confido" "ashby" "job_board_name" "confido" \
  '["new york", "nyc", "remote"]'
create_monitor "zettabyte" "Zettabyte" "ashby" "job_board_name" "zettabyte-space"
create_monitor "whoop" "WHOOP" "lever" "account_name" "whoop" \
  '["boston", "remote"]'

# High-upside early-career and new-grad boards. Some may have no matching role
# today; keeping the monitor active catches the next opening as it is published.
create_monitor "kernel" "Kernel" "ashby" "job_board_name" "usekernel"
create_monitor "foxglove" "Foxglove" "ashby" "job_board_name" "foxglove" \
  '["san francisco", "remote"]'
create_monitor "benchling" "Benchling" "ashby" "job_board_name" "benchling" \
  '["san francisco", "remote"]'
create_monitor "hipp-health" "Hipp Health" "ashby" "job_board_name" "hipp" \
  '["remote", "united states"]'
create_monitor "hifi" "HIFI" "ashby" "job_board_name" "hifi" \
  '["new york", "nyc", "remote"]'
create_monitor "mirage" "Mirage" "ashby" "job_board_name" "mirage" \
  '["new york", "nyc"]'
create_monitor "meshy" "Meshy" "ashby" "job_board_name" "meshy" \
  '["san francisco", "seattle", "new york", "vancouver", "toronto", "remote"]'
create_monitor "eventual" "Eventual" "ashby" "job_board_name" "eventualcomputing" \
  '["san francisco", "remote"]'
create_monitor "cerebras" "Cerebras" "ashby" "job_board_name" "cerebras" \
  '["sunnyvale", "san francisco", "remote"]'

echo "Seeded job monitors."
echo "Use the Workflows dashboard or POST /v1/workflows/<id>/run to run one immediately."
