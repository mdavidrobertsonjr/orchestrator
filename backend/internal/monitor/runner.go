package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"orchestrator/backend/internal/postings"
)

const NewGradJobType = "jobs.monitor.new_grad"

const defaultSourceLimit = 500

const (
	maxMonitorSources = 32
	maxSourceRetries  = 5
	maxBackoffMS      = 30_000
	maxRateLimitMS    = 60_000
	maxSourceResponse = 16 << 20
)

const postingWriteConcurrency = 8

type Runner struct {
	store       postings.Store
	sources     map[string]Source
	cacheMu     sync.Mutex
	sourceCache map[string]cachedSource
}

const sourceCacheTTL = 2 * time.Minute

const sourceCacheMaxEntries = 256

type cachedSource struct {
	fetchedAt  time.Time
	candidates []Candidate
}

type Source interface {
	Fetch(ctx context.Context, config SourceConfig) ([]Candidate, error)
}

type SourceConfig struct {
	Type              string      `json:"type"`
	Name              string      `json:"name"`
	Company           string      `json:"company"`
	URL               string      `json:"url"`
	APIURL            string      `json:"api_url"`
	CareersURL        string      `json:"careers_url"`
	BoardToken        string      `json:"board_token"`
	CompanyIdentifier string      `json:"company_identifier"`
	AccountName       string      `json:"account_name"`
	JobBoardName      string      `json:"job_board_name"`
	Tenant            string      `json:"tenant"`
	Site              string      `json:"site"`
	SearchText        string      `json:"search_text"`
	Limit             int         `json:"limit"`
	RateLimitMS       int         `json:"rate_limit_ms"`
	MaxRetries        int         `json:"max_retries"`
	BackoffMS         int         `json:"backoff_ms"`
	Postings          []Candidate `json:"postings"`
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
	Notifications    Notifications  `json:"notifications"`
	Recipients       []string       `json:"recipients"`
}

type Notifications struct {
	Mode       string   `json:"mode"`
	Recipients []string `json:"recipients"`
}

type Result struct {
	Scanned      int
	Matched      int
	Created      int
	Updated      int
	NewPostings  []*postings.Posting
	SourceErrors []string
	SourceStats  []SourceStat
}

