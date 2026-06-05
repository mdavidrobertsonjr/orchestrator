package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
	Type         string      `json:"type"`
	Name         string      `json:"name"`
	Company      string      `json:"company"`
	BoardToken   string      `json:"board_token"`
	AccountName  string      `json:"account_name"`
	JobBoardName string      `json:"job_board_name"`
	Postings     []Candidate `json:"postings"`
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
	if _, ok := sources["greenhouse"]; !ok {
		sources["greenhouse"] = NewGreenhouseSource(nil)
	}
	if _, ok := sources["lever"]; !ok {
		sources["lever"] = NewLeverSource(nil)
	}
	if _, ok := sources["ashby"]; !ok {
		sources["ashby"] = NewAshbySource(nil)
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

type GreenhouseSource struct {
	client  *http.Client
	baseURL string
}

func NewGreenhouseSource(client *http.Client) *GreenhouseSource {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &GreenhouseSource{
		client:  client,
		baseURL: "https://boards-api.greenhouse.io",
	}
}

func (s *GreenhouseSource) Fetch(ctx context.Context, config SourceConfig) ([]Candidate, error) {
	boardToken := strings.TrimSpace(config.BoardToken)
	if boardToken == "" {
		boardToken = strings.TrimSpace(config.Name)
	}
	if boardToken == "" {
		return nil, errors.New("greenhouse source requires board_token")
	}

	endpoint, err := url.JoinPath(strings.TrimRight(s.baseURL, "/"), "v1", "boards", boardToken, "jobs")
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	query := parsed.Query()
	query.Set("content", "true")
	parsed.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("greenhouse returned status %d", resp.StatusCode)
	}

	var body greenhouseJobsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}

	candidates := make([]Candidate, 0, len(body.Jobs))
	for _, job := range body.Jobs {
		location := job.Location.Name
		if location == "" {
			location = firstOfficeLocation(job.Offices)
		}

		metadata := map[string]string{
			"board_token": boardToken,
		}
		if job.InternalJobID != nil {
			metadata["internal_job_id"] = strconv.FormatInt(*job.InternalJobID, 10)
		}
		if job.Department.Name != "" {
			metadata["department"] = job.Department.Name
		}

		candidates = append(candidates, Candidate{
			Company:  firstNonEmpty(config.Company, config.Name, boardToken),
			Title:    job.Title,
			URL:      job.AbsoluteURL,
			Location: location,
			Source:   "greenhouse",
			SourceID: strconv.FormatInt(job.ID, 10),
			Metadata: metadata,
		})
	}

	return candidates, nil
}

type greenhouseJobsResponse struct {
	Jobs []greenhouseJob `json:"jobs"`
}

type greenhouseJob struct {
	ID            int64                     `json:"id"`
	InternalJobID *int64                    `json:"internal_job_id"`
	Title         string                    `json:"title"`
	AbsoluteURL   string                    `json:"absolute_url"`
	Location      greenhouseLocation        `json:"location"`
	Department    greenhouseNamedResource   `json:"department"`
	Offices       []greenhouseNamedResource `json:"offices"`
}

type greenhouseLocation struct {
	Name string `json:"name"`
}

type greenhouseNamedResource struct {
	Name     string `json:"name"`
	Location string `json:"location"`
}

func firstOfficeLocation(offices []greenhouseNamedResource) string {
	for _, office := range offices {
		if strings.TrimSpace(office.Location) != "" {
			return strings.TrimSpace(office.Location)
		}
		if strings.TrimSpace(office.Name) != "" {
			return strings.TrimSpace(office.Name)
		}
	}
	return ""
}

type LeverSource struct {
	client  *http.Client
	baseURL string
}

func NewLeverSource(client *http.Client) *LeverSource {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &LeverSource{
		client:  client,
		baseURL: "https://api.lever.co",
	}
}

func (s *LeverSource) Fetch(ctx context.Context, config SourceConfig) ([]Candidate, error) {
	accountName := strings.TrimSpace(config.AccountName)
	if accountName == "" {
		accountName = strings.TrimSpace(config.Name)
	}
	if accountName == "" {
		return nil, errors.New("lever source requires account_name")
	}

	endpoint, err := url.JoinPath(strings.TrimRight(s.baseURL, "/"), "v0", "postings", accountName)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	query := parsed.Query()
	query.Set("mode", "json")
	parsed.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("lever returned status %d", resp.StatusCode)
	}

	var postings []leverPosting
	if err := json.NewDecoder(resp.Body).Decode(&postings); err != nil {
		return nil, err
	}

	candidates := make([]Candidate, 0, len(postings))
	for _, posting := range postings {
		metadata := map[string]string{
			"account_name": accountName,
		}
		if posting.Categories.Team != "" {
			metadata["team"] = posting.Categories.Team
		}
		if posting.Categories.Department != "" {
			metadata["department"] = posting.Categories.Department
		}
		if posting.Categories.Commitment != "" {
			metadata["commitment"] = posting.Categories.Commitment
		}

		candidates = append(candidates, Candidate{
			Company:  firstNonEmpty(config.Company, config.Name, accountName),
			Title:    posting.Text,
			URL:      firstNonEmpty(posting.HostedURL, posting.ApplyURL),
			Location: posting.Categories.Location,
			Source:   "lever",
			SourceID: posting.ID,
			PostedAt: leverPostedAt(posting.CreatedAt),
			Metadata: metadata,
		})
	}

	return candidates, nil
}

