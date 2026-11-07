package domainerr

import "testing"

func TestDuplicateKeepsAQuoteInTheValueInsideItsQuotes(t *testing.T) {
	for value, want := range map[string]string{
		"taken":    "post with slug 'taken' already exists",
		"it's' or": `post with slug 'it\'s\' or' already exists`,
	} {
		if got := Duplicate("post", "slug", value).Message; got != want {
			t.Errorf("Duplicate(%q) = %q, want %q", value, got, want)
		}
	}
}