type SourceStat struct {
	Source     string `json:"source"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Candidates int    `json:"candidates"`
	Limited    bool   `json:"limited"`
}

type sourceFetchResult struct {
	config     SourceConfig
	sourceType string
	candidates []Candidate
	err        error
	duration   time.Duration
}

type matchedPosting struct {
	params postings.UpsertPostingParams
	title  string
}

type upsertedPosting struct {
	posting *postings.Posting
	isNew   bool
	err     error
	title   string
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
	if _, ok := sources["workday"]; !ok {
		sources["workday"] = NewWorkdaySource(nil)
	}
	if _, ok := sources["custom"]; !ok {
		sources["custom"] = NewCustomSource(nil)
	}
	if _, ok := sources["smartrecruiters"]; !ok {
		sources["smartrecruiters"] = NewSmartRecruitersSource(nil)
	}
	return &Runner{store: store, sources: sources, sourceCache: make(map[string]cachedSource)}
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

	fetched := make([]sourceFetchResult, len(payload.Sources))
	var fetchWG sync.WaitGroup
	for index, sourceConfig := range payload.Sources {
		sourceType := strings.TrimSpace(sourceConfig.Type)
		if sourceType == "" {
			sourceType = "fake"
		}
		if sourceType == "url" || sourceType == "json" || sourceType == "feed" {
			if identifier := smartRecruitersIdentifier(sourceConfig.URL); identifier != "" {
				sourceType = "smartrecruiters"
				sourceConfig.CompanyIdentifier = identifier
			} else {
				sourceType = "custom"
			}
		}
		source, ok := r.sources[sourceType]
		if !ok {
			return nil, fmt.Errorf("unsupported monitor source type %q", sourceType)
		}
		fetched[index] = sourceFetchResult{config: sourceConfig, sourceType: sourceType}
		fetchWG.Add(1)
		go func(index int, config SourceConfig, sourceType string, source Source) {
			defer fetchWG.Done()
			started := time.Now()
			defer func() { fetched[index].duration = time.Since(started) }()
			if candidates, ok := r.cachedSource(sourceType, config); ok {
				fetched[index].candidates = candidates
				return
			}
			if err := applySourceRateLimit(ctx, config); err != nil {
				fetched[index].err = err
				return
			}
			fetched[index].candidates, fetched[index].err = source.Fetch(ctx, config)
			if fetched[index].err == nil {
				r.cacheSource(sourceType, config, fetched[index].candidates)
			}
		}(index, sourceConfig, sourceType, source)
	}
	fetchWG.Wait()

	result := &Result{}
	successfulSources := 0
	for _, fetchedSource := range fetched {
		sourceConfig := fetchedSource.config
		if fetchedSource.err != nil {
			sourceErr := fmt.Sprintf("%s: %v", sourceName(sourceConfig), fetchedSource.err)
			result.SourceErrors = append(result.SourceErrors, sourceErr)
			result.SourceStats = append(result.SourceStats, SourceStat{Source: sourceName(sourceConfig), Status: "failed", DurationMS: fetchedSource.duration.Milliseconds()})
			log(logf, "monitor source failed: "+sourceErr)
			continue
		}
		successfulSources++
		candidates := fetchedSource.candidates
		limited := false
		if sourceConfig.Limit > 0 && len(candidates) > sourceConfig.Limit {
			limited = true
			log(logf, fmt.Sprintf("monitor source %q limited from %d to %d postings", sourceName(sourceConfig), len(candidates), sourceConfig.Limit))
			candidates = candidates[:sourceConfig.Limit]
		}
		result.SourceStats = append(result.SourceStats, SourceStat{Source: sourceName(sourceConfig), Status: "succeeded", DurationMS: fetchedSource.duration.Milliseconds(), Candidates: len(candidates), Limited: limited})
		log(logf, fmt.Sprintf("monitor source %q returned %d postings", sourceName(sourceConfig), len(candidates)))

		matched := make([]matchedPosting, 0, len(candidates))
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

			matched = append(matched, matchedPosting{params: params, title: candidate.Title})
		}

		upserted := r.upsertPostings(matched)
		for _, item := range upserted {
			if item.err != nil {
				return nil, fmt.Errorf("store posting %q: %w", item.title, item.err)
			}
			result.Matched++
			if item.isNew {
				result.Created++
				result.NewPostings = append(result.NewPostings, item.posting)
				log(logf, fmt.Sprintf("new matching posting: %s - %s (%s)", item.posting.Company, item.posting.Title, item.posting.URL))
			} else {
				result.Updated++
				log(logf, fmt.Sprintf("seen matching posting updated: %s - %s", item.posting.Company, item.posting.Title))
			}
		}
	}

	if successfulSources == 0 {
		return nil, errors.Join(errorStrings(result.SourceErrors)...)
	}
	if len(result.SourceErrors) > 0 {
		log(logf, fmt.Sprintf("monitor completed with %d source errors", len(result.SourceErrors)))
	}
	log(logf, fmt.Sprintf("monitor completed: scanned=%d matched=%d new=%d updated=%d", result.Scanned, result.Matched, result.Created, result.Updated))
	return result, nil
}

func smartRecruitersIdentifier(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.Contains(strings.ToLower(parsed.Hostname()), "smartrecruiters.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || strings.EqualFold(parts[0], "feed") {
		return ""
	}
	return parts[0]
}

func (r *Runner) upsertPostings(items []matchedPosting) []upsertedPosting {
	results := make([]upsertedPosting, len(items))
	sem := make(chan struct{}, postingWriteConcurrency)
	var wg sync.WaitGroup
	for index, item := range items {
		wg.Add(1)
		go func(index int, item matchedPosting) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			posting, isNew, err := r.store.Upsert(item.params)
			results[index] = upsertedPosting{posting: posting, isNew: isNew, err: err, title: item.title}
		}(index, item)
	}
	wg.Wait()
	return results
}

func sourceCacheKey(sourceType string, config SourceConfig) string {
	data, _ := json.Marshal(config)
	return sourceType + ":" + string(data)
}

func (r *Runner) cachedSource(sourceType string, config SourceConfig) ([]Candidate, bool) {
	key := sourceCacheKey(sourceType, config)
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	entry, ok := r.sourceCache[key]
	if !ok || time.Since(entry.fetchedAt) > sourceCacheTTL {
		if ok {
			delete(r.sourceCache, key)
		}
		return nil, false
	}
	return append([]Candidate(nil), entry.candidates...), true
}

func (r *Runner) cacheSource(sourceType string, config SourceConfig, candidates []Candidate) {
	key := sourceCacheKey(sourceType, config)
	r.cacheMu.Lock()
	if _, exists := r.sourceCache[key]; !exists && len(r.sourceCache) >= sourceCacheMaxEntries {
		var oldestKey string
		var oldest time.Time
		for candidateKey, entry := range r.sourceCache {
			if oldestKey == "" || entry.fetchedAt.Before(oldest) {
				oldestKey = candidateKey
				oldest = entry.fetchedAt
			}
		}
		if oldestKey != "" {
			delete(r.sourceCache, oldestKey)
		}
	}
	r.sourceCache[key] = cachedSource{fetchedAt: time.Now(), candidates: append([]Candidate(nil), candidates...)}
	r.cacheMu.Unlock()
}

func errorStrings(messages []string) []error {
	errs := make([]error, 0, len(messages))
	for _, message := range messages {
		errs = append(errs, errors.New(message))
	}
	return errs
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
	if len(payload.Sources) == 0 {
		return Payload{}, errors.New("monitor payload requires at least one source")
	}
	if len(payload.Sources) > maxMonitorSources {
		return Payload{}, fmt.Errorf("monitor payload supports at most %d sources", maxMonitorSources)
	}
	for index := range payload.Sources {
		if payload.Sources[index].Limit <= 0 {
			payload.Sources[index].Limit = defaultSourceLimit
		} else if payload.Sources[index].Limit > defaultSourceLimit {
			payload.Sources[index].Limit = defaultSourceLimit
		}
		if payload.Sources[index].MaxRetries < 0 {
			payload.Sources[index].MaxRetries = 0
		} else if payload.Sources[index].MaxRetries > maxSourceRetries {
			payload.Sources[index].MaxRetries = maxSourceRetries
		}
		if payload.Sources[index].BackoffMS < 0 {
			payload.Sources[index].BackoffMS = 0
		} else if payload.Sources[index].BackoffMS > maxBackoffMS {
			payload.Sources[index].BackoffMS = maxBackoffMS
		}
		if payload.Sources[index].RateLimitMS < 0 {
			payload.Sources[index].RateLimitMS = 0
		} else if payload.Sources[index].RateLimitMS > maxRateLimitMS {
			payload.Sources[index].RateLimitMS = maxRateLimitMS
		}
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
	if hasFilters(payload.Keywords) && !postings.MatchesQuery(strings.Join(payload.Keywords, " "), candidate.Company, candidate.Title, candidate.Location, candidate.URL) {
		return 0, nil
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
			break
		}
	}

	if score == 0 && len(payload.Companies) == 0 && len(payload.Keywords) == 0 && len(payload.Locations) == 0 {
		score = 1
		reasons = append(reasons, "unfiltered")
	}
	return score, reasons
}

func hasFilters(filters []string) bool {
	for _, filter := range filters {
		if strings.TrimSpace(filter) != "" {
			return true
		}
	}
	return false
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

func applySourceRateLimit(ctx context.Context, config SourceConfig) error {
	rateLimitMS := config.RateLimitMS
	if rateLimitMS <= 0 {
		return nil
	}
	if rateLimitMS > maxRateLimitMS {
		rateLimitMS = maxRateLimitMS
	}
	timer := time.NewTimer(time.Duration(rateLimitMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func doRequestWithRetries(ctx context.Context, newRequest func() (*http.Request, error), client *http.Client, config SourceConfig) (*http.Response, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	maxRetries := config.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	} else if maxRetries > maxSourceRetries {
		maxRetries = maxSourceRetries
	}
	backoff := time.Duration(config.BackoffMS) * time.Millisecond
	if backoff <= 0 {
		backoff = 250 * time.Millisecond
	} else if backoff > maxBackoffMS*time.Millisecond {
		backoff = maxBackoffMS * time.Millisecond
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := newRequest()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		if resp != nil {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("returned status %d", resp.StatusCode)
		}
		if err != nil {
			lastErr = err
		}
		if attempt == maxRetries {
			break
		}
		delay := retryDelay(resp, backoff, attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func retryDelay(resp *http.Response, backoff time.Duration, attempt int) time.Duration {
	if resp != nil {
		if value := strings.TrimSpace(resp.Header.Get("Retry-After")); value != "" {
			if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
				return time.Duration(seconds) * time.Second
			}
			if when, err := http.ParseTime(value); err == nil {
				if delay := time.Until(when); delay > 0 {
					return delay
				}
				return 0
			}
		}
	}
	base := backoff * time.Duration(1<<attempt)
	// Spread simultaneous retries across a 75%-125% window.
	return time.Duration(float64(base) * (0.75 + rand.Float64()*0.5))
}

func decodeSourceJSON(resp *http.Response, value any) error {
	return json.NewDecoder(io.LimitReader(resp.Body, maxSourceResponse)).Decode(value)
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
	// Job descriptions are not used by the monitor and can make large boards
	// exceed the hosted outbound response limit.
	query.Set("content", "false")
	parsed.RawQuery = query.Encode()

	resp, err := doRequestWithRetries(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	}, s.client, config)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("greenhouse returned status %d", resp.StatusCode)
	}

	var body greenhouseJobsResponse
	if err := decodeSourceJSON(resp, &body); err != nil {
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

	resp, err := doRequestWithRetries(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	}, s.client, config)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("lever returned status %d", resp.StatusCode)
	}

	var postings []leverPosting
	if err := decodeSourceJSON(resp, &postings); err != nil {
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
	// Compensation data can make large Ashby boards exceed the hosted
	// outbound response limit. Matching does not depend on it, so keep the
	// response focused on the fields needed for discovery and scoring.
	query.Set("includeCompensation", "false")
	parsed.RawQuery = query.Encode()

	resp, err := doRequestWithRetries(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	}, s.client, config)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ashby returned status %d", resp.StatusCode)
	}

	var body ashbyJobsResponse
	if err := decodeSourceJSON(resp, &body); err != nil {
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

type WorkdaySource struct {
	client  *http.Client
	baseURL string
}

func NewWorkdaySource(client *http.Client) *WorkdaySource {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &WorkdaySource{client: client, baseURL: "https://%s.wd5.myworkdayjobs.com"}
}

func (s *WorkdaySource) Fetch(ctx context.Context, config SourceConfig) ([]Candidate, error) {
	endpoint, careersBase, err := s.workdayEndpoint(config)
	if err != nil {
		return nil, err
	}

	limit := config.Limit
	if limit <= 0 || limit > defaultSourceLimit {
		limit = defaultSourceLimit
	}
	requestBody := map[string]any{
		"appliedFacets": map[string]any{},
		"limit":         limit,
		"offset":        0,
		"searchText":    strings.TrimSpace(config.SearchText),
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	resp, err := doRequestWithRetries(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}, s.client, config)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("workday returned status %d", resp.StatusCode)
	}

	var response workdayJobsResponse
	if err := decodeSourceJSON(resp, &response); err != nil {
		return nil, err
	}

	candidates := make([]Candidate, 0, len(response.JobPostings))
	for _, posting := range response.JobPostings {
		id := firstNonEmpty(posting.ExternalPath, firstString(posting.BulletFields), posting.Title)
		metadata := map[string]string{
			"tenant": strings.TrimSpace(config.Tenant),
			"site":   strings.TrimSpace(config.Site),
		}
		if posting.PostedOn != "" {
			metadata["posted_on"] = posting.PostedOn
		}
		if len(posting.BulletFields) > 0 {
			metadata["bullet_fields"] = strings.Join(posting.BulletFields, " | ")
		}

		candidates = append(candidates, Candidate{
			Company:  firstNonEmpty(config.Company, config.Name, config.Tenant),
			Title:    posting.Title,
			URL:      workdayPostingURL(careersBase, posting.ExternalPath),
			Location: firstNonEmpty(posting.LocationsText, strings.Join(posting.Locations, ", ")),
			Source:   "workday",
			SourceID: id,
			PostedAt: parseFlexibleTime(posting.PostedOn),
			Metadata: metadata,
		})
	}
	return candidates, nil
}

func firstString(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *WorkdaySource) workdayEndpoint(config SourceConfig) (string, string, error) {
	if strings.TrimSpace(config.APIURL) != "" {
		apiURL := strings.TrimSpace(config.APIURL)
		careersBase := strings.TrimSpace(config.CareersURL)
		if careersBase == "" {
			parsed, err := url.Parse(apiURL)
			if err == nil {
				careersBase = parsed.Scheme + "://" + parsed.Host
			}
		}
		return apiURL, careersBase, nil
	}

	tenant := strings.TrimSpace(config.Tenant)
	site := strings.TrimSpace(config.Site)
	if tenant == "" || site == "" {
		return "", "", errors.New("workday source requires api_url or tenant and site")
	}
	host := fmt.Sprintf(s.baseURL, tenant)
	endpoint, err := url.JoinPath(host, "wday", "cxs", tenant, site, "jobs")
	if err != nil {
		return "", "", err
	}
	return endpoint, firstNonEmpty(config.CareersURL, host), nil
}

func workdayPostingURL(base string, externalPath string) string {
	externalPath = strings.TrimSpace(externalPath)
	if externalPath == "" {
		return strings.TrimSpace(base)
	}
	if parsed, err := url.Parse(externalPath); err == nil && parsed.IsAbs() {
		return externalPath
	}
	joined, err := url.JoinPath(strings.TrimRight(base, "/"), externalPath)
	if err != nil {
		return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(externalPath, "/")
	}
	return joined
}

type SmartRecruitersSource struct {
	client  *http.Client
	baseURL string
}

func NewSmartRecruitersSource(client *http.Client) *SmartRecruitersSource {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &SmartRecruitersSource{client: client, baseURL: "https://api.smartrecruiters.com"}
}

func (s *SmartRecruitersSource) Fetch(ctx context.Context, config SourceConfig) ([]Candidate, error) {
	identifier := strings.TrimSpace(config.CompanyIdentifier)
	if identifier == "" {
		identifier = strings.TrimSpace(config.Name)
	}
	if identifier == "" {
		return nil, errors.New("smartrecruiters source requires company_identifier")
	}
	limit := config.Limit
	if limit <= 0 || limit > defaultSourceLimit {
		limit = defaultSourceLimit
	}
	endpoint, err := url.JoinPath(strings.TrimRight(s.baseURL, "/"), "v1", "companies", identifier, "postings")
	if err != nil {
		return nil, err
	}

	candidates := make([]Candidate, 0, limit)
	for offset := 0; offset < limit; {
		pageSize := minInt(100, limit-offset)
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		query := parsed.Query()
		query.Set("limit", strconv.Itoa(pageSize))
		query.Set("offset", strconv.Itoa(offset))
		parsed.RawQuery = query.Encode()
		resp, err := doRequestWithRetries(ctx, func() (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		}, s.client, config)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, fmt.Errorf("smartrecruiters returned status %d", resp.StatusCode)
		}
		var body smartRecruitersResponse
		decodeErr := decodeSourceJSON(resp, &body)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		for _, posting := range body.Content {
			candidates = append(candidates, Candidate{
				Company:  firstNonEmpty(config.Company, posting.Company.Name, config.Name, identifier),
				Title:    posting.Name,
				URL:      firstNonEmpty(posting.ApplyURL, posting.JobAdURL, posting.Ref),
				Location: smartRecruitersLocation(posting.Location),
				Source:   "smartrecruiters",
				SourceID: firstNonEmpty(posting.UUID, posting.ID),
				PostedAt: smartRecruitersPostedAt(posting.ReleasedDate),
				Metadata: map[string]string{"company_identifier": identifier},
			})
			if len(candidates) >= limit {
				return candidates[:limit], nil
			}
		}
		if len(body.Content) == 0 {
			break
		}
		offset += len(body.Content)
		if body.TotalFound > 0 && offset >= body.TotalFound {
			break
		}
	}
	return candidates, nil
}

type smartRecruitersResponse struct {
	Content    []smartRecruitersPosting `json:"content"`
	TotalFound int                      `json:"totalFound"`
}

type smartRecruitersPosting struct {
	ID           string                     `json:"id"`
	UUID         string                     `json:"uuid"`
	Name         string                     `json:"name"`
	Ref          string                     `json:"ref"`
	JobAdURL     string                     `json:"jobAdUrl"`
	ApplyURL     string                     `json:"applyUrl"`
	ReleasedDate string                     `json:"releasedDate"`
	Location     smartRecruitersLocationObj `json:"location"`
	Company      smartRecruitersCompany     `json:"company"`
}

type smartRecruitersLocationObj struct {
	City    string `json:"city"`
	Region  string `json:"region"`
	Country string `json:"country"`
	Remote  bool   `json:"remote"`
}

type smartRecruitersCompany struct {
	Name string `json:"name"`
}

func smartRecruitersLocation(location smartRecruitersLocationObj) string {
	parts := []string{location.City, location.Region, location.Country}
	var nonEmpty []string
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			nonEmpty = append(nonEmpty, strings.TrimSpace(part))
		}
	}
	if len(nonEmpty) > 0 {
		return strings.Join(nonEmpty, ", ")
	}
	if location.Remote {
		return "Remote"
	}
	return ""
}

func smartRecruitersPostedAt(raw string) *time.Time {
	posted, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	posted = posted.UTC()
	return &posted
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type workdayJobsResponse struct {
	JobPostings []workdayPosting `json:"jobPostings"`
}

type workdayPosting struct {
	Title         string   `json:"title"`
	ExternalPath  string   `json:"externalPath"`
	LocationsText string   `json:"locationsText"`
	Locations     []string `json:"locations"`
	PostedOn      string   `json:"postedOn"`
	BulletFields  []string `json:"bulletFields"`
}

type CustomSource struct {
	client *http.Client
}

func NewCustomSource(client *http.Client) *CustomSource {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &CustomSource{client: client}
}

func (s *CustomSource) Fetch(ctx context.Context, config SourceConfig) ([]Candidate, error) {
	sourceURL := strings.TrimSpace(firstNonEmpty(config.URL, config.APIURL))
	if sourceURL == "" {
		return nil, errors.New("custom source requires url")
	}

	resp, err := doRequestWithRetries(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	}, s.client, config)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("custom source returned status %d", resp.StatusCode)
	}

	postings, err := decodeCustomPostings(resp)
	if err != nil {
		return nil, err
	}

	candidates := make([]Candidate, 0, len(postings))
	for _, posting := range postings {
		candidates = append(candidates, Candidate{
			Company:  firstNonEmpty(posting.Company, config.Company, config.Name),
			Title:    firstNonEmpty(posting.Title, posting.Name),
			URL:      firstNonEmpty(posting.URL, posting.AbsoluteURL),
			Location: firstNonEmpty(posting.Location, strings.Join(posting.Locations, ", ")),
			Source:   "custom",
			SourceID: firstNonEmpty(posting.SourceID, posting.ID, posting.URL, posting.AbsoluteURL),
			PostedAt: parseFlexibleTime(firstNonEmpty(posting.PostedAt, posting.Date)),
			Metadata: map[string]string{
				"url": sourceURL,
			},
		})
	}
	return candidates, nil
}

func decodeCustomPostings(resp *http.Response) ([]customPosting, error) {
	var raw json.RawMessage
	if err := decodeSourceJSON(resp, &raw); err != nil {
		return nil, err
	}

	var direct []customPosting
	if err := json.Unmarshal(raw, &direct); err == nil && direct != nil {
		return direct, nil
	}

	var wrapped struct {
		Jobs     []customPosting `json:"jobs"`
		Postings []customPosting `json:"postings"`
		Results  []customPosting `json:"results"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	return append(append(wrapped.Jobs, wrapped.Postings...), wrapped.Results...), nil
}

type customPosting struct {
	ID          string   `json:"id"`
	SourceID    string   `json:"source_id"`
	Company     string   `json:"company"`
	Title       string   `json:"title"`
	Name        string   `json:"name"`
	URL         string   `json:"url"`
	AbsoluteURL string   `json:"absolute_url"`
	Location    string   `json:"location"`
	Locations   []string `json:"locations"`
	PostedAt    string   `json:"posted_at"`
	Date        string   `json:"date"`
}

func parseFlexibleTime(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02",
		"Jan 2, 2006",
		"January 2, 2006",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, raw)
		if err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	return nil
}
