// Package documentsearchpostgres answers document searches from the database's
// search index.
package documentsearchpostgres

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/lib/pq"
	"gorm.io/gorm"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/documentsearch"
)

// Index reads document_search_candidates, the one place that knows every document
// type's tables. Its callers name no table of another domain.
type Index struct{ db *gorm.DB }

func NewIndex(db *gorm.DB) (*Index, error) {
	if db == nil {
		return nil, errors.New("document search index database is required")
	}
	return &Index{db: db}, nil
}

const candidatesQuery = `
SELECT
  candidate.document_type,
  candidate.document_id::text AS document_id,
  candidate.source_locale,
  candidate.title,
  candidate.slug
FROM public.document_search_candidates(@text, CAST(@document_types AS text[]), CAST(@locales AS text[]), @limit, @offset, CAST(@visible_ids AS uuid[])) AS candidate
ORDER BY array_position(CAST(@document_types AS text[]), candidate.document_type), candidate.document_id`

// visibleIDs is NULL for a scope of every document.
func visibleIDs(scope documentsearch.Scope) any {
	if scope.All {
		return nil
	}
	return pq.StringArray(stringValues(slices.Collect(maps.Keys(scope.Documents))))
}

type candidateRow struct {
	DocumentType string
	DocumentID   string
	SourceLocale string
	Title        string
	Slug         *string
}

// Candidates returns one page of the documents that may hold the query's text.
func (index *Index) Candidates(ctx context.Context, query documentsearch.IndexQuery) ([]documentsearch.Document, error) {
	var locales any
	if len(query.Locales) != 0 {
		locales = pq.StringArray(stringValues(query.Locales))
	}
	var rows []candidateRow
	err := index.db.WithContext(ctx).Raw(candidatesQuery, map[string]any{
		"text": query.Text, "document_types": pq.StringArray(stringValues(query.DocumentTypes)), "locales": locales,
		"limit": query.Limit, "offset": query.Offset, "visible_ids": visibleIDs(query.Scope),
	}).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	candidates := make([]documentsearch.Document, len(rows))
	for position, row := range rows {
		candidates[position] = documentsearch.Document{
			DocumentType: core.Domain(row.DocumentType), DocumentID: core.DocumentReference(row.DocumentID),
			SourceLocale: core.Locale(row.SourceLocale), Title: row.Title, Slug: row.Slug,
		}
	}
	return candidates, nil
}

func stringValues[Name ~string](names []Name) []string {
	values := make([]string, len(names))
	for position, name := range names {
		values[position] = string(name)
	}
	return values
}
