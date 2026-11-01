package menu_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func requireCode(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	require.Error(t, err)
	mapped := errs.Wrap(err)
	require.Equal(t, code, mapped.Code(), mapped.Message())
	if message != "" {
		require.Equal(t, message, mapped.Message())
	}
}

func TestCreateMenuStoresTheRootItsDocumentSourceValuesAuditAndPolicy(t *testing.T) {
	f := newMenuFixture(t)

	created, err := f.service.CreateMenu(f.ctx, menu.CreateMenuCommand{Name: "  Main  ", Items: []menu.Item{
		{ID: "first", Label: "first", LinkType: menu.LinkCustom, URL: stringPointer("/first"),
			Visibility: &menu.Visibility{Mode: "", Roles: []string{" Admin ", "admin"}}},
	}})

	require.NoError(t, err)
	require.Equal(t, "Main", created.Name)
	require.Equal(t, translation.DefaultLocale, created.SourceLocale)
	root, ok := f.store.Stored(created.ID)
	require.True(t, ok)
	require.JSONEq(t, `[{"id":"first","label":"first","linkType":"custom","url":"/first","visibility":{"mode":"all","roles":["admin"]}}]`, string(root.ItemsJSON))
	document, ok := f.store.StoredDocument(root.ContentDocumentID)
	require.True(t, ok)
	require.Equal(t, "compact", document.Profile)
	require.Equal(t, map[string]string{"first": "first"}, f.labels(t, created.ID, translation.DefaultLocale))

	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditMenuCreated, records[0].Action)
	require.Equal(t, "menu", records[0].TargetType)
	require.Equal(t, created.ID, records[0].TargetID)
	require.Equal(t, f.memberID, records[0].MemberID)
	require.Equal(t, sharedtelemetry.RequestIDFromContext(f.ctx), records[0].RequestID)

	touch, err := policyv1.Menu.TouchPolicy(created.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, f.checker.Applied())
	require.Equal(t, []mutation.Step{
		mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange,
		mutation.StepAudit, mutation.StepRelationships, mutation.StepCommit,
	}, f.lastSteps())
	require.Equal(t, []string{f.identityID}, f.fence.Locked())
}

func TestCreateMenuRollsBackEverythingWhenTheAuditFails(t *testing.T) {
	f := newMenuFixture(t)
	auditDown := errors.New("audit unavailable")

	_, err := f.newService(t, &audittest.Recorder{Err: auditDown}).CreateMenu(f.ctx, menu.CreateMenuCommand{
		Name: "Must rollback", Items: []menu.Item{customItem("rollback", "/rollback")},
	})

	require.ErrorIs(t, err, auditDown)
	require.Zero(t, f.store.MenuCount())
	require.Zero(t, f.store.DocumentCount())
	require.Empty(t, f.checker.Applied(), "the relationship batch is written only after the audit")
	require.Equal(t, mutation.StepRollback, f.lastSteps()[len(f.lastSteps())-1])
}

