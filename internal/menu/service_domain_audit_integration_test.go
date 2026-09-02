//go:build integration

package menu_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	menuadapter "github.com/echovisionlab/geul-api/internal/adapters/menu"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type menuDomainAuditRow struct {
	Action        string
	TargetType    string `gorm:"column:target_type"`
	TargetID      string `gorm:"column:target_id"`
	ActorMemberID string `gorm:"column:actor_member_id"`
	RequestID     string `gorm:"column:request_id"`
	Attributes    []byte `gorm:"column:attributes"`
}

func TestMenuDomainAuditDirectCreateUpdateNoopAndRollbackIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Menu audit admin "+identityID[:8])
	spiceDB := testutil.IntegrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	service := newMenuIntegrationService(db, apitelemetry.NewDurableWriter(db), spiceDB)
	items := menuAuditItems("first", "/first")

	created, err := service.CreateMenu(ctx, connect.NewRequest(&managev1.CreateMenuRequest{
		Name: "Audited Menu", Items: items,
	}))
	require.NoError(t, err)
	menuID := created.Msg.Id
	documentID, documentRevision := menuDocumentState(t, db, menuID)
	var initializedSource struct {
		SourceLocale string `gorm:"column:source_locale"`
		ItemsJSON    []byte `gorm:"column:items_json"`
	}
	require.NoError(t, db.Raw(
		`SELECT root.source_locale, values.items_json
		 FROM menu AS root
		 JOIN menu_translation AS values
		   ON values.entity_id = root.id AND values.locale = root.source_locale
		 WHERE root.id = ?`,
		menuID,
	).Scan(&initializedSource).Error)
	require.Equal(t, translation.DefaultLocale, initializedSource.SourceLocale)
	require.JSONEq(t, `[{"id":"first","label":"first"}]`, string(initializedSource.ItemsJSON))
	rows := menuAuditRows(t, db, menuID)
	require.Len(t, rows, 1)
	require.Equal(t, string(sharedtelemetry.AuditMenuCreated), rows[0].Action)
	require.Equal(t, "menu", rows[0].TargetType)
	require.Equal(t, menuID, rows[0].TargetID)
	require.Equal(t, memberID, rows[0].ActorMemberID)
	require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), rows[0].RequestID)

	var beforeUpdatedAt time.Time
	require.NoError(t, db.Table("menu").Select("updated_at").Where("id = ?", menuID).Scan(&beforeUpdatedAt).Error)
	_, err = service.UpdateMenu(ctx, connect.NewRequest(&managev1.UpdateMenuRequest{
		Id: menuID, Name: menuStringPtr("Audited Menu"), Items: &managev1.MenuItemsUpdate{Items: items},
	}))
	require.NoError(t, err)
	var afterUpdatedAt time.Time
	require.NoError(t, db.Table("menu").Select("updated_at").Where("id = ?", menuID).Scan(&afterUpdatedAt).Error)
	require.Equal(t, beforeUpdatedAt, afterUpdatedAt)
	noopDocumentID, noopDocumentRevision := menuDocumentState(t, db, menuID)
	require.Equal(t, documentID, noopDocumentID)
	require.Equal(t, documentRevision, noopDocumentRevision)
	require.Len(t, menuAuditRows(t, db, menuID), 1)

	_, err = service.UpdateMenu(ctx, connect.NewRequest(&managev1.UpdateMenuRequest{
		Id: menuID, Name: menuStringPtr("Audited Menu next"),
		Items: &managev1.MenuItemsUpdate{Items: menuAuditItems("next", "/next")},
	}))
	require.NoError(t, err)
	updatedDocumentID, updatedDocumentRevision := menuDocumentState(t, db, menuID)
	require.Equal(t, documentID, updatedDocumentID)
	require.NotEqual(t, documentRevision, updatedDocumentRevision)
	rows = menuAuditRows(t, db, menuID)
	require.Len(t, rows, 2)
	require.Equal(t, string(sharedtelemetry.AuditMenuUpdated), rows[1].Action)
	require.JSONEq(t, `{"changed_fields":["items","name"]}`, string(rows[1].Attributes))
	require.Equal(t, memberID, rows[1].ActorMemberID)
	require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), rows[1].RequestID)

	var documentsBeforeFailedCreate int64
	require.NoError(t, db.Table("content_document").Count(&documentsBeforeFailedCreate).Error)
	_, err = newMenuIntegrationService(db, failingMenuDomainAuditAppender{}, spiceDB).CreateMenu(
		ctx, connect.NewRequest(&managev1.CreateMenuRequest{Name: "Must rollback", Items: menuAuditItems("rollback", "/rollback")}),
	)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Table("menu").Where("name = ?", "Must rollback").Count(&count).Error)
	require.Zero(t, count)
	var documentsAfterFailedCreate int64
	require.NoError(t, db.Table("content_document").Count(&documentsAfterFailedCreate).Error)
	require.Equal(t, documentsBeforeFailedCreate, documentsAfterFailedCreate)

	_, err = newMenuIntegrationService(db, failingMenuDomainAuditAppender{}, spiceDB).UpdateMenu(
		ctx, connect.NewRequest(&managev1.UpdateMenuRequest{Id: menuID, Name: menuStringPtr("Must not update")}),
	)
	require.Error(t, err)
	var storedName string
	require.NoError(t, db.Table("menu").Select("name").Where("id = ?", menuID).Scan(&storedName).Error)
	require.Equal(t, "Audited Menu next", storedName)
	rollbackDocumentID, rollbackDocumentRevision := menuDocumentState(t, db, menuID)
	require.Equal(t, documentID, rollbackDocumentID)
	require.Equal(t, updatedDocumentRevision, rollbackDocumentRevision)
}

