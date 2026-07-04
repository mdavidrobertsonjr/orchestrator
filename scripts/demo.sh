#!/usr/bin/env bash
set -euo pipefail

base_url="${ORCH_DEMO_URL:-http://localhost:8080}"
curl_auth=()
if [[ -n "${ORCH_AUTH_TOKEN:-}" ]]; then
  curl_auth=(-H "Authorization: Bearer ${ORCH_AUTH_TOKEN}")
fi

payload='{
  "name": "demo-new-grad-monitor",
  "job_type": "jobs.monitor.new_grad",
  "max_attempts": 2,
  "enabled": true,
  "interval_seconds": 900,
  "payload": {
    "sources": [
      {
        "type": "fake",
        "company": "Datadog",
        "postings": [
          {
            "company": "Datadog",
            "title": "Software Engineer, New Grad",
            "url": "https://example.com/datadog-new-grad-demo",
            "location": "New York, NY",
            "source": "fake",
            "source_id": "demo-dd-1"
          }
        ]
      }
    ],
    "keywords": ["new grad", "software engineer"],
    "locations": ["new york", "nyc"],
    "excluded_keywords": ["senior", "staff"],
    "min_score": 20,
    "notification_mode": "immediate"
  },
  "metadata": {
    "submitted_by": "make-demo"
  }
}'

echo "Creating fake job-monitor workflow against ${base_url}"
workflow="$(curl -fsS -X POST "${base_url}/v1/workflows" "${curl_auth[@]}" -H "Content-Type: application/json" -d "${payload}")"
echo "${workflow}"

workflow_id="$(printf "%s" "${workflow}" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')"
if [[ -z "${workflow_id}" ]]; then
  echo "Could not parse workflow id" >&2
  exit 1
fi

echo "Running workflow ${workflow_id} now"
curl -fsS -X POST "${base_url}/v1/workflows/${workflow_id}/run" "${curl_auth[@]}"
echo

sleep 2

echo "Postings:"
curl -fsS "${base_url}/v1/postings" "${curl_auth[@]}"
echo

echo "Workflow runs:"
curl -fsS "${base_url}/v1/workflow-runs?workflow_id=${workflow_id}" "${curl_auth[@]}"
echo
