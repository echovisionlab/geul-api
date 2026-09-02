//go:build integration

package menu_test

import (
	"testing"

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

func TestMenuAIDocumentExactTargetLifecycleSeedsCASAndAuditsIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Menu AI admin "+identityID[:8])
	spiceDB := testutil.IntegrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	service := newMenuIntegrationService(db, apitelemetry.NewDurableWriter(db), spiceDB)

	created, err := service.CreateMenu(ctx, connect.NewRequest(&managev1.CreateMenuRequest{
		Name: "Menu AI " + identityID[:8],
		Items: []*managev1.MenuItem{
			menuAuditItems("home", "/")[0],
			menuAuditItems("about", "/about")[0],
		},
	}))
	require.NoError(t, err)
	menuID := created.Msg.Id
	sourceLocale := translation.DefaultLocale
	targetLocale := "ko"
	if targetLocale == sourceLocale {
		targetLocale = "en"
	}

	source, err := service.LoadAIDocument(ctx, menuID, sourceLocale)
	require.NoError(t, err)
	sourceEmpty, err := service.ExecuteAIDocumentMutation(
		ctx, menuID, sourceLocale, menudomain.AIDocumentMutationEdit,
		menudomain.AIDocumentExecutionApply,
		menuMutation(source.DocumentRevision, nil, menudomain.AIDocumentOperation{
			Kind: menudomain.AIDocumentSetItemField, ItemID: "about", Field: "label",
			Value: menudomain.AIDocumentValue{Kind: menudomain.AIDocumentText, Text: ""},
		}),
	)
	require.NoError(t, err)
	require.True(t, sourceEmpty.Changed)
	require.Nil(t, sourceEmpty.TargetRevision)

	missing, err := service.LoadAIDocument(ctx, menuID, targetLocale)
	require.NoError(t, err)
	require.False(t, missing.LocaleExists)
	require.Nil(t, missing.TargetRevision)

	createdTarget, err := service.ExecuteAIDocumentMutation(
		ctx, menuID, targetLocale, menudomain.AIDocumentMutationEdit,
		menudomain.AIDocumentExecutionApply,
		menuMutation(missing.DocumentRevision, nil, menudomain.AIDocumentOperation{
			Kind: menudomain.AIDocumentSetItemField, ItemID: "home", Field: "label",
			Value: menudomain.AIDocumentValue{Kind: menudomain.AIDocumentText, Text: "홈"},
		}),
	)
	require.NoError(t, err)
	require.True(t, createdTarget.Changed)
	require.Equal(t, missing.DocumentRevision, createdTarget.DocumentRevision)
	require.NotNil(t, createdTarget.TargetRevision)
	require.JSONEq(t, `[{"id":"about","label":""},{"id":"home","label":"홈"}]`, menuLocaleJSON(t, db, menuID, targetLocale))

	rows := menuAuditRows(t, db, menuID)
	require.Len(t, rows, 3)
	require.Equal(t, memberID, rows[2].ActorMemberID)
	require.JSONEq(t,
		`{"changed_fields":["locale_content"],"locale":"`+targetLocale+`","item_operation":"created"}`,
		string(rows[2].Attributes),
	)

	current, err := service.LoadAIDocument(ctx, menuID, targetLocale)
	require.NoError(t, err)
	noChange, err := service.ExecuteAIDocumentMutation(
		ctx, menuID, targetLocale, menudomain.AIDocumentMutationEdit,
		menudomain.AIDocumentExecutionApply,
		menuMutation(current.DocumentRevision, current.TargetRevision, menudomain.AIDocumentOperation{
			Kind: menudomain.AIDocumentSetItemField, ItemID: "home", Field: "label",
			Value: menudomain.AIDocumentValue{Kind: menudomain.AIDocumentText, Text: "홈"},
		}),
	)
	require.NoError(t, err)
	require.False(t, noChange.Changed)
	require.Equal(t, current.TargetRevision, noChange.TargetRevision)
	require.Len(t, menuAuditRows(t, db, menuID), 3, "semantic no-op must not append an Audit")

	_, err = service.ExecuteAIDocumentMutation(
		ctx, menuID, targetLocale, menudomain.AIDocumentMutationEdit,
		menudomain.AIDocumentExecutionApply,
		menuMutation(current.DocumentRevision, nil, menudomain.AIDocumentOperation{
			Kind: menudomain.AIDocumentSetItemField, ItemID: "home", Field: "label",
			Value: menudomain.AIDocumentValue{Kind: menudomain.AIDocumentText, Text: "stale"},
		}),
	)
	var conflict *menudomain.AIDocumentRevisionConflict
	require.ErrorAs(t, err, &conflict)
	require.True(t, conflict.Target)
	require.Len(t, menuAuditRows(t, db, menuID), 3, "CAS conflict must not append an Audit")

	_, err = service.ExecuteAIDocumentMutation(
		ctx, menuID, targetLocale, menudomain.AIDocumentMutationEdit,
		menudomain.AIDocumentExecutionApply,
		menuMutation(current.DocumentRevision, current.TargetRevision, menudomain.AIDocumentOperation{
			Kind: menudomain.AIDocumentSetItemField, ItemID: "home", Field: "url",
			Value: menudomain.AIDocumentValue{Kind: menudomain.AIDocumentText, Text: "/forbidden"},
		}),
	)
	var invalid *menudomain.AIDocumentValidationError
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, menudomain.AIDocumentIssueTargetForbidden, invalid.Issues[0].Code)
	require.Len(t, menuAuditRows(t, db, menuID), 3, "rejected target structure must not append an Audit")

	emptyTarget, err := service.ExecuteAIDocumentMutation(
		ctx, menuID, targetLocale, menudomain.AIDocumentMutationEdit,
		menudomain.AIDocumentExecutionApply,
		menuMutation(current.DocumentRevision, current.TargetRevision, menudomain.AIDocumentOperation{
			Kind: menudomain.AIDocumentSetItemField, ItemID: "home", Field: "label",
			Value: menudomain.AIDocumentValue{Kind: menudomain.AIDocumentText, Text: ""},
		}),
	)
	require.NoError(t, err)
	require.JSONEq(t, `[{"id":"about","label":""},{"id":"home","label":""}]`, menuLocaleJSON(t, db, menuID, targetLocale))
	require.Len(t, menuAuditRows(t, db, menuID), 4)

	deleted, err := service.ExecuteAIDocumentMutation(
		ctx, menuID, targetLocale, menudomain.AIDocumentMutationEdit,
		menudomain.AIDocumentExecutionApply,
		menuMutation(emptyTarget.DocumentRevision, emptyTarget.TargetRevision, menudomain.AIDocumentOperation{
			Kind: menudomain.AIDocumentDeleteTranslation,
		}),
	)
	require.NoError(t, err)
	require.Equal(t, emptyTarget.DocumentRevision, deleted.DocumentRevision)
	require.Nil(t, deleted.TargetRevision)
	require.False(t, menuLocaleExists(t, db, menuID, targetLocale))
	rows = menuAuditRows(t, db, menuID)
	require.Len(t, rows, 5)
	require.JSONEq(t,
		`{"changed_fields":["locale_content"],"locale":"`+targetLocale+`","item_operation":"deleted"}`,
		string(rows[4].Attributes),
	)

	recreated, err := service.ExecuteAIDocumentMutation(
		ctx, menuID, targetLocale, menudomain.AIDocumentMutationEdit,
		menudomain.AIDocumentExecutionApply,
		menuMutation(deleted.DocumentRevision, nil, menudomain.AIDocumentOperation{
			Kind: menudomain.AIDocumentCreateTranslation,
		}),
	)
	require.NoError(t, err)
	require.NotNil(t, recreated.TargetRevision)
	require.JSONEq(t, `[{"id":"about","label":""},{"id":"home","label":"home"}]`, menuLocaleJSON(t, db, menuID, targetLocale))
	rows = menuAuditRows(t, db, menuID)
	require.Len(t, rows, 6)
	require.JSONEq(t,
		`{"changed_fields":["locale_content"],"locale":"`+targetLocale+`","item_operation":"created"}`,
		string(rows[5].Attributes),
	)
}

