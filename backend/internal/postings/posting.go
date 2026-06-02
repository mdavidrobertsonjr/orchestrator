package postings

import "time"

type Posting struct {
	ID           string            `json:"id"`
	Company      string            `json:"company"`
	Title        string            `json:"title"`
	URL          string            `json:"url"`
	Location     string            `json:"location,omitempty"`
	Source       string            `json:"source"`
	SourceID     string            `json:"source_id,omitempty"`
	DedupeKey    string            `json:"dedupe_key"`
	PostedAt     *time.Time        `json:"posted_at,omitempty"`
	FirstSeenAt  time.Time         `json:"first_seen_at"`
	LastSeenAt   time.Time         `json:"last_seen_at"`
	MatchedAt    *time.Time        `json:"matched_at,omitempty"`
	MatchScore   int               `json:"match_score,omitempty"`
	MatchReasons []string          `json:"match_reasons,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type UpsertPostingParams struct {
	Company      string            `json:"company"`
	Title        string            `json:"title"`
	URL          string            `json:"url"`
	Location     string            `json:"location"`
	Source       string            `json:"source"`
	SourceID     string            `json:"source_id"`
	PostedAt     *time.Time        `json:"posted_at"`
	MatchedAt    *time.Time        `json:"matched_at"`
	MatchScore   int               `json:"match_score"`
	MatchReasons []string          `json:"match_reasons"`
	Metadata     map[string]string `json:"metadata"`
}