func TestCreateMenuValidatesBeforeAndAuthorizesBeforeTargets(t *testing.T) {
	f := newMenuFixture(t)
	f.createMenu(t, "Taken", customItem("home", "/"))

	_, err := f.service.CreateMenu(f.ctx, menu.CreateMenuCommand{Name: "Taken"})
	requireCode(t, err, connect.CodeAlreadyExists, "menu with name 'Taken' already exists")

	_, err = f.service.CreateMenu(f.ctx, menu.CreateMenuCommand{Name: "Bad", Items: []menu.Item{{ID: "x", Label: "x"}}})
	requireCode(t, err, connect.CodeInvalidArgument, "item[0]: linkType must be specified")
	_, err = f.service.CreateMenu(f.ctx, menu.CreateMenuCommand{Name: "Bad", Items: []menu.Item{{ID: "x", Label: "x", LinkType: "7"}}})
	requireCode(t, err, connect.CodeInvalidArgument, "item[0]: invalid linkType")
	_, err = f.service.CreateMenu(f.ctx, menu.CreateMenuCommand{Name: "Bad", Items: []menu.Item{{ID: "x", Label: "x", LinkType: menu.LinkPage}}})
	requireCode(t, err, connect.CodeInvalidArgument, "item[0]: targetId or targetSlug is required for MENU_LINK_TYPE_PAGE link type")

	for _, linkType := range []menu.LinkType{menu.LinkCategory, menu.LinkTag, menu.LinkSeries} {
		target := menu.TargetReference{LinkType: linkType, ID: "missing-" + string(linkType)}
		f.store.MissTarget(target)
		_, err = f.service.CreateMenu(f.ctx, menu.CreateMenuCommand{Name: "Missing " + string(linkType), Items: []menu.Item{
			{ID: "missing", Label: "missing", LinkType: linkType, TargetID: stringPointer(target.ID)},
		}})
		requireCode(t, err, connect.CodeInvalidArgument, "items: "+string(linkType)+" target does not exist")
	}

	can, err := policyv1.Menu.Create()
	require.NoError(t, err)
	f.checker.Deny(can, nil)
	_, err = f.service.CreateMenu(f.ctx, menu.CreateMenuCommand{Name: "Denied", Items: []menu.Item{
		{ID: "missing", Label: "missing", LinkType: menu.LinkCategory, TargetID: stringPointer("missing-category")},
	}})
	requireCode(t, err, connect.CodePermissionDenied, "admin access required")
	require.Equal(t, 1, f.store.MenuCount())
}

func TestUpdateMenuWithTheStoredNameAndItemsChangesNothing(t *testing.T) {
	f := newMenuFixture(t)
	created, revision := f.createMenu(t, "Audited Menu", customItem("first", "/first"))
	before, _ := f.store.Stored(created.ID)
	items := []menu.Item{customItem("first", "/first")}

	updated, err := f.service.UpdateMenu(f.ctx, menu.UpdateMenuCommand{ID: created.ID, Name: stringPointer("Audited Menu"), Items: &items})

	require.NoError(t, err)
	require.Equal(t, created.UpdatedAt, updated.UpdatedAt)
	after, _ := f.store.Stored(created.ID)
	require.Equal(t, before, after)
	require.Equal(t, revision, f.revision(t, created.ID))
	require.Len(t, f.audit.Records(), 1)
	require.Equal(t, []menu.TargetReference{}, f.store.LockedTargets()[1], "a replaced tree locks its targets")
}

func TestUpdateMenuWritesTheChangeAdvancesTheDocumentAndPrunesLabels(t *testing.T) {
	f := newMenuFixture(t)
	created, revision := f.createMenu(t, "Audited Menu", customItem("first", "/first"), customItem("second", "/second"))
	_, err := f.service.ExecuteAIDocumentMutation(f.ctx, created.ID, "ko", menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(revision, nil, menu.AIDocumentOperation{Kind: menu.AIDocumentCreateTranslation}))
	require.NoError(t, err)
	items := []menu.Item{customItem("next", "/next"), customItem("second", "/second")}

	updated, err := f.service.UpdateMenu(f.ctx, menu.UpdateMenuCommand{ID: created.ID, Name: stringPointer(" Audited Menu next "), Items: &items})

	require.NoError(t, err)
	require.Equal(t, "Audited Menu next", updated.Name)
	require.Equal(t, []string{"next", "second"}, []string{updated.Items[0].ID, updated.Items[1].ID})
	require.NotEqual(t, revision, f.revision(t, created.ID))
	require.Equal(t, map[string]string{"next": "next", "second": "second"}, f.labels(t, created.ID, translation.DefaultLocale))
	require.Equal(t, map[string]string{"second": "second"}, f.labels(t, created.ID, "ko"), "a deleted item's target label is pruned")
	records := f.audit.Records()
	require.Equal(t, sharedtelemetry.AuditMenuUpdated, records[len(records)-1].Action)
	require.Equal(t, []string{"items", "name"}, records[len(records)-1].ChangedFields)
}

