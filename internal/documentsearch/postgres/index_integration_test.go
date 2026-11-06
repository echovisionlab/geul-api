//go:build integration

package documentsearchpostgres_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/documentsearch"
	documentsearchpostgres "github.com/echovisionlab/geul-api/internal/documentsearch/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

// seededPage is one Page stored as the domains store it: its text in a block's
// locale row, its title and summary in the translation table.
type seededPage struct {
	id   string
	slug string
}

func seedPage(t *testing.T, db *gorm.DB, title string, blockText map[string]any, translations map[string]string) seededPage {
	t.Helper()
	page := seededPage{id: uuid.NewString(), slug: "search-" + strings.ReplaceAll(uuid.NewString(), "-", "")}
	documentID, blockID := uuid.NewString(), uuid.NewString()
	localized, err := json.Marshal(blockText)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'page')`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO page (id, slug, content_document_id, source_locale) VALUES (?::uuid, ?, ?::uuid, 'en')`, page.id, page.slug, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO page_translation (entity_id, locale, title, summary) VALUES (?::uuid, 'en', ?, 'A short summary')`, page.id, title).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id, container_slot, "position", kind) VALUES (?::uuid, ?::uuid, 'body', 0, 'paragraph')`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block_locale (block_id, locale, localized_data) VALUES (?::uuid, 'en', ?::jsonb)`, blockID, string(localized)).Error)
	for locale, title := range translations {
		require.NoError(t, db.Exec(`INSERT INTO page_translation (entity_id, locale, title) VALUES (?::uuid, ?, ?)`, page.id, locale, title).Error)
	}
	return page
}

func richText(runs ...map[string]any) map[string]any {
	return map[string]any{"content": runs}
}

func searchIndex(t *testing.T) (*documentsearchpostgres.Index, *gorm.DB) {
	t.Helper()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	index, err := documentsearchpostgres.NewIndex(db)
	require.NoError(t, err)
	return index, db
}

func TestIndexFindsWhatADocumentReadFindsIntegration(t *testing.T) {
	t.Parallel()
	index, db := searchIndex(t)
	page := seedPage(t, db, "Findable Heading",
		richText(
			map[string]any{"text": "Sign up today, Hel"},
			map[string]any{"text": "lo World", "marks": []any{map[string]any{"type": "bold"}}},
			map[string]any{"text": " 지원 문의", "marks": []any{map[string]any{"attrs": map[string]any{"href": "https://old.example.test/docs"}}}},
		),
		map[string]string{"ko": "번역된 제목"},
	)
	finds := func(text string, locales ...core.Locale) bool {
		t.Helper()
		candidates, err := index.Candidates(t.Context(), documentsearch.IndexQuery{Text: text, DocumentTypes: []core.Domain{core.DomainPage}, Locales: locales, Limit: 100})
		require.NoError(t, err, text)
		for _, candidate := range candidates {
			if string(candidate.DocumentID) == page.id {
				require.Equal(t, "Findable Heading", candidate.Title)
				require.Equal(t, page.slug, *candidate.Slug)
				require.EqualValues(t, "en", candidate.SourceLocale)
				return true
			}
		}
		return false
	}
	for name, text := range map[string]string{
		"a word":                   "Sign",
		"another case":             "SIGN UP",
		"part of a word":           "orl",
		"text split by formatting": "Hello World",
		"spacing that differs":     "Sign    up",
		"a link address":           "old.example.test/docs",
		"korean":                   "지원",
		"the title":                "Findable Heading",
		"the summary":              "short summary",
		"the slug":                 page.slug,
	} {
		require.True(t, finds(text), "%s: %q", name, text)
	}
	require.False(t, finds("absentword"))
	require.False(t, finds("번역된"), "a translation is not searched without its locale")
	require.True(t, finds("번역된", "ko"), "a translation is found in its locale")
	require.False(t, finds("Sign", "ko"), "the source is not searched in another locale")
}

func TestIndexKeepsToTheRequestedTypesAndPagesIntegration(t *testing.T) {
	t.Parallel()
	index, db := searchIndex(t)
	word := "pagingword" + strings.ReplaceAll(uuid.NewString(), "-", "")
	for range 3 {
		seedPage(t, db, "Paged", richText(map[string]any{"text": word}), nil)
	}

	none, err := index.Candidates(t.Context(), documentsearch.IndexQuery{Text: word, DocumentTypes: []core.Domain{core.DomainPost}, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, none)

	first, err := index.Candidates(t.Context(), documentsearch.IndexQuery{Text: word, DocumentTypes: []core.Domain{core.DomainPage}, Limit: 2})
	require.NoError(t, err)
	require.Len(t, first, 2)
	rest, err := index.Candidates(t.Context(), documentsearch.IndexQuery{Text: word, DocumentTypes: []core.Domain{core.DomainPage}, Limit: 2, Offset: 2})
	require.NoError(t, err)
	require.Len(t, rest, 1)
	require.NotContains(t, []core.DocumentReference{first[0].DocumentID, first[1].DocumentID}, rest[0].DocumentID)
}

// The search only scales if the query's expressions are the indexed ones.
func TestIndexedExpressionsAreTheOnesSearchedIntegration(t *testing.T) {
	t.Parallel()
	_, db := searchIndex(t)
	for name, statement := range map[string]string{
		"idx_content_block_locale_search": `SELECT block_id FROM content_block_locale WHERE public.content_block_search_text(localized_data) &@ 'Sign'`,
		"idx_page_translation_search":     `SELECT document_id FROM public.document_search_translation WHERE document_type = 'page' AND search_text &@ 'Sign'`,
		"idx_page_search":                 `SELECT document_id FROM public.document_search_root WHERE document_type = 'page' AND search_text &@ 'Sign'`,
	} {
		var plan []string
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(`SET LOCAL enable_seqscan = off`).Error; err != nil {
				return err
			}
			return tx.Raw(`EXPLAIN ` + statement).Scan(&plan).Error
		}), name)
		require.Contains(t, strings.Join(plan, "\n"), name)
	}
}
