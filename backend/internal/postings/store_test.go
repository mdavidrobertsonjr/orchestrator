package postings

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreUpsertCreatesPostingCopies(t *testing.T) {
	store := NewMemoryStore()
	postedAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	matchedAt := time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC)

	posting, isNew, err := store.Upsert(UpsertPostingParams{
		Company:      "Datadog",
		Title:        "Software Engineer, New Grad",
		URL:          "https://example.com/jobs/1?utm_source=test",
		Location:     "New York, NY",
		Source:       "greenhouse",
		SourceID:     "job-1",
		PostedAt:     &postedAt,
		MatchedAt:    &matchedAt,
		MatchScore:   92,
		MatchReasons: []string{"nyc", "new grad"},
		Metadata:     map[string]string{"team": "infra"},
	})
	if err != nil {
		t.Fatalf("upsert posting: %v", err)
	}
	if !isNew {
		t.Fatal("expected first upsert to create a posting")
	}
	if posting.ID == "" {
		t.Fatal("expected generated posting ID")
	}
	if posting.DedupeKey != "greenhouse:job-1" {
		t.Fatalf("unexpected dedupe key %q", posting.DedupeKey)
	}

	posting.MatchReasons[0] = "mutated"
	posting.Metadata["team"] = "mutated"
	*posting.PostedAt = posting.PostedAt.Add(24 * time.Hour)

	got, err := store.Get(posting.ID)
	if err != nil {
		t.Fatalf("get posting: %v", err)
	}
	if got.MatchReasons[0] != "nyc" {
		t.Fatalf("store match reasons mutated through returned posting: %#v", got.MatchReasons)
	}
	if got.Metadata["team"] != "infra" {
		t.Fatalf("store metadata mutated through returned posting: %#v", got.Metadata)
	}
	if !got.PostedAt.Equal(postedAt) {
		t.Fatalf("store posted timestamp mutated through returned posting: %s", got.PostedAt)
	}
}

func TestMemoryStoreUpsertDedupesExistingPosting(t *testing.T) {
	store := NewMemoryStore()

	created, isNew, err := store.Upsert(UpsertPostingParams{
		Company:  "Ramp",
		Title:    "Software Engineer - New Grad",
		URL:      "https://jobs.example.com/ramp/new-grad?utm_campaign=x",
		Location: "New York",
		Source:   "lever",
	})
	if err != nil {
		t.Fatalf("create posting: %v", err)
	}
	if !isNew {
		t.Fatal("expected first upsert to be new")
	}

	updated, isNew, err := store.Upsert(UpsertPostingParams{
		Company:    "Ramp",
		Title:      "Software Engineer - New Grad",
		URL:        "https://jobs.example.com/ramp/new-grad?utm_source=y",
		Location:   "NYC",
		Source:     "lever",
		MatchScore: 88,
	})
	if err != nil {
		t.Fatalf("update posting: %v", err)
	}
	if isNew {
		t.Fatal("expected second upsert to update existing posting")
	}
	if updated.ID != created.ID {
		t.Fatalf("expected same posting ID, got %q and %q", updated.ID, created.ID)
	}
	if updated.Location != "NYC" {
		t.Fatalf("expected updated location, got %q", updated.Location)
	}
	if !updated.LastSeenAt.After(created.LastSeenAt) && !updated.LastSeenAt.Equal(created.LastSeenAt) {
		t.Fatalf("expected last seen to move forward, before=%s after=%s", created.LastSeenAt, updated.LastSeenAt)
	}

	postings, err := store.List()
	if err != nil {
		t.Fatalf("list postings: %v", err)
	}
	if len(postings) != 1 {
		t.Fatalf("expected one deduped posting, got %d", len(postings))
	}
}

func TestMemoryStoreListSortsByScoreThenRecency(t *testing.T) {
	store := NewMemoryStore()

	if _, _, err := store.Upsert(UpsertPostingParams{Company: "Old High", Title: "Software Engineer, New Grad", URL: "https://example.com/old-high", Source: "test", MatchScore: 80}); err != nil {
		t.Fatalf("upsert old high: %v", err)
	}
	time.Sleep(time.Millisecond)
	if _, _, err := store.Upsert(UpsertPostingParams{Company: "Low", Title: "Software Engineer, New Grad", URL: "https://example.com/low", Source: "test", MatchScore: 40}); err != nil {
		t.Fatalf("upsert low: %v", err)
	}
	time.Sleep(time.Millisecond)
	if _, _, err := store.Upsert(UpsertPostingParams{Company: "New High", Title: "Software Engineer, New Grad", URL: "https://example.com/new-high", Source: "test", MatchScore: 80}); err != nil {
		t.Fatalf("upsert new high: %v", err)
	}

	postings, err := store.List()
	if err != nil {
		t.Fatalf("list postings: %v", err)
	}
	if len(postings) != 3 {
		t.Fatalf("expected three postings, got %d", len(postings))
	}
	if postings[0].Company != "New High" || postings[1].Company != "Old High" || postings[2].Company != "Low" {
		t.Fatalf("unexpected ordering: %#v", postings)
	}
}

func TestQueryGroupsGroupsKnownConcepts(t *testing.T) {
	groups := QueryGroups("new grad early software engineering")
	if len(groups) != 2 {
		t.Fatalf("expected career-stage and software-engineering groups, got %#v", groups)
	}
	if !hasTerm(groups[0], "new grad") || !hasTerm(groups[0], "early career") || !hasTerm(groups[0], "entry level") {
		t.Fatalf("expected career-stage synonyms in first group, got %#v", groups)
	}
	if !hasTerm(groups[1], "software engineering") || !hasTerm(groups[1], "software engineer") || !hasTerm(groups[1], "swe") {
		t.Fatalf("expected software-engineering synonyms in second group, got %#v", groups)
	}
}

func TestMatchesQueryUsesOrWithinConceptsAndAndAcrossConcepts(t *testing.T) {
	if !MatchesQuery("new grad early software engineering", "Early Career Software Engineer", "New York") {
		t.Fatal("expected query to match early-career software engineering posting")
	}
	if !MatchesQuery("new grad early software engineering", "Software Engineer, New Grad", "New York") {
		t.Fatal("expected query to match new-grad software engineering posting")
	}
	if MatchesQuery("new grad early software engineering", "Early Career Product Manager", "New York") {
		t.Fatal("expected query to reject posting text missing software engineering concept")
	}
}

func TestMemoryStoreNotFound(t *testing.T) {
	store := NewMemoryStore()

	_, err := store.Get("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func hasTerm(terms []string, want string) bool {
	for _, term := range terms {
		if term == want {
			return true
		}
	}
	return false
}
