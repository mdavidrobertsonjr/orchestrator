package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultOpenAIBaseURL = "https://api.openai.com/v1"

type Planner interface {
	Plan(ctx context.Context, prompt string) (*JobPlan, error)
	PlanWorkflow(ctx context.Context, prompt string) (*WorkflowPlan, error)
}

type JobPlan struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	DurationMS  int               `json:"duration_ms"`
	MaxAttempts int               `json:"max_attempts"`
	ShouldFail  bool              `json:"should_fail"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Report      ReportPlan        `json:"report"`
}

type ReportPlan struct {
	Recipients []string `json:"recipients"`
	Subject    string   `json:"subject"`
	Kind       string   `json:"kind"`
	Schedule   string   `json:"schedule"`
}

type WorkflowPlan struct {
	Name            string            `json:"name"`
	JobType         string            `json:"job_type"`
	IntervalSeconds int               `json:"interval_seconds"`
	MaxAttempts     int               `json:"max_attempts"`
	Enabled         bool              `json:"enabled"`
	Payload         map[string]any    `json:"payload"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type OpenAIPlanner struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

func NewOpenAIPlanner(apiKey string, model string) *OpenAIPlanner {
	if model == "" {
		model = "gpt-5.4-nano"
	}

	return &OpenAIPlanner{
		apiKey:  apiKey,
		model:   model,
		baseURL: defaultOpenAIBaseURL,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (p *OpenAIPlanner) Plan(ctx context.Context, prompt string) (*JobPlan, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, errors.New("prompt is required")
	}
	if p.apiKey == "" {
		return nil, errors.New("OPENAI_API_KEY is not configured")
	}

	body := responseRequest{
		Model: p.model,
		Input: []responseInput{
			{
				Role:    "system",
				Content: "Convert the user's request into one orchestrator job. Use only these job types: video.transcode, scrape.url, python.script, ai.inference, data.pipeline, report.email. Pick sensible defaults when unspecified: duration_ms 2000, max_attempts 1, should_fail false. Use report.email for email reporting requests. Keep names short and operational. If the request mentions a recurring schedule, preserve it as human-readable text in report.schedule; this system will still run the job immediately for now.",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
		Text: responseText{
			Format: responseFormat{
				Type:   "json_schema",
				Name:   "job_plan",
				Strict: true,
				Schema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"name", "type", "duration_ms", "max_attempts", "should_fail", "metadata", "report"},
					"properties": map[string]any{
						"name": map[string]any{
							"type":      "string",
							"minLength": 1,
							"maxLength": 80,
						},
						"type": map[string]any{
							"type": "string",
							"enum": []string{"video.transcode", "scrape.url", "python.script", "ai.inference", "data.pipeline", "report.email"},
						},
						"duration_ms": map[string]any{
							"type":    "integer",
							"minimum": 100,
							"maximum": 30000,
						},
						"max_attempts": map[string]any{
							"type":    "integer",
							"minimum": 1,
							"maximum": 10,
						},
						"should_fail": map[string]any{
							"type": "boolean",
						},
						"metadata": map[string]any{
							"type":                 "object",
							"additionalProperties": map[string]any{"type": "string"},
						},
						"report": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"recipients", "subject", "kind", "schedule"},
							"properties": map[string]any{
								"recipients": map[string]any{
									"type":  "array",
									"items": map[string]any{"type": "string"},
								},
								"subject": map[string]any{
									"type": "string",
								},
								"kind": map[string]any{
									"type": "string",
									"enum": []string{"job_summary", "worker_status", "failure_alert", "custom"},
								},
								"schedule": map[string]any{
									"type": "string",
								},
							},
						},
					},
				},
			},
		},
	}

	text, err := p.responsesOutput(ctx, body)
	if err != nil {
		return nil, err
	}

	var plan JobPlan
	if err := json.Unmarshal([]byte(text), &plan); err != nil {
		return nil, err
	}
	normalizePlan(&plan)
	return &plan, nil
}

