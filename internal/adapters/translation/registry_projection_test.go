package translationadapter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTranslationEntrySelectSQLUsesTypedBlockAuthority(t *testing.T) {
	t.Parallel()

	registry := completeDomainRegistryForTest(t)
	for _, entityType := range []string{"post", "program_event", "work"} {
		entityType := entityType
		t.Run(entityType, func(t *testing.T) {
			t.Parallel()
			query, err := registry.TranslationEntrySelectSQL(entityType, entityType+"_translation")
			require.NoError(t, err)
			require.Contains(t, query, "NULL::text AS content_html")
			require.Contains(t, query, "NULL::text AS content_text")
		})
	}
}

func TestTranslationEntrySelectSQLProjectsWorkLocaleTitle(t *testing.T) {
	t.Parallel()

	query, err := completeDomainRegistryForTest(t).TranslationEntrySelectSQL("work", "work_translation")
	require.NoError(t, err)
	require.Contains(t, query, "SELECT locale, title, summary")
	require.NotContains(t, query, "NULL::text AS title")
}

func TestTranslationEntrySelectSQLProjectsLegalEntriesWithoutSummaryColumn(t *testing.T) {
	t.Parallel()

	registry := completeDomainRegistryForTest(t)
	for _, entityType := range []string{"privacy", "terms"} {
		entityType := entityType
		t.Run(entityType, func(t *testing.T) {
			t.Parallel()
			query, err := registry.TranslationEntrySelectSQL(entityType, entityType+"_translation")
			require.NoError(t, err)
			require.Contains(t, query, "SELECT locale, title, NULL::text AS summary, content_html")
			require.NotContains(t, query, "SELECT locale, title, summary")
		})
	}
}

func completeDomainRegistryForTest(t *testing.T) *DomainRegistry {
	t.Helper()
	ports, err := buildDomainPorts(defaultDomainRegistrations(nil, nil))
	require.NoError(t, err)
	return &DomainRegistry{ports: ports}
}
