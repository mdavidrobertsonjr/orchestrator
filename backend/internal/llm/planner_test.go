package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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
				"sources":                       []any{map[string]any{"type": "ashby", "company": "OpenAI", "job_board_name": "OpenAI"}},
				"new-graduate":                  "new graduate OR early career",
				"software-engineering_keywords": []any{"software engineer"},
				"senior-level_exclusions":       []any{"senior"},
				"min_score":                     float64(70),
				"notifications":                 map[string]any{"mode": "email", "recipients": []any{"email:jobs", "valid@example.com"}},
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
	recipients := notifications["recipients"].([]string)
	if len(recipients) != 0 {
		t.Fatalf("expected model recipients to defer to server configuration, got %#v", recipients)
	}
	source := payload["sources"].([]any)[0].(map[string]any)
	if source["job_board_name"] != "openai" {
		t.Fatalf("expected canonical OpenAI board name, got %#v", source)
	}
	if _, exists := payload["software-engineering_keywords"]; exists {
		t.Fatalf("expected descriptive alias to be removed: %#v", payload)
	}
}

func TestCommandPlanAcceptsStructuredMetadataValues(t *testing.T) {
	var plan CommandPlan
	err := json.Unmarshal([]byte(`{
		"action":"workflow",
		"job":{"name":"fallback","type":"report.email","duration_ms":1000,"max_attempts":1,"should_fail":false,"metadata":{},"report":{"recipients":[],"subject":"","kind":"custom","schedule":""}},
		"workflow":{"name":"monitor","job_type":"jobs.monitor.new_grad","interval_seconds":3600,"max_attempts":2,"enabled":true,"payload":{},"metadata":{"companies":["OpenAI","SpaceX"],"source":"english"}}
	}`), &plan)
	if err != nil {
		t.Fatalf("unmarshal command plan: %v", err)
	}
	if plan.Workflow.Metadata["companies"] != `["OpenAI","SpaceX"]` || plan.Workflow.Metadata["source"] != "english" {
		t.Fatalf("unexpected normalized metadata: %#v", plan.Workflow.Metadata)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestConnectedPlannerPreservesFlexiblePayload(t *testing.T) {
	planner := NewConnectedPlanner(func(ctx context.Context, instructions, prompt string, schema map[string]any) (string, error) {
		if schema["additionalProperties"] != false || !strings.Contains(instructions, "plan_json") || !strings.Contains(instructions, "job_type") {
			t.Fatal("missing strict envelope or planning schema")
		}
		encoded, _ := json.Marshal(map[string]string{"plan_json": `{"action":"workflow","workflow":{"name":"Monitor","job_type":"jobs.monitor.new_grad","interval_seconds":3600,"enabled":true,"payload":{"sources":[{"type":"ashby","company":"OpenAI","job_board_name":"OpenAI"}]},"metadata":{"custom_key":"retained"}}}`})
		return string(encoded), nil
	})
	plan, err := planner.PlanCommand(t.Context(), "Monitor OpenAI hourly")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Workflow.IntervalSeconds != 3600 || plan.Workflow.Metadata["custom_key"] != "retained" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	source := plan.Workflow.Payload["sources"].([]any)[0].(map[string]any)
	if source["job_board_name"] != "openai" {
		t.Fatalf("payload normalization lost: %#v", source)
	}
}
func TestConnectedPlannerRejectsMalformedEnvelope(t *testing.T) {
	for _, output := range []string{`{}`, `{"plan_json":"not JSON"}`, `{"plan_json":"null"}`, `{"plan_json":"[]"}`} {
		t.Run(output, func(t *testing.T) {
			p := NewConnectedPlanner(func(context.Context, string, string, map[string]any) (string, error) { return output, nil })
			if _, err := p.PlanCommand(t.Context(), "monitor roles"); err == nil {
				t.Fatal("accepted invalid plan")
			}
		})
	}
}