func TestUpdateMenuRollsBackWhenTheAuditFails(t *testing.T) {
	f := newMenuFixture(t)
	created, revision := f.createMenu(t, "Kept", customItem("first", "/first"))
	auditDown := errors.New("audit unavailable")

	_, err := f.newService(t, &audittest.Recorder{Err: auditDown}).UpdateMenu(f.ctx, menu.UpdateMenuCommand{ID: created.ID, Name: stringPointer("Must not update")})

	require.ErrorIs(t, err, auditDown)
	root, _ := f.store.Stored(created.ID)
	require.Equal(t, "Kept", root.Name)
	require.Equal(t, revision, f.revision(t, created.ID))
}

func TestUpdateMenuNeedsManageForTopologyAndHidesTheMenuFromAnInactivePrincipal(t *testing.T) {
	f := newMenuFixture(t)
	created, _ := f.createMenu(t, "Main", customItem("home", "/"))
	f.checker.Deny(policyv1.Menu.Manage(created.ID))

	relabeled := []menu.Item{{ID: "home", Label: "Start", LinkType: menu.LinkCustom, URL: stringPointer("/")}}
	_, err := f.service.UpdateMenu(f.ctx, menu.UpdateMenuCommand{ID: created.ID, Items: &relabeled})
	require.NoError(t, err, "a label edit needs Edit only")

	restructured := []menu.Item{customItem("home", "/"), customItem("about", "/about")}
	_, err = f.service.UpdateMenu(f.ctx, menu.UpdateMenuCommand{ID: created.ID, Items: &restructured})
	requireCode(t, err, connect.CodePermissionDenied, "no permission to manage this menu")

	_, err = f.service.UpdateMenu(f.ctx, menu.UpdateMenuCommand{ID: "a8f3c2a6-07f7-4c34-a3bd-5c5b3d3a7a51", Name: stringPointer("Missing")})
	requireCode(t, err, connect.CodeNotFound, "menu not found: a8f3c2a6-07f7-4c34-a3bd-5c5b3d3a7a51")

	f.fence.Deactivate(f.identityID)
	_, err = f.service.UpdateMenu(f.ctx, menu.UpdateMenuCommand{ID: created.ID, Name: stringPointer("Inactive")})
	requireCode(t, err, connect.CodeNotFound, "menu not found: "+created.ID)
	root, _ := f.store.Stored(created.ID)
	require.Equal(t, "Main", root.Name)
}

func TestUpdateMenuRejectsATakenName(t *testing.T) {
	f := newMenuFixture(t)
	f.createMenu(t, "Taken")
	created, _ := f.createMenu(t, "Mine")

	_, err := f.service.UpdateMenu(f.ctx, menu.UpdateMenuCommand{ID: created.ID, Name: stringPointer(" Taken ")})

	requireCode(t, err, connect.CodeAlreadyExists, "menu with name 'Taken' already exists")
}

func TestDeleteMenuClearsSiteSettingsFirstAndDeletesTheDocument(t *testing.T) {
	f := newMenuFixture(t)
	created, _ := f.createMenu(t, "Bound", customItem("bound", "/bound"))
	root, _ := f.store.Stored(created.ID)

	require.NoError(t, f.service.DeleteMenu(f.ctx, menu.DeleteMenuCommand{ID: created.ID}))

	require.Equal(t, []string{created.ID}, f.store.ClearedSiteSettings())
	_, ok := f.store.Stored(created.ID)
	require.False(t, ok)
	_, ok = f.store.StoredDocument(root.ContentDocumentID)
	require.False(t, ok)
	require.Nil(t, f.labels(t, created.ID, translation.DefaultLocale))
	records := f.audit.Records()
	require.Equal(t, sharedtelemetry.AuditMenuDeleted, records[len(records)-1].Action)
	deletePolicy, err := policyv1.Menu.DeletePolicy(created.ID)
	require.NoError(t, err)
	require.Equal(t, []policyv1.RelationshipMutation{deletePolicy}, f.checker.Applied()[1])
}

