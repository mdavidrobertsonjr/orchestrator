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
}

type JobPlan struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	DurationMS  int               `json:"duration_ms"`
	MaxAttempts int               `json:"max_attempts"`
	ShouldFail  bool              `json:"should_fail"`
	Metadata    map[string]string `json:"metadata,omitempty"`
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
				Content: "Convert the user's request into one orchestrator job. Use only these job types: video.transcode, scrape.url, python.script, ai.inference, data.pipeline. Pick sensible defaults when unspecified: duration_ms 2000, max_attempts 1, should_fail false. Keep names short and operational.",
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
					"required":             []string{"name", "type", "duration_ms", "max_attempts", "should_fail", "metadata"},
					"properties": map[string]any{
						"name": map[string]any{
							"type":      "string",
							"minLength": 1,
							"maxLength": 80,
						},
						"type": map[string]any{
							"type": "string",
							"enum": []string{"video.transcode", "scrape.url", "python.script", "ai.inference", "data.pipeline"},
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
					},
				},
			},
		},
	}

	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(body); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.baseURL, "/")+"/responses", &encoded)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OpenAI request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var parsed responseResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}

	text := parsed.OutputText()
	if text == "" {
		return nil, errors.New("OpenAI response did not include output text")
	}

	var plan JobPlan
	if err := json.Unmarshal([]byte(text), &plan); err != nil {
		return nil, err
	}
	normalizePlan(&plan)
	return &plan, nil
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
}
