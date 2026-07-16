package llm

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestOpenAIPlannerParsesStructuredResponse(t *testing.T) {
	planner := NewOpenAIPlanner("test-key", "test-model")
	planner.baseURL = "https://example.test/v1"
	planner.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected authorization header %q", got)
		}

		var req responseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "test-model" {
			t.Fatalf("expected test-model, got %q", req.Model)
		}
		if req.Text.Format.Type != "json_schema" {
			t.Fatalf("expected json_schema format, got %q", req.Text.Format.Type)
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(bytes.NewBufferString(`{
			"output": [{
				"content": [{
					"type": "output_text",
					"text": "{\"name\":\"demo transcode\",\"type\":\"video.transcode\",\"duration_ms\":5000,\"max_attempts\":2,\"should_fail\":false,\"metadata\":{\"source\":\"test\"},\"report\":{\"recipients\":[],\"subject\":\"\",\"kind\":\"custom\",\"schedule\":\"\"}}"
				}]
			}]
		}`)),
		}, nil
	})}

	plan, err := planner.Plan(t.Context(), "transcode for five seconds")
	if err != nil {
		t.Fatalf("plan job: %v", err)
	}
	if plan.Name != "demo transcode" {
		t.Fatalf("expected planned name, got %q", plan.Name)
	}
	if plan.Type != "video.transcode" {
		t.Fatalf("expected planned type, got %q", plan.Type)
	}
	if plan.DurationMS != 5000 || plan.MaxAttempts != 2 {
		t.Fatalf("unexpected planned controls: %#v", plan)
	}
	if plan.Metadata["source"] != "test" {
		t.Fatalf("expected metadata from structured output, got %#v", plan.Metadata)
	}
}

func TestOpenAIPlannerParsesWorkflowResponse(t *testing.T) {
	planner := NewOpenAIPlanner("test-key", "test-model")
	planner.baseURL = "https://example.test/v1"
	planner.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}

		var req responseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Text.Format.Name != "workflow_plan" {
			t.Fatalf("expected workflow_plan schema, got %q", req.Text.Format.Name)
		}
		if req.Text.Format.Strict {
			t.Fatal("expected workflow schema to allow job-type-specific payloads")
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(bytes.NewBufferString(`{
			"output": [{
				"content": [{
					"type": "output_text",
					"text": "{\"name\":\"nyc new grad monitor\",\"job_type\":\"jobs.monitor.new_grad\",\"interval_seconds\":86400,\"max_attempts\":2,\"enabled\":true,\"payload\":{\"keywords\":[\"new grad\"],\"locations\":[\"new york\"]},\"metadata\":{\"source\":\"test\"}}"
				}]
			}]
		}`)),
		}, nil
	})}

	plan, err := planner.PlanWorkflow(t.Context(), "monitor NYC new grad roles daily")
	if err != nil {
		t.Fatalf("plan workflow: %v", err)
	}
	if plan.Name != "nyc new grad monitor" {
		t.Fatalf("expected planned name, got %q", plan.Name)
	}
	if plan.JobType != "jobs.monitor.new_grad" {
		t.Fatalf("expected monitor job type, got %q", plan.JobType)
	}
	if plan.IntervalSeconds != 86400 || plan.MaxAttempts != 2 || !plan.Enabled {
		t.Fatalf("unexpected workflow controls: %#v", plan)
	}
	if plan.Metadata["source"] != "test" {
		t.Fatalf("expected metadata from structured output, got %#v", plan.Metadata)
	}
}

func TestOpenAIPlannerParsesCommandResponse(t *testing.T) {
	planner := NewOpenAIPlanner("test-key", "test-model")
	planner.baseURL = "https://example.test/v1"
	planner.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var req responseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Text.Format.Name != "command_plan" {
			t.Fatalf("expected command_plan schema, got %q", req.Text.Format.Name)
		}
		if req.Text.Format.Strict {
			t.Fatal("expected command plan schema to allow job-type-specific workflow payloads")
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(bytes.NewBufferString(`{
			"output": [{
				"content": [{
					"type": "output_text",
					"text": "{\"action\":\"workflow\",\"job\":{\"name\":\"fallback job\",\"type\":\"report.email\",\"duration_ms\":1000,\"max_attempts\":1,\"should_fail\":false,\"metadata\":{},\"report\":{\"recipients\":[],\"subject\":\"Report\",\"kind\":\"custom\",\"schedule\":\"immediate\"}},\"workflow\":{\"name\":\"nyc monitor\",\"job_type\":\"jobs.monitor.new_grad\",\"interval_seconds\":86400,\"max_attempts\":2,\"enabled\":true,\"payload\":{\"keywords\":[\"new grad\"]},\"metadata\":{\"source\":\"test\"}}}"
				}]
			}]
		}`)),
		}, nil
	})}

	plan, err := planner.PlanCommand(t.Context(), "monitor NYC roles daily")
	if err != nil {
		t.Fatalf("plan command: %v", err)
	}
	if plan.Action != "workflow" {
		t.Fatalf("expected workflow action, got %q", plan.Action)
	}
	if plan.Workflow.JobType != "jobs.monitor.new_grad" || plan.Workflow.IntervalSeconds != 86400 {
		t.Fatalf("unexpected workflow command plan: %#v", plan.Workflow)
	}
}

func TestNormalizeCommandPlanCanonicalizesMonitorPayload(t *testing.T) {
	plan := CommandPlan{
		Action: "workflow",
		Workflow: WorkflowPlan{
			JobType: "jobs.monitor.new_grad",
			Payload: map[string]any{
				"sources":                       []any{map[string]any{"type": "ashby", "company": "OpenAI"}},
				"new-graduate":                  "new graduate OR early career",
				"software-engineering_keywords": []any{"software engineer"},
				"senior-level_exclusions":       []any{"senior"},
				"min_score":                     float64(70),
				"notifications":                 map[string]any{"mode": "email"},
			},
		},
	}

	normalizeCommandPlan(&plan)
	payload := plan.Workflow.Payload
	if !hasValues(payload["keywords"]) || !hasValues(payload["excluded_keywords"]) {
		t.Fatalf("expected canonical monitor filters, got %#v", payload)
	}
	if payload["min_score"] != 20 {
		t.Fatalf("expected safe default score, got %#v", payload["min_score"])
	}
	notifications := payload["notifications"].(map[string]any)
	if notifications["mode"] != "immediate" {
		t.Fatalf("expected supported notification mode, got %#v", notifications)
	}
	if _, exists := payload["software-engineering_keywords"]; exists {
		t.Fatalf("expected descriptive alias to be removed: %#v", payload)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