func TestMenuDeleteUnbindsActualSlotsAndAuditsOnlyWhenNeededIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Menu delete admin "+identityID[:8])
	spiceDB := testutil.IntegrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	service := newMenuIntegrationService(db, apitelemetry.NewDurableWriter(db), spiceDB)
	bound, err := service.CreateMenu(ctx, connect.NewRequest(&managev1.CreateMenuRequest{Name: "Bound Menu", Items: menuAuditItems("bound", "/bound")}))
	require.NoError(t, err)
	boundID := bound.Msg.Id
	boundDocumentID, _ := menuDocumentState(t, db, boundID)
	require.NoError(t, db.Exec(`UPDATE site_settings SET
		menu_header_id = ?::uuid, menu_footer_id = ?::uuid, menu_avatar_dropdown_id = ?::uuid
		WHERE id = 1`, boundID, boundID, boundID).Error)

	_, err = service.DeleteMenu(ctx, connect.NewRequest(&managev1.DeleteMenuRequest{Id: boundID}))
	require.NoError(t, err)
	var settings struct {
		Header *string `gorm:"column:menu_header_id"`
		Footer *string `gorm:"column:menu_footer_id"`
		Avatar *string `gorm:"column:menu_avatar_dropdown_id"`
	}
	require.NoError(t, db.Raw(`SELECT menu_header_id::text, menu_footer_id::text, menu_avatar_dropdown_id::text FROM site_settings WHERE id = 1`).Scan(&settings).Error)
	require.Nil(t, settings.Header)
	require.Nil(t, settings.Footer)
	require.Nil(t, settings.Avatar)
	var boundDocumentCount int64
	require.NoError(t, db.Table("content_document").Where("id = ?", boundDocumentID).Count(&boundDocumentCount).Error)
	require.Zero(t, boundDocumentCount)
	rows := menuAuditRows(t, db, boundID)
	require.Len(t, rows, 2)
	require.Equal(t, string(sharedtelemetry.AuditMenuCreated), rows[0].Action)
	require.Equal(t, string(sharedtelemetry.AuditMenuDeleted), rows[1].Action)
	var settingsAudits []menuDomainAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id, request_id::text AS request_id, attributes
		FROM domain_audit WHERE action = ? ORDER BY occurred_at, audit_id`, sharedtelemetry.AuditSiteSettingsUpdated).Scan(&settingsAudits).Error)
	require.Len(t, settingsAudits, 1)
	require.Equal(t, "site_settings", settingsAudits[0].TargetType)
	require.Equal(t, "1", settingsAudits[0].TargetID)
	require.JSONEq(t, `{"changed_fields":["menu_avatar_dropdown_id","menu_footer_id","menu_header_id"]}`, string(settingsAudits[0].Attributes))

	zero, err := service.CreateMenu(ctx, connect.NewRequest(&managev1.CreateMenuRequest{Name: "Zero Slot Menu", Items: menuAuditItems("zero", "/zero")}))
	require.NoError(t, err)
	_, err = service.DeleteMenu(ctx, connect.NewRequest(&managev1.DeleteMenuRequest{Id: zero.Msg.Id}))
	require.NoError(t, err)
	var settingsAuditCount int64
	require.NoError(t, db.Table("domain_audit").Where("action = ?", sharedtelemetry.AuditSiteSettingsUpdated).Count(&settingsAuditCount).Error)
	require.EqualValues(t, 1, settingsAuditCount)

	rollback, err := service.CreateMenu(ctx, connect.NewRequest(&managev1.CreateMenuRequest{Name: "Rollback Delete Menu", Items: menuAuditItems("rollback-delete", "/rollback-delete")}))
	require.NoError(t, err)
	rollbackDocumentID, rollbackDocumentRevision := menuDocumentState(t, db, rollback.Msg.Id)
	require.NoError(t, db.Exec(`UPDATE site_settings SET menu_secondary_id = ?::uuid WHERE id = 1`, rollback.Msg.Id).Error)
	_, err = newMenuIntegrationService(db, failingMenuDomainAuditAppender{}, spiceDB).DeleteMenu(
		ctx, connect.NewRequest(&managev1.DeleteMenuRequest{Id: rollback.Msg.Id}),
	)
	require.Error(t, err)
	var menuCount int64
	require.NoError(t, db.Table("menu").Where("id = ?", rollback.Msg.Id).Count(&menuCount).Error)
	require.EqualValues(t, 1, menuCount)
	var secondary *string
	require.NoError(t, db.Raw(`SELECT menu_secondary_id::text FROM site_settings WHERE id = 1`).Scan(&secondary).Error)
	require.NotNil(t, secondary)
	require.Equal(t, rollback.Msg.Id, *secondary)
	storedRollbackDocumentID, storedRollbackDocumentRevision := menuDocumentState(t, db, rollback.Msg.Id)
	require.Equal(t, rollbackDocumentID, storedRollbackDocumentID)
	require.Equal(t, rollbackDocumentRevision, storedRollbackDocumentRevision)
}

func TestMenuRejectsDanglingCategoryTagAndSeriesTargetsIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Menu target admin "+identityID[:8])
	spiceDB := testutil.IntegrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	service := newMenuIntegrationService(db, apitelemetry.NewDurableWriter(db), spiceDB)
	for _, target := range []struct {
		name     string
		linkType managev1.MenuLinkType
	}{
		{"category", managev1.MenuLinkType_MENU_LINK_TYPE_CATEGORY},
		{"tag", managev1.MenuLinkType_MENU_LINK_TYPE_TAG},
		{"series", managev1.MenuLinkType_MENU_LINK_TYPE_SERIES},
	} {
		t.Run(target.name, func(t *testing.T) {
			id := uuid.NewString()
			_, err := service.CreateMenu(ctx, connect.NewRequest(&managev1.CreateMenuRequest{
				Name: "Missing " + target.name,
				Items: []*managev1.MenuItem{{
					Id: "missing-" + target.name, Label: target.name, LinkType: target.linkType, TargetId: &id,
				}},
			}))
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestMenuAuthorizationCreateDeleteIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Menu authorization admin")
	spiceDB := testutil.IntegrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	service := newMenuIntegrationService(db, apitelemetry.NewDurableWriter(db), spiceDB)

	created, err := service.CreateMenu(ctx, connect.NewRequest(&managev1.CreateMenuRequest{
		Name: "Authorization lifecycle menu", Items: menuAuditItems("home", "/"),
	}))
	require.NoError(t, err)
	requireMenuManagePermission(t, spiceDB, created.Msg.Id, identityID, true)

	_, err = service.DeleteMenu(ctx, connect.NewRequest(&managev1.DeleteMenuRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	requireMenuManagePermission(t, spiceDB, created.Msg.Id, identityID, false)
}

func requireMenuManagePermission(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	menuID string,
	identityID string,
	want bool,
) {
	t.Helper()
	can, err := policyv1.Menu.Manage(menuID)
	require.NoError(t, err)
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	allowed, err := spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, want, allowed)
}

func menuAuditItems(id, url string) []*managev1.MenuItem {
	return []*managev1.MenuItem{{
		Id: id, Label: id, LinkType: managev1.MenuLinkType_MENU_LINK_TYPE_CUSTOM, Url: &url,
	}}
}

func menuAuditRows(t *testing.T, db *gorm.DB, menuID string) []menuDomainAuditRow {
	t.Helper()
	var rows []menuDomainAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id,
		request_id::text AS request_id, attributes FROM domain_audit
		WHERE target_type = 'menu' AND target_id = ? ORDER BY occurred_at, audit_id`, menuID).Scan(&rows).Error)
	return rows
}

func menuDocumentState(t *testing.T, db *gorm.DB, menuID string) (string, string) {
	t.Helper()
	var document struct {
		ID       string `gorm:"column:id"`
		Profile  string `gorm:"column:profile"`
		Revision string `gorm:"column:revision"`
	}
	require.NoError(t, db.Raw(
		`SELECT document.id::text AS id, document.profile, document.revision::text AS revision
		 FROM menu AS root
		 JOIN content_document AS document ON document.id = root.content_document_id
		 WHERE root.id = ?`,
		menuID,
	).Scan(&document).Error)
	require.NotEmpty(t, document.ID)
	require.Equal(t, "compact", document.Profile)
	require.NotEmpty(t, document.Revision)
	return document.ID, document.Revision
}

func newMenuIntegrationService(
	db *gorm.DB,
	auditWriter domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
) *menudomain.MenuService {
	return menudomain.NewAuditedMenuService(
		db,
		auditWriter,
		menuadapter.NewSiteSettingsReferences(auditWriter),
		menuadapter.NewTargetReferences(),
		spiceDB,
	)
}

type failingMenuDomainAuditAppender struct{}

func (failingMenuDomainAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("audit unavailable")
}

func menuStringPtr(value string) *string { return &value }
