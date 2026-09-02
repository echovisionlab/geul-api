//go:build integration

package form

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type formProviderFileReuseAuthorizer struct{}

func (formProviderFileReuseAuthorizer) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

type formProviderAuditRecorder struct {
	records []sharedtelemetry.AuditRecord
}

func (recorder *formProviderAuditRecorder) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	recorder.records = append(recorder.records, record)
	return nil
}

func TestFormTranslationPersistenceUsesCurrentSourceSchemaIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
	ctx := context.Background()
	now := time.Unix(1_750_000_000, 0).UTC()
	formID := uuid.NewString()
	seedIntegrationFormTranslationFixture(t, pg.DB, formID, now)

	source, err := LoadTranslationSourceDocument(ctx, pg.DB, formID)
	require.NoError(t, err)
	assert.Equal(t, "Contact form", source.Title)
	assert.JSONEq(t, integrationFormSourceSchema, string(source.ContentJSON))

	job, candidate := formProviderCandidate(t, formID, "ko")
	store, err := contentblock.NewGeneratedStore(formProviderFileReuseAuthorizer{})
	require.NoError(t, err)
	audits := &formProviderAuditRecorder{}
	require.NoError(t, ApplyProviderTranslationCandidateWithDB(
		ctx, pg.DB, store, job, candidate,
		translation.EntryWrite{Now: now.Add(time.Minute)}, audits,
	))

	var saved struct {
		Title       *string
		ContentJSON []byte
		ContentText *string
	}
	require.NoError(t, pg.DB.Table("form_translation").
		Where("entity_id = ? AND locale = ?", formID, "ko").Take(&saved).Error)
	require.NotNil(t, saved.Title)
	assert.Equal(t, "요청 시점 번역", *saved.Title)
	assert.JSONEq(t, integrationFormCanonicalTargetSchema, string(saved.ContentJSON))
	require.NotNil(t, saved.ContentText)
	assert.Equal(t, "문의\n도움\n이메일", *saved.ContentText)
	require.Len(t, audits.records, 1)

	selection, err := ResolvePublicLocalization(ctx, pg.DB, formID, "ko, en;q=0.8")
	require.NoError(t, err)
	assert.Equal(t, "ko", selection.DisplayedLocale)
	assert.False(t, selection.IsFallback)
	assert.Equal(t, []string{"en", "ko"}, selection.AvailableLocales)
	require.NotNil(t, selection.Title)
	assert.Equal(t, "요청 시점 번역", *selection.Title)
	assert.JSONEq(t, integrationFormCanonicalTargetSchema, string(selection.ContentJSON))
}

func TestFormTranslationPublicSelectionUsesConfiguredPolicyIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
	ctx := context.Background()
	now := time.Unix(1_750_001_000, 0).UTC()
	formID := uuid.NewString()
	seedIntegrationFormTranslationFixture(t, pg.DB, formID, now)

	canonicalSource, err := LoadFormCanonicalSourceDocumentState(ctx, pg.DB, formID, "en")
	require.NoError(t, err)
	assert.JSONEq(t, integrationFormSourceSchema, string(canonicalSource.ContentJSON))

	existing, err := ResolvePublicLocalization(ctx, pg.DB, formID, "ko")
	require.NoError(t, err)
	assert.Equal(t, "ko", existing.DisplayedLocale)
	assert.False(t, existing.IsFallback)
	assert.False(t, existing.IsOriginal)
	assert.Equal(t, openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_NONE, existing.FallbackReason)
	assert.Equal(t, []string{"en", "ko"}, existing.AvailableLocales)

	require.NoError(t, pg.DB.Exec(
		"DELETE FROM form_translation WHERE entity_id = ? AND locale = ?", formID, "en",
	).Error)
	_, err = LoadFormCanonicalSourceDocumentState(ctx, pg.DB, formID, "en")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	var missingSourceRows int64
	require.NoError(t, pg.DB.Table("form_translation").
		Where("entity_id = ? AND locale = ?", formID, "en").Count(&missingSourceRows).Error)
	assert.Zero(t, missingSourceRows)
}

