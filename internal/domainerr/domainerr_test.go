package domainerr

import "testing"

func TestDuplicateKeepsTheValueInsideItsQuotes(t *testing.T) {
	for value, want := range map[string]string{
		"taken":      `post with slug "taken" already exists`,
		"Editor's":   `post with slug "Editor's" already exists`,
		`say "hi"`:   `post with slug "say \"hi\"" already exists`,
		`back\slash`: `post with slug "back\\slash" already exists`,
		"글":          `post with slug "글" already exists`,
	} {
		if got := Duplicate("post", "slug", value).Message; got != want {
			t.Errorf("Duplicate(%q) = %q, want %q", value, got, want)
		}
	}
}
