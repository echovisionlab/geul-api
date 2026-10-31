package versionrestore

import (
	"slices"
	"testing"
)

func TestOutcomeNamesChangesInPublishOrder(t *testing.T) {
	cases := []struct {
		name        string
		outcome     Outcome
		fields      []string
		refreshesOG bool
	}{
		{name: "unchanged", fields: []string{}},
		{name: "content only", outcome: Outcome{ContentChanged: true}, fields: []string{"content"}},
		{name: "summary only", outcome: Outcome{SummaryChanged: true}, fields: []string{"summary"}},
		{name: "title only", outcome: Outcome{TitleChanged: true}, fields: []string{"title"}, refreshesOG: true},
		{
			name:    "everything",
			outcome: Outcome{SourceLocaleSwitched: true, TitleChanged: true, SummaryChanged: true, ContentChanged: true},
			fields:  []string{"title", "summary", "content", "sourceLocale"}, refreshesOG: true,
		},
		{name: "source locale", outcome: Outcome{SourceLocaleSwitched: true}, fields: []string{"sourceLocale"}, refreshesOG: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.outcome.Fields(); !slices.Equal(got, tc.fields) {
				t.Fatalf("Fields() = %v, want %v", got, tc.fields)
			}
			if got := tc.outcome.Changed(); got != (len(tc.fields) != 0) {
				t.Fatalf("Changed() = %v", got)
			}
			if got := tc.outcome.RefreshesOG(); got != tc.refreshesOG {
				t.Fatalf("RefreshesOG() = %v, want %v", got, tc.refreshesOG)
			}
		})
	}
}
