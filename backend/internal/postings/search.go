package postings

import "strings"

var searchConcepts = []struct {
	Triggers []string
	Terms    []string
}{
	{
		Triggers: []string{"new grad", "new-grad", "new college grad", "early career", "early-career", "early", "entry level", "entry-level", "university", "university grad", "university graduate", "campus"},
		Terms:    []string{"new grad", "new-grad", "new college grad", "early career", "early-career", "early", "entry level", "entry-level", "university", "university grad", "university graduate", "campus"},
	},
	{
		Triggers: []string{"software engineering", "software engineer", "swe"},
		Terms:    []string{"software engineering", "software engineer", "swe"},
	},
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
		if !seenTerm[term] {
			groups = append(groups, []string{term})
			seenTerm[term] = true
		}
	}
	return groups
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