func TestPrepareSourceLocaleSwitchCreatesCanonicalEmptyFormRowIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
	ctx := context.Background()
	now := time.Unix(1_750_001_500, 0).UTC()
	formID := uuid.NewString()
	seedIntegrationFormTranslationFixture(t, pg.DB, formID, now)
	require.NoError(t, pg.DB.Exec(
		"DELETE FROM form_translation WHERE entity_id = ? AND locale = 'ko'", formID,
	).Error)

	require.NoError(t, PrepareSourceLocaleSwitch(
		ctx, pg.DB, formID, "en", "ko", now.Add(time.Minute),
	))
	var prepared struct {
		Title       *string
		ContentJSON []byte
		ContentText *string
	}
	require.NoError(t, pg.DB.Table("form_translation").
		Where("entity_id = ? AND locale = 'ko'", formID).
		Take(&prepared).Error)
	assert.Nil(t, prepared.Title)
	assert.Nil(t, prepared.ContentText)
	assert.JSONEq(t, integrationFormEmptyTargetSchema, string(prepared.ContentJSON))

	var source struct {
		Title       *string
		ContentJSON []byte
	}
	require.NoError(t, pg.DB.Table("form_translation").
		Where("entity_id = ? AND locale = 'en'", formID).
		Take(&source).Error)
	require.NotNil(t, source.Title)
	assert.Equal(t, "Contact form", *source.Title)
	assert.JSONEq(t, integrationFormSourceSchema, string(source.ContentJSON))
}

func TestFormProviderUsesCurrentRootTopologyAcrossPointerSwitchIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
	ctx := context.Background()
	now := time.Unix(1_750_002_000, 0).UTC()
	formID := uuid.NewString()
	seedIntegrationFormTranslationFixture(t, pg.DB, formID, now)
	frenchTitle := ""
	require.NoError(t, pg.DB.Exec(
		"INSERT INTO form_translation (entity_id, locale, title, content_json, content_text, created_at, updated_at) VALUES (?, 'fr', ?, CAST(? AS jsonb), 'Français', ?, ?)",
		formID, frenchTitle, integrationFormFrenchSourceSchema, now, now,
	).Error)
	require.NoError(t, pg.DB.Table("form").Where("id = ?", formID).Update("source_locale", "fr").Error)

	rootSource, err := LoadTranslationSourceDocument(ctx, pg.DB, formID)
	require.NoError(t, err)
	assert.Equal(t, frenchTitle, rootSource.Title)
	assert.JSONEq(t, integrationFormFrenchSourceSchema, string(rootSource.ContentJSON))

	job, candidate := formProviderCandidate(t, formID, "ko")
	store, err := contentblock.NewGeneratedStore(formProviderFileReuseAuthorizer{})
	require.NoError(t, err)
	audits := &formProviderAuditRecorder{}
	require.NoError(t, ApplyProviderTranslationCandidateWithDB(
		ctx, pg.DB, store, job, candidate,
		translation.EntryWrite{Now: now.Add(time.Minute)}, audits,
	))
	var stored struct {
		Title       *string
		ContentJSON []byte
	}
	require.NoError(t, pg.DB.Table("form_translation").
		Where("entity_id = ? AND locale = 'ko'", formID).Take(&stored).Error)
	assert.JSONEq(t, integrationFormEmptyFrenchTargetSchema, string(stored.ContentJSON),
		"late provider response must intersect its request manifest with the current root topology")
	require.NotNil(t, stored.Title, "explicit-empty current source title keeps the stable title handle")
	assert.Equal(t, "요청 시점 번역", *stored.Title)
	require.Len(t, audits.records, 1)
	assert.Equal(t, job.RequestedByMemberID, audits.records[0].MemberID)
	assert.Equal(t, "ko", audits.records[0].Locale)
	assert.Equal(t, sharedtelemetry.AuditItemOperationUpdated, audits.records[0].ItemOperation)
}

