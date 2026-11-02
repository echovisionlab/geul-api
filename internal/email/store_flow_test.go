package email_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/email/emailtest"
	"github.com/echovisionlab/geul-api/internal/translation"
)

func seedTemplateContent(store *emailtest.Store, templateID, locale, subject, html string) {
	store.SeedTemplateContent(templateID, email.TemplateContent{Locale: locale, Subject: &subject, ContentHTML: &html})
}

func TestRenderTemplateForLocaleOverlaysTheTemplateAndLayoutLocale(t *testing.T) {
	t.Parallel()
	store := emailtest.NewStore()
	layoutID := uuid.NewString()
	store.SeedLayout(layoutID, "en")
	sourceLayout, err := email.CanonicalizeLayoutSourceMarkers("<main>{{content}}</main><footer>Layout footer</footer>")
	require.NoError(t, err)
	units, err := email.ExtractLayoutContentUnits(sourceLayout)
	require.NoError(t, err)
	footerHandle := ""
	for _, unit := range units {
		if unit.SourceValue == "Layout footer" {
			footerHandle = unit.Handle
			break
		}
	}
	require.NotEmpty(t, footerHandle)
	targetLayout, _, err := email.ApplyLayoutLocaleValues(sourceLayout, map[string]string{footerHandle: "레이아웃"})
	require.NoError(t, err)
	require.NoError(t, store.UpsertLayoutEntry(t.Context(), layoutID, "en", &sourceLayout, nil, time.Now()))
	require.NoError(t, store.UpsertLayoutEntry(t.Context(), layoutID, "ko", targetLayout, nil, time.Now()))

	templateID := uuid.NewString()
	store.SeedTemplate(email.Template{ID: templateID, IsActive: true, SourceLocale: "en", LayoutID: &layoutID})
	seedTemplateContent(store, templateID, "en", "Welcome {{recipient_email}}", "<p>Hello {{recipient_email}}</p>")
	seedTemplateContent(store, templateID, "ko", "환영합니다 {{recipient_email}}", "<p>안녕하세요 {{recipient_email}}</p>")

	rendered, err := email.RenderTemplateForLocale(
		t.Context(),
		store,
		email.DirectTemplateType(templateID),
		"ko",
		map[string]string{"recipient_email": "johndoe@example.com"},
	)
	require.NoError(t, err)
	require.NotNil(t, rendered)
	assert.Equal(t, "환영합니다 johndoe@example.com", rendered.Subject)
	assert.Contains(t, rendered.HTML, "레이아웃")
	assert.Contains(t, rendered.HTML, "안녕하세요 johndoe@example.com")
	assert.Equal(t, "ko", rendered.TemplateLocale)
	assert.Equal(t, "ko", rendered.LayoutLocale)
}

func TestLayoutTranslationKeepsTargetsThroughSourceEdits(t *testing.T) {
	t.Parallel()
	store := emailtest.NewStore()
	ctx := t.Context()
	layoutID := uuid.NewString()
	store.SeedLayout(layoutID, "en")
	now := time.Date(2026, time.August, 20, 14, 0, 0, 0, time.UTC)
	sourceHTML := `<main>{{content}}</main><footer>Source footer</footer>`
	require.NoError(t, email.SaveLayoutSourceLocaleDocument(
		ctx,
		store,
		layoutID,
		"en",
		email.LayoutTranslationDocument{ContentHTML: &sourceHTML},
		now,
	))

	resolvedLocale, found, err := email.ResolveLayoutTranslationSourceLocale(ctx, store, layoutID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "en", resolvedLocale)
	source, err := email.LoadCanonicalLayoutTranslationDocument(ctx, store, layoutID, "en")
	require.NoError(t, err)
	require.NotNil(t, source.ContentText)
	assert.Contains(t, *source.ContentText, "Source footer")

	require.NoError(t, email.SeedLayoutTranslationEntryFromSource(
		ctx, store, layoutID, "ko", "en", now.Add(time.Minute),
	))
	seeded, err := email.LoadLayoutTranslationEntry(ctx, store, layoutID, "ko")
	require.NoError(t, err)
	require.NotNil(t, seeded)
	require.NotNil(t, seeded.ContentHTML)
	assert.Contains(t, *seeded.ContentHTML, "Source footer")
	require.NotNil(t, seeded.ContentText)
	assert.Contains(t, *seeded.ContentText, "Source footer")

	require.NotNil(t, source.ContentHTML)
	units, err := email.ExtractLayoutContentUnits(*source.ContentHTML)
	require.NoError(t, err)
	var footerHandle string
	for _, unit := range units {
		if unit.SourceValue == "Source footer" {
			footerHandle = unit.Handle
			break
		}
	}
	require.NotEmpty(t, footerHandle)
	targetHTML, targetText, err := email.ApplyLayoutLocaleValues(*source.ContentHTML, map[string]string{
		footerHandle: "Target footer",
	})
	require.NoError(t, err)
	require.NoError(t, email.UpsertLayoutTranslationEntry(ctx, store, layoutID, "ko", translation.EntryWrite{
		ContentHTML: targetHTML, ContentText: targetText,
		Now: now.Add(2 * time.Minute),
	}))

	recaptured, err := email.LoadLayoutTranslationEntry(ctx, store, layoutID, "ko")
	require.NoError(t, err)
	require.NotNil(t, recaptured)
	require.NotNil(t, recaptured.ContentHTML)
	assert.Equal(t, *targetHTML, *recaptured.ContentHTML)
	require.NotNil(t, recaptured.ContentText)
	assert.Equal(t, *targetText, *recaptured.ContentText)

	editedSourceHTML, _, err := email.ApplyLayoutSourceValues(*source.ContentHTML, map[string]string{
		footerHandle: "Edited source footer",
	})
	require.NoError(t, err)
	require.NoError(t, email.SaveLayoutSourceLocaleDocument(
		ctx, store, layoutID, "en",
		email.LayoutTranslationDocument{ContentHTML: editedSourceHTML},
		now.Add(3*time.Minute),
	))
	recapturedAfterSourceEdit, err := email.LoadLayoutTranslationEntry(ctx, store, layoutID, "ko")
	require.NoError(t, err)
	require.Equal(t, recaptured.ContentHTML, recapturedAfterSourceEdit.ContentHTML)
	localeSnapshots, err := email.LoadLayoutLocaleSnapshots(ctx, store, layoutID)
	require.NoError(t, err)
	require.Len(t, localeSnapshots, 2)
	assert.Equal(t, "en", localeSnapshots[0].Locale)
	assert.True(t, localeSnapshots[0].IsSourceLocale)
	assert.Equal(t, "ko", localeSnapshots[1].Locale)
	assert.False(t, localeSnapshots[1].IsSourceLocale)
	require.NotNil(t, localeSnapshots[1].ContentHTML)
	assert.Contains(t, *localeSnapshots[1].ContentHTML, "Target footer")
	assert.NotContains(t, *localeSnapshots[1].ContentHTML, "geul-unit:")

	providerSource, err := email.LoadLayoutTranslationSourceDocument(ctx, store, layoutID, "en")
	require.NoError(t, err)
	require.NotNil(t, providerSource.ContentHTML)
	assert.Contains(t, *providerSource.ContentHTML, "Edited source footer")
}
