//go:build integration

package series

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPostSeriesTranslationInterchangePersistsSparseTargetCASAuditAndRollbackIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Post Series XLIFF admin")
	stack := testutil.SetupOryStack(t)
	grantIntegrationGlobalRole(t, stack.SpiceDBClient, adminID, policyv1.Role.Admin())
	ctx := seriesAuditContext(t, adminID)
	service := auditedSeriesIntegrationService(
		t, db, adminID, stack.SpiceDBClient, apitelemetry.NewDurableWriter(db),
	)
	created, err := service.CreateSeries(ctx, connect.NewRequest(&managev1.CreateSeriesRequest{
		Title: "Post Series XLIFF", Description: stringPointer("Source summary"),
	}))
	require.NoError(t, err)
	seriesID := created.Msg.Id
	sourceLocale := created.Msg.SourceLocale
	targetLocale := "ko"
	if sourceLocale == targetLocale {
		targetLocale = "en"
	}
	source, err := LoadTranslationSourceDocument(ctx, db, seriesID)
	require.NoError(t, err)
	plan, err := BuildTranslationExtractionPlan(seriesID, sourceLocale, targetLocale, source)
	require.NoError(t, err)

	var result TranslationInterchangeResult
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var applyErr error
		result, applyErr = service.ApplyTranslationInterchange(ctx, tx, TranslationInterchangeMutation{
			SeriesID: seriesID, SourceLocale: sourceLocale, TargetLocale: targetLocale,
			Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
			Plan: plan, Targets: map[string]translation.UnitResult{
				"entity:title": {UnitID: "entity:title", TranslatedText: ""},
			},
			UnitHandles: []string{"entity:title"}, Now: time.Now().UTC(),
		})
		return applyErr
	})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.NotEmpty(t, result.Revision)

	var row model.SeriesTranslation
	require.NoError(t, db.Table("series_translation").
		Where("entity_id = ? AND locale = ?", seriesID, targetLocale).Take(&row).Error)
	require.NotNil(t, row.Title)
	require.Empty(t, *row.Title)
	require.Nil(t, row.Summary)
	audits := postSeriesAuditRows(t, db, seriesID)
	require.Len(t, audits, 2)
	require.JSONEq(t, `{"changed_fields":["locale_content"],"locale":"`+targetLocale+`","item_operation":"created"}`, string(audits[1].Attributes))
	oldTargetRevision := result.Revision
	updatedSourceTitle := "Post Series XLIFF updated"
	_, err = service.UpdateSeries(ctx, connect.NewRequest(&managev1.UpdateSeriesRequest{
		Id: seriesID, Title: &updatedSourceTitle,
	}))
	require.NoError(t, err)
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, applyErr := service.ApplyTranslationInterchange(ctx, tx, TranslationInterchangeMutation{
			SeriesID: seriesID, SourceLocale: sourceLocale, TargetLocale: targetLocale,
			Mode:             managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
			ExpectedRevision: &oldTargetRevision, Plan: plan,
			Targets: map[string]translation.UnitResult{
				"entity:title": {UnitID: "entity:title", TranslatedText: "Changed after source edit"},
			},
			UnitHandles: []string{"entity:title"}, Now: time.Now().UTC(),
		})
		return applyErr
	})
	var sourceChangedConflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &sourceChangedConflict, "source document revision must invalidate the target token")

	staleAbsentRevision := ""
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, applyErr := service.ApplyTranslationInterchange(ctx, tx, TranslationInterchangeMutation{
			SeriesID: seriesID, SourceLocale: sourceLocale, TargetLocale: targetLocale,
			Mode:             managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
			ExpectedRevision: &staleAbsentRevision, Plan: plan,
			Targets: map[string]translation.UnitResult{
				"entity:title": {UnitID: "entity:title", TranslatedText: "Changed"},
			},
			UnitHandles: []string{"entity:title"}, Now: time.Now().UTC(),
		})
		return applyErr
	})
	var conflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &conflict)

	rollbackLocale := "fr"
	if sourceLocale == rollbackLocale || targetLocale == rollbackLocale {
		rollbackLocale = "de"
	}
	rollbackPlan, err := BuildTranslationExtractionPlan(seriesID, sourceLocale, rollbackLocale, source)
	require.NoError(t, err)
	failing := auditedSeriesIntegrationService(
		t, db, adminID, stack.SpiceDBClient, seriesFailingAuditAppender{},
	)
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, applyErr := failing.ApplyTranslationInterchange(ctx, tx, TranslationInterchangeMutation{
			SeriesID: seriesID, SourceLocale: sourceLocale, TargetLocale: rollbackLocale,
			Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
			Plan: rollbackPlan, Targets: map[string]translation.UnitResult{
				"entity:title": {UnitID: "entity:title", TranslatedText: "Titre"},
			},
			UnitHandles: []string{"entity:title"}, Now: time.Now().UTC(),
		})
		return applyErr
	})
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Table("series_translation").
		Where("entity_id = ? AND locale = ?", seriesID, rollbackLocale).Count(&count).Error)
	require.Zero(t, count)
}