func TestFormProviderPromotesLateTargetToCurrentSourceMutationIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
	ctx := context.Background()
	now := time.Unix(1_750_003_000, 0).UTC()
	formID := uuid.NewString()
	seedIntegrationFormTranslationFixture(t, pg.DB, formID, now)
	require.NoError(t, pg.DB.Exec(
		"INSERT INTO form_translation (entity_id, locale, title, content_json, content_text, created_at, updated_at) VALUES (?, 'fr', '', CAST(? AS jsonb), 'Français', ?, ?)",
		formID, integrationFormFrenchSourceSchema, now, now,
	).Error)
	require.NoError(t, pg.DB.Table("form").Where("id = ?", formID).Update("source_locale", "fr").Error)

	var beforeRevision string
	require.NoError(t, pg.DB.Raw(
		"SELECT document.revision::text FROM form AS root JOIN content_document AS document ON document.id = root.content_document_id WHERE root.id = ?",
		formID,
	).Scan(&beforeRevision).Error)
	job, candidate := formProviderCandidate(t, formID, "fr")
	store, err := contentblock.NewGeneratedStore(formProviderFileReuseAuthorizer{})
	require.NoError(t, err)
	audits := &formProviderAuditRecorder{}
	require.NoError(t, ApplyProviderTranslationCandidateWithDB(
		ctx, pg.DB, store, job, candidate,
		translation.EntryWrite{Now: now.Add(time.Minute)}, audits,
	))

	var afterRevision string
	require.NoError(t, pg.DB.Raw(
		"SELECT document.revision::text FROM form AS root JOIN content_document AS document ON document.id = root.content_document_id WHERE root.id = ?",
		formID,
	).Scan(&afterRevision).Error)
	assert.NotEqual(t, beforeRevision, afterRevision)
	var source struct {
		Title       *string
		ContentJSON []byte
	}
	require.NoError(t, pg.DB.Table("form_translation").
		Where("entity_id = ? AND locale = 'fr'", formID).Take(&source).Error)
	require.NotNil(t, source.Title)
	assert.Equal(t, "요청 시점 번역", *source.Title)
	assert.JSONEq(t, integrationFormEmptyFrenchTargetSchema, string(source.ContentJSON))
	require.Len(t, audits.records, 1)
	assert.Equal(t, job.RequestedByMemberID, audits.records[0].MemberID)
	assert.Equal(t, "fr", audits.records[0].Locale)
	assert.Equal(t, sharedtelemetry.AuditItemOperationUpdated, audits.records[0].ItemOperation)
}

func TestFormProviderDoesNotReviveAbsentCurrentSourceTitleIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
	ctx := context.Background()
	now := time.Unix(1_750_004_000, 0).UTC()
	formID := uuid.NewString()
	seedIntegrationFormTranslationFixture(t, pg.DB, formID, now)
	require.NoError(t, pg.DB.Table("form_translation").
		Where("entity_id = ? AND locale = 'en'", formID).
		Update("title", nil).Error)

	job, candidate := formProviderCandidate(t, formID, "ko")
	store, err := contentblock.NewGeneratedStore(formProviderFileReuseAuthorizer{})
	require.NoError(t, err)
	audits := &formProviderAuditRecorder{}
	require.NoError(t, ApplyProviderTranslationCandidateWithDB(
		ctx, pg.DB, store, job, candidate,
		translation.EntryWrite{Now: now.Add(time.Minute)}, audits,
	))
	var stored struct{ Title *string }
	require.NoError(t, pg.DB.Table("form_translation").
		Select("title").
		Where("entity_id = ? AND locale = 'ko'", formID).
		Take(&stored).Error)
	assert.Nil(t, stored.Title, "absent current source title removes the stable title handle")
}

