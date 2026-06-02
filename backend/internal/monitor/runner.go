package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"orchestrator/backend/internal/postings"
)

const NewGradJobType = "jobs.monitor.new_grad"

type Runner struct {
	store   postings.Store
	sources map[string]Source
}

type Source interface {
	Fetch(ctx context.Context, config SourceConfig) ([]Candidate, error)
}

type SourceConfig struct {
	Type     string      `json:"type"`
	Name     string      `json:"name"`
	Company  string      `json:"company"`
	Postings []Candidate `json:"postings"`
}

type Candidate struct {
	Company  string            `json:"company"`
	Title    string            `json:"title"`
	URL      string            `json:"url"`
	Location string            `json:"location"`
	Source   string            `json:"source"`
	SourceID string            `json:"source_id"`
	PostedAt *time.Time        `json:"posted_at"`
	Metadata map[string]string `json:"metadata"`
}

type Payload struct {
	Sources          []SourceConfig `json:"sources"`
	Companies        []string       `json:"companies"`
	Keywords         []string       `json:"keywords"`
	ExcludedKeywords []string       `json:"excluded_keywords"`
	Locations        []string       `json:"locations"`
	MinScore         int            `json:"min_score"`
	NotificationMode string         `json:"notification_mode"`
}

type Result struct {
	Scanned int
	Matched int
	Created int
	Updated int
}

func NewRunner(store postings.Store, sources map[string]Source) *Runner {
	if sources == nil {
		sources = map[string]Source{}
	}
	if _, ok := sources["fake"]; !ok {
		sources["fake"] = FakeSource{}
	}
	return &Runner{store: store, sources: sources}
}

func (r *Runner) Run(ctx context.Context, rawPayload map[string]any, logf func(string)) (*Result, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("monitor runner is not configured")
	}

	payload, err := parsePayload(rawPayload)
	if err != nil {
		return nil, err
	}
	if len(payload.Sources) == 0 {
		return nil, errors.New("monitor payload requires at least one source")
	}
	if payload.MinScore <= 0 {
		payload.MinScore = 1
	}

	result := &Result{}
	for _, sourceConfig := range payload.Sources {
		sourceType := strings.TrimSpace(sourceConfig.Type)
		if sourceType == "" {
			sourceType = "fake"
		}

		source, ok := r.sources[sourceType]
		if !ok {
			return nil, fmt.Errorf("unsupported monitor source type %q", sourceType)
		}

		candidates, err := source.Fetch(ctx, sourceConfig)
		if err != nil {
			return nil, fmt.Errorf("fetch %s source: %w", sourceType, err)
		}
		log(logf, fmt.Sprintf("monitor source %q returned %d postings", sourceName(sourceConfig), len(candidates)))

		for _, candidate := range candidates {
			result.Scanned++
			score, reasons := scoreCandidate(candidate, payload)
			if score < payload.MinScore {
				continue
			}

			now := time.Now().UTC()
			params := postings.UpsertPostingParams{
				Company:      firstNonEmpty(candidate.Company, sourceConfig.Company),
				Title:        candidate.Title,
				URL:          candidate.URL,
				Location:     candidate.Location,
				Source:       firstNonEmpty(candidate.Source, sourceName(sourceConfig)),
				SourceID:     candidate.SourceID,
				PostedAt:     candidate.PostedAt,
				MatchedAt:    &now,
				MatchScore:   score,
				MatchReasons: reasons,
				Metadata:     candidate.Metadata,
			}

			posting, isNew, err := r.store.Upsert(params)
			if err != nil {
				return nil, fmt.Errorf("store posting %q: %w", candidate.Title, err)
			}

			result.Matched++
			if isNew {
				result.Created++
				log(logf, fmt.Sprintf("new matching posting: %s - %s (%s)", posting.Company, posting.Title, posting.URL))
			} else {
				result.Updated++
				log(logf, fmt.Sprintf("seen matching posting updated: %s - %s", posting.Company, posting.Title))
			}
		}
	}

	log(logf, fmt.Sprintf("monitor completed: scanned=%d matched=%d new=%d updated=%d", result.Scanned, result.Matched, result.Created, result.Updated))
	return result, nil
}

func parsePayload(raw map[string]any) (Payload, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return Payload{}, err
	}

	var payload Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		return Payload{}, err
	}
	return payload, nil
}

func scoreCandidate(candidate Candidate, payload Payload) (int, []string) {
	text := strings.ToLower(strings.Join([]string{
		candidate.Company,
		candidate.Title,
		candidate.Location,
		candidate.URL,
	}, " "))

	for _, excluded := range payload.ExcludedKeywords {
		excluded = strings.ToLower(strings.TrimSpace(excluded))
		if excluded != "" && strings.Contains(text, excluded) {
			return 0, nil
		}
	}

	score := 0
	var reasons []string
	for _, company := range payload.Companies {
		company = strings.ToLower(strings.TrimSpace(company))
		if company != "" && strings.Contains(strings.ToLower(candidate.Company), company) {
			score += 30
			reasons = append(reasons, "company:"+company)
		}
	}
	for _, keyword := range payload.Keywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" && strings.Contains(text, keyword) {
			score += 20
			reasons = append(reasons, "keyword:"+keyword)
		}
	}
	for _, location := range payload.Locations {
		location = strings.ToLower(strings.TrimSpace(location))
		if location != "" && strings.Contains(strings.ToLower(candidate.Location), location) {
			score += 20
			reasons = append(reasons, "location:"+location)
		}
	}

	if score == 0 && len(payload.Companies) == 0 && len(payload.Keywords) == 0 && len(payload.Locations) == 0 {
		score = 1
		reasons = append(reasons, "unfiltered")
	}
	return score, reasons
}

func sourceName(config SourceConfig) string {
	return firstNonEmpty(config.Name, config.Company, config.Type, "unknown")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func log(logf func(string), message string) {
	if logf != nil {
		logf(message)
	}
}

type FakeSource struct{}

func (FakeSource) Fetch(ctx context.Context, config SourceConfig) ([]Candidate, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return append([]Candidate(nil), config.Postings...), nil
}