func menuMutation(
	expectedDocumentRevision string,
	expectedTargetRevision *string,
	operations ...menudomain.AIDocumentOperation,
) menudomain.AIDocumentMutationCompiler {
	return func(snapshot menudomain.AIDocumentSnapshot) (menudomain.AIDocumentApply, error) {
		return menudomain.AIDocumentApply{
			MenuID: snapshot.ID, Locale: snapshot.Locale,
			ExpectedDocumentRevision: expectedDocumentRevision,
			ExpectedTargetRevision:   cloneMenuString(expectedTargetRevision),
			Operations:               append([]menudomain.AIDocumentOperation(nil), operations...),
		}, nil
	}
}

func menuLocaleJSON(t *testing.T, db *gorm.DB, menuID, locale string) string {
	t.Helper()
	var row struct {
		ItemsJSON string `gorm:"column:items_json"`
	}
	require.NoError(t, db.Table("menu_translation").Select("items_json").
		Where("entity_id = ? AND locale = ?", menuID, locale).Take(&row).Error)
	return row.ItemsJSON
}

func menuLocaleExists(t *testing.T, db *gorm.DB, menuID, locale string) bool {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("menu_translation").
		Where("entity_id = ? AND locale = ?", menuID, locale).Count(&count).Error)
	return count == 1
}

func cloneMenuString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
