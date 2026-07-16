package postings

import "strings"

var searchConcepts = []struct {
	Triggers []string
	Terms    []string
}{
	{
		Triggers: []string{"new graduate", "new college grad", "new grad", "new-grad", "early career", "early-career", "early", "entry level", "entry-level", "university graduate", "university grad", "university", "campus"},
		Terms:    []string{"new grad", "new-grad", "new graduate", "new college grad", "early career", "early-career", "early", "entry level", "entry-level", "university", "university grad", "university graduate", "campus"},
	},
	{
		Triggers: []string{"software engineering", "software engineer", "swe"},
		Terms:    []string{"software engineering", "software engineer", "engineer, software", "swe"},
	},
}

var searchStopWords = map[string]bool{
	"a": true, "an": true, "at": true, "find": true, "for": true, "from": true,
	"in": true, "job": true, "jobs": true, "me": true, "of": true,
	"looking": true,
	"opening": true, "openings": true, "please": true, "posting": true,
	"postings": true, "role": true, "roles": true, "search": true,
	"show": true, "the": true, "to": true,
}

func QueryGroups(query string) [][]string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}

	var groups [][]string
	seenTerm := map[string]bool{}
	for _, concept := range searchConcepts {
		for _, trigger := range concept.Triggers {
			if strings.Contains(query, trigger) {
				groups = append(groups, concept.Terms)
				for _, term := range concept.Terms {
					seenTerm[term] = true
				}
				for _, matched := range concept.Triggers {
					query = strings.ReplaceAll(query, matched, " ")
				}
				break
			}
		}
	}

	for _, term := range strings.Fields(query) {
		term = strings.Trim(term, " .!?\t\r\n")
		if term != "" && !searchStopWords[term] && !seenTerm[term] {
			groups = append(groups, []string{term})
			seenTerm[term] = true
		}
	}
	return groups
}

func FilterTerms(value string) []string {
	parts := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return r == ',' || r == '\n'
	})
	terms := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		term := strings.TrimSpace(part)
		if term == "" || seen[term] {
			continue
		}
		terms = append(terms, term)
		seen[term] = true
	}
	return terms
}

func MatchesQuery(query string, fields ...string) bool {
	groups := QueryGroups(query)
	if len(groups) == 0 {
		return true
	}
	haystack := strings.ToLower(strings.Join(fields, " "))
	for _, group := range groups {
		if !matchesAnyTerm(haystack, group) {
			return false
		}
	}
	return true
}

func matchesAnyTerm(haystack string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(haystack, term) {
			return true
		}
	}
	return false
}

func MatchesAnyFilterTerm(value string, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	value = strings.ToLower(value)
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}
