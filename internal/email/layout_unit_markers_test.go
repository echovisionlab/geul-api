package email

import (
	"context"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

// renderRows holds template-1 and layout "legacy", both with source locale
// "en": the rows a render reads. Any other Store call panics on the nil
// Store.
type renderRows struct {
	Store
	subject, contentHTML string
	layoutSource         string
}

// templateRows is template-1 in layout "legacy" with the source subject and
// HTML.
func templateRows(subject, contentHTML, layoutSource string) *renderRows {
	return &renderRows{subject: subject, contentHTML: contentHTML, layoutSource: layoutSource}
}

func (r *renderRows) TemplateByID(_ context.Context, templateID string) (model.EmailTemplate, error) {
	if templateID != "template-1" {
		return model.EmailTemplate{}, ErrTemplateNotFound
	}
	layoutID := "legacy"
	return model.EmailTemplate{ID: templateID, LayoutID: &layoutID}, nil
}

func (r *renderRows) TemplateSourceLocale(context.Context, string) (string, bool, error) {
	return "en", true, nil
}

func (r *renderRows) TemplateContent(_ context.Context, _, locale string) (*TemplateContent, error) {
	return &TemplateContent{Locale: locale, Subject: &r.subject, ContentHTML: &r.contentHTML}, nil
}

func (r *renderRows) LayoutSourceLocale(context.Context, string) (string, bool, error) {
	return "en", true, nil
}

func (r *renderRows) LayoutContent(context.Context, string, string) (*string, error) {
	return &r.layoutSource, nil
}

func TestDurableUnitsRetainPurePlaceholdersAfterLiteralMigration(t *testing.T) {
	source, err := CanonicalizeLayoutSourceMarkers(`<img alt="Brand"><p>Brand</p>{{content}}`)
	require.NoError(t, err)
	units, err := ExtractLayoutContentUnits(source)
	require.NoError(t, err)
	require.Len(t, units, 2)
	transformed := strings.ReplaceAll(source, "Brand", "{{site.title}}")
	canonical, err := CanonicalizeLayoutSourceMarkers(transformed)
	require.NoError(t, err)
	require.Equal(t, transformed, canonical)
	after, err := ExtractLayoutContentUnits(canonical)
	require.NoError(t, err)
	require.Len(t, after, 2)
	for i, unit := range after {
		require.Equal(t, units[i].Handle, unit.Handle)
		require.Equal(t, "{{site.title}}", unit.SourceValue)
	}
}