func (p *OpenAIPlanner) PlanWorkflow(ctx context.Context, prompt string) (*WorkflowPlan, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, errors.New("prompt is required")
	}
	if p.apiKey == "" {
		return nil, errors.New("OPENAI_API_KEY is not configured")
	}

	body := responseRequest{
		Model: p.model,
		Input: []responseInput{
			{
				Role:    "system",
				Content: "Convert the user's request into one recurring orchestrator workflow. Use only these job types: jobs.monitor.new_grad, report.email, video.transcode, scrape.url, data.pipeline. Prefer jobs.monitor.new_grad for requests about monitoring new-grad software engineering roles. For Greenhouse sources, use objects with type greenhouse, company, and board_token. For Lever sources, use objects with type lever, company, and account_name. Use lowercase board_token/account_name values inferred from company names when obvious. Defaults: enabled true, max_attempts 2, interval_seconds 86400 for daily, 3600 for hourly, 900 for frequent/near real-time. For job monitors, include keywords, excluded_keywords, locations, min_score, notification_mode, and sources in payload. Keep names short and operational.",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
		Text: responseText{
			Format: responseFormat{
				Type:   "json_schema",
				Name:   "workflow_plan",
				Strict: true,
				Schema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"name", "job_type", "interval_seconds", "max_attempts", "enabled", "payload", "metadata"},
					"properties": map[string]any{
						"name": map[string]any{
							"type":      "string",
							"minLength": 1,
							"maxLength": 80,
						},
						"job_type": map[string]any{
							"type": "string",
							"enum": []string{"jobs.monitor.new_grad", "report.email", "video.transcode", "scrape.url", "data.pipeline"},
						},
						"interval_seconds": map[string]any{
							"type":    "integer",
							"minimum": 60,
							"maximum": 604800,
						},
						"max_attempts": map[string]any{
							"type":    "integer",
							"minimum": 1,
							"maximum": 10,
						},
						"enabled": map[string]any{
							"type": "boolean",
						},
						"payload": map[string]any{
							"type":                 "object",
							"additionalProperties": true,
						},
						"metadata": map[string]any{
							"type":                 "object",
							"additionalProperties": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}

	text, err := p.responsesOutput(ctx, body)
	if err != nil {
		return nil, err
	}

	var plan WorkflowPlan
	if err := json.Unmarshal([]byte(text), &plan); err != nil {
		return nil, err
	}
	normalizeWorkflowPlan(&plan)
	return &plan, nil
}

func (p *OpenAIPlanner) responsesOutput(ctx context.Context, body responseRequest) (string, error) {
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(body); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.baseURL, "/")+"/responses", &encoded)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("OpenAI request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var parsed responseResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}

	text := parsed.OutputText()
	if text == "" {
		return "", errors.New("OpenAI response did not include output text")
	}
	return text, nil
}

type responseRequest struct {
	Model string          `json:"model"`
	Input []responseInput `json:"input"`
	Text  responseText    `json:"text"`
}

type responseInput struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseText struct {
	Format responseFormat `json:"format"`
}

type responseFormat struct {
	Type   string         `json:"type"`
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type responseResponse struct {
	Output []struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func (r responseResponse) OutputText() string {
	for _, output := range r.Output {
		for _, content := range output.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				return content.Text
			}
		}
	}
	return ""
}

func normalizePlan(plan *JobPlan) {
	plan.Name = strings.TrimSpace(plan.Name)
	plan.Type = strings.TrimSpace(plan.Type)
	if plan.Name == "" {
		plan.Name = plan.Type
	}
	if plan.DurationMS < 100 {
		plan.DurationMS = 2000
	}
	if plan.DurationMS > 30000 {
		plan.DurationMS = 30000
	}
	if plan.MaxAttempts < 1 {
		plan.MaxAttempts = 1
	}
	if plan.MaxAttempts > 10 {
		plan.MaxAttempts = 10
	}
	if plan.Metadata == nil {
		plan.Metadata = map[string]string{}
	}
	plan.Report.Subject = strings.TrimSpace(plan.Report.Subject)
	plan.Report.Kind = strings.TrimSpace(plan.Report.Kind)
	plan.Report.Schedule = strings.TrimSpace(plan.Report.Schedule)
	if plan.Type == "report.email" {
		if plan.Report.Subject == "" {
			plan.Report.Subject = "Orchestrator report"
		}
		if plan.Report.Kind == "" {
			plan.Report.Kind = "job_summary"
		}
		if plan.Report.Schedule == "" {
			plan.Report.Schedule = "immediate"
		}
	}
}

func normalizeWorkflowPlan(plan *WorkflowPlan) {
	plan.Name = strings.TrimSpace(plan.Name)
	plan.JobType = strings.TrimSpace(plan.JobType)
	if plan.Name == "" {
		plan.Name = plan.JobType
	}
	if plan.IntervalSeconds < 60 {
		plan.IntervalSeconds = 86400
	}
	if plan.IntervalSeconds > 604800 {
		plan.IntervalSeconds = 604800
	}
	if plan.MaxAttempts < 1 {
		plan.MaxAttempts = 2
	}
	if plan.MaxAttempts > 10 {
		plan.MaxAttempts = 10
	}
	if plan.Payload == nil {
		plan.Payload = map[string]any{}
	}
	if plan.Metadata == nil {
		plan.Metadata = map[string]string{}
	}
}
