//go:build integration

package form_test

import (
	"testing"
	"time"

	formdomain "github.com/echovisionlab/geul-api/internal/form"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFormTranslationInterchangePersistsSparseTargetCASAuditAndRollbackIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	now := time.Now().UTC()
	formID := seedGuardedFormLocales(t, db, now)
	require.NoError(t, db.Exec(
		"DELETE FROM form_translation WHERE entity_id = ?::uuid AND locale = 'ko'", formID,
	).Error)
	identityID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Form XLIFF admin")
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	service := newAuditedInternalFormServiceForIntegration(
		db, nil, apitelemetry.NewDurableWriter(db), integrationSpiceDB(t),
	)
	source, err := formdomain.LoadTranslationSourceDocument(ctx, db, formID)
	require.NoError(t, err)
	plan, err := formdomain.BuildTranslationExtractionPlan(formID, "en", "ko", source)
	require.NoError(t, err)
	targets := map[string]translation.UnitResult{
		"entity:title":            {UnitID: "entity:title", TranslatedText: ""},
		"step:step-1:title":       {UnitID: "step:step-1:title", TranslatedText: "문의"},
		"field:field-email:label": {UnitID: "field:field-email:label", TranslatedText: ""},
	}

	var result formdomain.TranslationInterchangeResult
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var applyErr error
		result, applyErr = service.ApplyTranslationInterchange(ctx, tx, formdomain.TranslationInterchangeMutation{
			FormID: formID, SourceLocale: "en", TargetLocale: "ko",
			Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
			Source: source, Plan: plan, Targets: targets,
			UnitHandles: []string{"entity:title", "step:step-1:title", "field:field-email:label"},
			Now:         now.Add(time.Minute),
		})
		return applyErr
	})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.NotEmpty(t, result.Revision)

	var row struct {
		Title       *string `gorm:"column:title"`
		ContentJSON []byte  `gorm:"column:content_json"`
	}
	require.NoError(t, db.Table("form_translation").Select("title, content_json").
		Where("entity_id = ?::uuid AND locale = 'ko'", formID).Take(&row).Error)
	require.NotNil(t, row.Title)
	require.Empty(t, *row.Title)
	require.JSONEq(t, `{"id":"source-schema","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"","type":"email","required":true}]}]}`, string(row.ContentJSON))

	var audits []formAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id,
		request_id::text AS request_id, attributes FROM domain_audit
		WHERE target_type = 'form' AND target_id = ? ORDER BY occurred_at, audit_id`, formID).Scan(&audits).Error)
	require.Len(t, audits, 1)
	require.JSONEq(t, `{"changed_fields":["locale_content"],"locale":"ko","item_operation":"created"}`, string(audits[0].Attributes))

	staleAbsentRevision := ""
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, applyErr := service.ApplyTranslationInterchange(ctx, tx, formdomain.TranslationInterchangeMutation{
			FormID: formID, SourceLocale: "en", TargetLocale: "ko",
			Mode:             managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
			ExpectedRevision: &staleAbsentRevision, Source: source, Plan: plan, Targets: targets,
			UnitHandles: []string{"entity:title", "step:step-1:title", "field:field-email:label"},
			Now:         now.Add(2 * time.Minute),
		})
		return applyErr
	})
	var conflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &conflict)

	rollbackPlan, err := formdomain.BuildTranslationExtractionPlan(formID, "en", "fr", source)
	require.NoError(t, err)
	failing := newAuditedInternalFormServiceForIntegration(
		db, nil, failingFormAuditAppender{}, integrationSpiceDB(t),
	)
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, applyErr := failing.ApplyTranslationInterchange(ctx, tx, formdomain.TranslationInterchangeMutation{
			FormID: formID, SourceLocale: "en", TargetLocale: "fr",
			Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
			Source: source, Plan: rollbackPlan,
			Targets: map[string]translation.UnitResult{
				"entity:title": {UnitID: "entity:title", TranslatedText: "Contact"},
			},
			UnitHandles: []string{"entity:title"},
			Now:         now.Add(3 * time.Minute),
		})
		return applyErr
	})
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Table("form_translation").
		Where("entity_id = ?::uuid AND locale = 'fr'", formID).Count(&count).Error)
	require.Zero(t, count)
}