const integrationFormSourceSchema = "{\"id\":\"source-schema\",\"steps\":[{\"id\":\"step-1\",\"title\":\"Contact\",\"description\":\"How can we help\",\"fields\":[{\"id\":\"field-email\",\"key\":\"email\",\"type\":\"email\",\"label\":\"Email\",\"required\":true}]}]}"

const integrationFormTargetSchema = "{\"id\":\"target-schema\",\"steps\":[{\"id\":\"step-1\",\"title\":\"문의\",\"description\":\"도움\",\"fields\":[{\"id\":\"field-email\",\"key\":\"changed-key\",\"type\":\"text\",\"label\":\"이메일\",\"required\":false}]},{\"id\":\"target-only\",\"title\":\"추가 단계\",\"fields\":[]}]}"

const integrationFormCanonicalTargetSchema = "{\"id\":\"source-schema\",\"steps\":[{\"id\":\"step-1\",\"title\":\"문의\",\"description\":\"도움\",\"fields\":[{\"id\":\"field-email\",\"key\":\"email\",\"type\":\"email\",\"label\":\"이메일\",\"required\":true}]}]}"

const integrationFormEmptyTargetSchema = "{\"id\":\"source-schema\",\"steps\":[{\"id\":\"step-1\",\"fields\":[{\"id\":\"field-email\",\"key\":\"email\",\"type\":\"email\",\"required\":true}]}]}"

const integrationFormFrenchSourceSchema = "{\"id\":\"french-schema\",\"steps\":[{\"id\":\"step-fr\",\"title\":\"Français\",\"fields\":[]}]}"

const integrationFormEmptyFrenchTargetSchema = "{\"id\":\"french-schema\",\"steps\":[{\"id\":\"step-fr\",\"fields\":[]}]}"

func formProviderCandidate(
	t *testing.T,
	formID string,
	targetLocale string,
) (*model.TranslationJob, *translation.Candidate) {
	t.Helper()
	requestSource := &translation.SourceDocument{
		Title:       "Contact form",
		ContentJSON: []byte(integrationFormSourceSchema),
	}
	plan, err := BuildTranslationExtractionPlan(formID, "en", targetLocale, requestSource)
	require.NoError(t, err)
	results := map[string]translation.UnitResult{
		"entity:title":            {UnitID: "entity:title", TranslatedText: "요청 시점 번역"},
		"step:step-1:title":       {UnitID: "step:step-1:title", TranslatedText: "문의"},
		"step:step-1:description": {UnitID: "step:step-1:description", TranslatedText: "도움"},
		"field:field-email:label": {UnitID: "field:field-email:label", TranslatedText: "이메일"},
	}
	candidate, err := ApplyTranslationCandidate(requestSource, results)
	require.NoError(t, err)
	require.NoError(t, candidate.SetProviderUnitPatch(plan, results))
	return &model.TranslationJob{
		EntityType: "form", EntityID: formID,
		SourceLocale: "en", TargetLocale: targetLocale,
		RequestedByMemberID: uuid.NewString(),
	}, candidate
}

func seedIntegrationFormTranslationFixture(t *testing.T, db *gorm.DB, formID string, now time.Time) {
	t.Helper()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO content_document (id, profile) VALUES (?, 'compact')", documentID).Error)
	require.NoError(t, db.Exec("INSERT INTO form (id, status, is_public, source_locale, content_document_id, created_at, updated_at) VALUES (?, 'FORM_STATUS_DRAFT', FALSE, 'en', ?, ?, ?)", formID, documentID, now, now).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO form_translation (entity_id, locale, title, content_json, content_text, created_at, updated_at) VALUES (?, 'en', 'Contact form', CAST(? AS jsonb), 'Contact\nHow can we help\nEmail', ?, ?)",
		formID, integrationFormSourceSchema, now, now,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO form_translation (entity_id, locale, title, content_json, content_text, created_at, updated_at) VALUES (?, 'ko', '기존 양식', CAST(? AS jsonb), '기존', ?, ?)",
		formID, integrationFormTargetSchema, now, now,
	).Error)
}