type leverPosting struct {
	ID         string          `json:"id"`
	Text       string          `json:"text"`
	HostedURL  string          `json:"hostedUrl"`
	ApplyURL   string          `json:"applyUrl"`
	Categories leverCategories `json:"categories"`
	CreatedAt  int64           `json:"createdAt"`
}

type leverCategories struct {
	Location   string `json:"location"`
	Team       string `json:"team"`
	Department string `json:"department"`
	Commitment string `json:"commitment"`
}

func leverPostedAt(createdAt int64) *time.Time {
	if createdAt <= 0 {
		return nil
	}
	postedAt := time.UnixMilli(createdAt).UTC()
	return &postedAt
}

type AshbySource struct {
	client  *http.Client
	baseURL string
}

func NewAshbySource(client *http.Client) *AshbySource {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &AshbySource{
		client:  client,
		baseURL: "https://api.ashbyhq.com",
	}
}

func (s *AshbySource) Fetch(ctx context.Context, config SourceConfig) ([]Candidate, error) {
	jobBoardName := strings.TrimSpace(config.JobBoardName)
	if jobBoardName == "" {
		jobBoardName = strings.TrimSpace(config.Name)
	}
	if jobBoardName == "" {
		return nil, errors.New("ashby source requires job_board_name")
	}

	endpoint, err := url.JoinPath(strings.TrimRight(s.baseURL, "/"), "posting-api", "job-board", jobBoardName)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	query := parsed.Query()
	query.Set("includeCompensation", "true")
	parsed.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ashby returned status %d", resp.StatusCode)
	}

	var body ashbyJobsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}

	candidates := make([]Candidate, 0, len(body.Jobs))
	for _, job := range body.Jobs {
		if !job.IsListed {
			continue
		}

		metadata := map[string]string{
			"job_board_name": jobBoardName,
		}
		if job.Department != "" {
			metadata["department"] = job.Department
		}
		if job.Team != "" {
			metadata["team"] = job.Team
		}
		if job.EmploymentType != "" {
			metadata["employment_type"] = job.EmploymentType
		}
		if job.WorkplaceType != "" {
			metadata["workplace_type"] = job.WorkplaceType
		}
		if job.Compensation.CompensationTierSummary != "" {
			metadata["compensation"] = job.Compensation.CompensationTierSummary
		}

		candidates = append(candidates, Candidate{
			Company:  firstNonEmpty(config.Company, config.Name, jobBoardName),
			Title:    job.Title,
			URL:      firstNonEmpty(job.JobURL, job.ApplyURL),
			Location: ashbyLocation(job),
			Source:   "ashby",
			SourceID: firstNonEmpty(job.ID, job.JobURL, job.ApplyURL),
			PostedAt: ashbyPublishedAt(job.PublishedAt),
			Metadata: metadata,
		})
	}

	return candidates, nil
}

type ashbyJobsResponse struct {
	Jobs []ashbyJob `json:"jobs"`
}

type ashbyJob struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	Location       string            `json:"location"`
	Department     string            `json:"department"`
	Team           string            `json:"team"`
	IsListed       bool              `json:"isListed"`
	IsRemote       bool              `json:"isRemote"`
	WorkplaceType  string            `json:"workplaceType"`
	PublishedAt    string            `json:"publishedAt"`
	EmploymentType string            `json:"employmentType"`
	JobURL         string            `json:"jobUrl"`
	ApplyURL       string            `json:"applyUrl"`
	Compensation   ashbyCompensation `json:"compensation"`
}

type ashbyCompensation struct {
	CompensationTierSummary string `json:"compensationTierSummary"`
}

func ashbyLocation(job ashbyJob) string {
	if strings.TrimSpace(job.Location) != "" {
		return strings.TrimSpace(job.Location)
	}
	if job.IsRemote || strings.EqualFold(job.WorkplaceType, "Remote") {
		return "Remote"
	}
	return ""
}

func ashbyPublishedAt(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	publishedAt, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil
	}
	publishedAt = publishedAt.UTC()
	return &publishedAt
}
