//go:build integration

package menu_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMenuTranslationInterchangePersistsSparseTargetCASAuditAndRollbackIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Menu XLIFF admin "+identityID[:8])
	spiceDB := testutil.IntegrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	service := newMenuIntegrationService(db, apitelemetry.NewDurableWriter(db), spiceDB)

	created, err := service.CreateMenu(ctx, connect.NewRequest(&managev1.CreateMenuRequest{
		Name: "Menu XLIFF", Items: []*managev1.MenuItem{
			menuAuditItems("home", "/")[0],
			menuAuditItems("about", "/about")[0],
		},
	}))
	require.NoError(t, err)
	source, err := menudomain.LoadTranslationSourceDocument(ctx, db, created.Msg.Id)
	require.NoError(t, err)
	plan, err := menudomain.BuildTranslationExtractionPlan(
		created.Msg.Id, translation.DefaultLocale, "ko", source,
	)
	require.NoError(t, err)

	var result menudomain.TranslationInterchangeResult
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var applyErr error
		result, applyErr = service.ApplyTranslationInterchange(ctx, tx, menudomain.TranslationInterchangeMutation{
			MenuID: created.Msg.Id, SourceLocale: translation.DefaultLocale, TargetLocale: "ko",
			Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
			Plan: plan, Targets: map[string]translation.UnitResult{
				"item:home:label": {UnitID: "item:home:label", TranslatedText: ""},
			},
			UnitHandles: []string{"item:home:label"}, Now: time.Now().UTC(),
		})
		return applyErr
	})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.NotEmpty(t, result.Revision)

	var stored struct {
		ItemsJSON []byte `gorm:"column:items_json"`
	}
	require.NoError(t, db.Table("menu_translation").Select("items_json").
		Where("entity_id = ? AND locale = 'ko'", created.Msg.Id).Scan(&stored).Error)
	require.JSONEq(t, `[{"id":"about","label":"about"},{"id":"home","label":""}]`, string(stored.ItemsJSON))
	rows := menuAuditRows(t, db, created.Msg.Id)
	require.Len(t, rows, 2)
	require.JSONEq(t, `{"changed_fields":["locale_content"],"locale":"ko","item_operation":"created"}`, string(rows[1].Attributes))

	createdRevision := result.Revision
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var applyErr error
		result, applyErr = service.ApplyTranslationInterchange(ctx, tx, menudomain.TranslationInterchangeMutation{
			MenuID: created.Msg.Id, SourceLocale: translation.DefaultLocale, TargetLocale: "ko",
			Mode:             managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
			ExpectedRevision: &createdRevision, Plan: plan,
			Targets: map[string]translation.UnitResult{
				"item:home:label": {UnitID: "item:home:label", TranslatedText: ""},
			},
			UnitHandles: []string{"item:home:label"}, Now: time.Now().UTC(),
		})
		return applyErr
	})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, []string{"item:about:label"}, result.AffectedUnitHandles)
	require.JSONEq(t, `[{"id":"home","label":""}]`, menuLocaleJSON(t, db, created.Msg.Id, "ko"))
	rows = menuAuditRows(t, db, created.Msg.Id)
	require.Len(t, rows, 3)
	require.JSONEq(t, `{"changed_fields":["locale_content"],"locale":"ko","item_operation":"updated"}`, string(rows[2].Attributes))

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, applyErr := service.ApplyTranslationInterchange(ctx, tx, menudomain.TranslationInterchangeMutation{
			MenuID: created.Msg.Id, SourceLocale: translation.DefaultLocale, TargetLocale: "ko",
			Mode:             managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
			ExpectedRevision: &createdRevision, Plan: plan,
			Targets: map[string]translation.UnitResult{
				"item:home:label": {UnitID: "item:home:label", TranslatedText: "홈"},
			},
			UnitHandles: []string{"item:home:label"}, Now: time.Now().UTC(),
		})
		return applyErr
	})
	var conflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &conflict)
	require.Len(t, menuAuditRows(t, db, created.Msg.Id), 3)

	rollbackPlan, err := menudomain.BuildTranslationExtractionPlan(
		created.Msg.Id, translation.DefaultLocale, "fr", source,
	)
	require.NoError(t, err)
	failing := newMenuIntegrationService(db, failingMenuDomainAuditAppender{}, spiceDB)
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, applyErr := failing.ApplyTranslationInterchange(ctx, tx, menudomain.TranslationInterchangeMutation{
			MenuID: created.Msg.Id, SourceLocale: translation.DefaultLocale, TargetLocale: "fr",
			Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
			Plan: rollbackPlan, Targets: map[string]translation.UnitResult{
				"item:home:label": {UnitID: "item:home:label", TranslatedText: "Accueil"},
			},
			UnitHandles: []string{"item:home:label"}, Now: time.Now().UTC(),
		})
		return applyErr
	})
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Table("menu_translation").
		Where("entity_id = ? AND locale = 'fr'", created.Msg.Id).Count(&count).Error)
	require.Zero(t, count)
}