func TestDeleteMenuRollsBackTheClearedSlotsWhenItFails(t *testing.T) {
	f := newMenuFixture(t)
	created, revision := f.createMenu(t, "Rollback", customItem("rollback", "/rollback"))
	auditDown := errors.New("audit unavailable")

	err := f.newService(t, &audittest.Recorder{Err: auditDown}).DeleteMenu(f.ctx, menu.DeleteMenuCommand{ID: created.ID})
	require.ErrorIs(t, err, auditDown)
	_, ok := f.store.Stored(created.ID)
	require.True(t, ok)
	require.Equal(t, revision, f.revision(t, created.ID))
	require.Len(t, f.checker.Compensated(), 0, "nothing was written before the audit failed")

	f.checker.Deny(policyv1.Menu.Delete(created.ID))
	err = f.service.DeleteMenu(f.ctx, menu.DeleteMenuCommand{ID: created.ID})
	requireCode(t, err, connect.CodePermissionDenied, "")
	_, ok = f.store.Stored(created.ID)
	require.True(t, ok)

	err = f.service.DeleteMenu(f.ctx, menu.DeleteMenuCommand{ID: "4b8ad5bb-9c4c-4b0e-a1ad-74e07f1d7b8e"})
	requireCode(t, err, connect.CodeNotFound, "menu not found: 4b8ad5bb-9c4c-4b0e-a1ad-74e07f1d7b8e")
}

func TestGetMenuHidesAMenuTheMemberMayNotView(t *testing.T) {
	f := newMenuFixture(t)
	created, _ := f.createMenu(t, "Main", customItem("home", "/"))

	got, err := f.service.GetMenu(f.ctx, menu.GetMenuQuery{ID: created.ID})
	require.NoError(t, err)
	require.Equal(t, created, got)

	_, err = f.service.GetMenu(f.ctx, menu.GetMenuQuery{ID: " "})
	requireCode(t, err, connect.CodeInvalidArgument, "id is required")
	_, err = f.service.GetMenu(f.ctx, menu.GetMenuQuery{ID: "4b8ad5bb-9c4c-4b0e-a1ad-74e07f1d7b8e"})
	requireCode(t, err, connect.CodeNotFound, "menu not found: 4b8ad5bb-9c4c-4b0e-a1ad-74e07f1d7b8e")
	f.checker.Deny(policyv1.Menu.View(created.ID))
	_, err = f.service.GetMenu(f.ctx, menu.GetMenuQuery{ID: created.ID})
	requireCode(t, err, connect.CodeNotFound, "menu not found: "+created.ID)
}

func TestListMenusPagesByNameAndOmitsAnUndecodableMenu(t *testing.T) {
	f := newMenuFixture(t)
	f.createMenu(t, "b-menu", customItem("home", "/"))
	f.createMenu(t, "a-menu")
	f.store.Seed(menu.Root{Name: "c-corrupt", SourceLocale: "en", ItemsJSON: []byte(`{"broken"`)}, [16]byte{1})

	page, err := f.service.ListMenus(f.ctx, menu.ListMenusQuery{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []string{"a-menu", "b-menu"}, []string{page.Menus[0].Name, page.Menus[1].Name})
	require.Equal(t, menu.MenuPage{Menus: page.Menus, Total: 3, Limit: 2, Offset: 0, HasMore: true}, page)

	page, err = f.service.ListMenus(f.ctx, menu.ListMenusQuery{Offset: 2})
	require.NoError(t, err)
	require.Empty(t, page.Menus, "the corrupt Menu is omitted")
	require.Equal(t, int32(100), page.Limit)

	f.checker.Deny(policyv1.Menu.List())
	_, err = f.service.ListMenus(f.ctx, menu.ListMenusQuery{})
	requireCode(t, err, connect.CodePermissionDenied, "")
}
