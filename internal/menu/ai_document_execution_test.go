package menu_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const aiTarget = "ko"

func menuMutation(expectedDocumentRevision string, expectedTargetRevision *string, operations ...menu.AIDocumentOperation) menu.AIDocumentMutationCompiler {
	return func(snapshot menu.AIDocumentSnapshot) (menu.AIDocumentApply, error) {
		var target *string
		if expectedTargetRevision != nil {
			target = stringPointer(*expectedTargetRevision)
		}
		return menu.AIDocumentApply{
			MenuID: snapshot.ID, Locale: snapshot.Locale,
			ExpectedDocumentRevision: expectedDocumentRevision, ExpectedTargetRevision: target,
			Operations: append([]menu.AIDocumentOperation(nil), operations...),
		}, nil
	}
}

func setName(name string) menu.AIDocumentOperation {
	return menu.AIDocumentOperation{Kind: menu.AIDocumentSetName, Value: menu.AIDocumentValue{Kind: menu.AIDocumentText, Text: name}}
}

func setText(itemID, field, value string) menu.AIDocumentOperation {
	return menu.AIDocumentOperation{
		Kind: menu.AIDocumentSetItemField, ItemID: itemID, Field: field,
		Value: menu.AIDocumentValue{Kind: menu.AIDocumentText, Text: value},
	}
}

// createAIMenu is the two-item Menu every AI document case starts from.
func (f *menuFixture) createAIMenu(t *testing.T) (string, string) {
	t.Helper()
	created, revision := f.createMenu(t, "Menu "+uuid.NewString()[:8], customItem("home", "/"), customItem("about", "/about"))
	return created.ID, revision
}

func (f *menuFixture) execute(menuID, locale string, action menu.AIDocumentMutationAction, mode menu.AIDocumentExecutionMode, compiler menu.AIDocumentMutationCompiler) (menu.AIDocumentApplyResult, error) {
	return f.service.ExecuteAIDocumentMutation(f.ctx, menuID, locale, action, mode, compiler)
}

func (f *menuFixture) lastAudit(t *testing.T) sharedtelemetry.AuditRecord {
	t.Helper()
	records := f.audit.Records()
	require.NotEmpty(t, records)
	return records[len(records)-1]
}

const aiRootItems = `[
	{"id":"home","label":"home","linkType":"custom","url":"/"},
	{"id":"about","label":"about","linkType":"custom","url":"/about"}
]`

func TestAIDocumentSourceNameApplyAdvancesTheDocumentAndSignals(t *testing.T) {
	f := newMenuFixture(t)
	menuID, before := f.createAIMenu(t)

	result, err := f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(before, nil, setName("Renamed")))

	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Nil(t, result.TargetRevision)
	require.NotEqual(t, before, result.DocumentRevision)
	require.Equal(t, result.DocumentRevision, f.revision(t, menuID))
	root, _ := f.store.Stored(menuID)
	require.Equal(t, "Renamed", root.Name)
	require.JSONEq(t, aiRootItems, string(root.ItemsJSON))
	require.Equal(t, []string{"name"}, f.lastAudit(t).ChangedFields)
	require.Equal(t, []menu.AIDocumentChange{{
		MenuID: menuID, Locale: translation.DefaultLocale, DocumentRevision: result.DocumentRevision,
		Changed: true, MemberID: f.memberID,
	}}, f.store.Signals())
	require.Equal(t, []mutation.Step{mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepOutbox, mutation.StepCommit}, f.lastSteps())
}

func TestAIDocumentValidateRollsEveryWriteBackAndReportsNoResult(t *testing.T) {
	f := newMenuFixture(t)
	menuID, before := f.createAIMenu(t)

	result, err := f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionValidate,
		menuMutation(before, nil, setName("Validated")))

	require.NoError(t, err)
	require.Equal(t, menu.AIDocumentApplyResult{}, result, "a dry run returns no unsaved revision")
	require.Equal(t, before, f.revision(t, menuID))
	root, _ := f.store.Stored(menuID)
	require.NotEqual(t, "Validated", root.Name)
	require.Len(t, f.audit.Records(), 1)
	require.Empty(t, f.store.Signals())
	require.Equal(t, mutation.StepRollback, f.lastSteps()[len(f.lastSteps())-1])
}

func TestAIDocumentSourceLabelsWriteTheSourceValuesOnly(t *testing.T) {
	f := newMenuFixture(t)
	menuID, before := f.createAIMenu(t)

	result, err := f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(before, nil, setText("about", "label", "About us")))
	require.NoError(t, err)
	require.True(t, result.Changed)
	root, _ := f.store.Stored(menuID)
	require.JSONEq(t, aiRootItems, string(root.ItemsJSON), "a source label never rewrites the root items")
	require.Equal(t, map[string]string{"about": "About us", "home": "home"}, f.labels(t, menuID, translation.DefaultLocale))
	require.Equal(t, []string{"items"}, f.lastAudit(t).ChangedFields)

	_, err = f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(result.DocumentRevision, nil, menu.AIDocumentOperation{Kind: menu.AIDocumentUnsetItemField, ItemID: "about", Field: "label"}))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"home": "home"}, f.labels(t, menuID, translation.DefaultLocale))

	current := f.revision(t, menuID)
	_, err = f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(current, nil, setText("home", "label", "")))
	require.NoError(t, err)
	require.Equal(t, map[string]string{"home": ""}, f.labels(t, menuID, translation.DefaultLocale), "an explicit empty label is stored")
}

func TestAIDocumentSourceFieldsRewriteTheRootItems(t *testing.T) {
	f := newMenuFixture(t)
	menuID, before := f.createAIMenu(t)

	result, err := f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(before, nil, setText("about", "url", "/about-us"), menu.AIDocumentOperation{
			Kind: menu.AIDocumentSetItemField, ItemID: "home", Field: "open_in_new_tab",
			Value: menu.AIDocumentValue{Kind: menu.AIDocumentBoolean, Boolean: true},
		}))

	require.NoError(t, err)
	require.True(t, result.Changed)
	root, _ := f.store.Stored(menuID)
	require.JSONEq(t, `[
		{"id":"home","label":"home","linkType":"custom","url":"/","openInNewTab":true},
		{"id":"about","label":"about","linkType":"custom","url":"/about-us"}
	]`, string(root.ItemsJSON))
	require.Equal(t, map[string]string{"about": "about", "home": "home"}, f.labels(t, menuID, translation.DefaultLocale))
	require.Equal(t, []string{"items"}, f.lastAudit(t).ChangedFields)
}

func TestAIDocumentStructuralOperationsRequireManageAndReconcileLabels(t *testing.T) {
	f := newMenuFixture(t)
	menuID, before := f.createAIMenu(t)
	insert := []menu.AIDocumentOperation{
		{Kind: menu.AIDocumentInsertItem, ItemID: "contact", ParentID: menuID, AfterID: "home"},
		setText("contact", "link_type", "page"),
		setText("contact", "target_slug", "contact"),
	}

	_, err := f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply, menuMutation(before, nil, insert...))
	requireCode(t, err, connect.CodeInvalidArgument, "mutation: compiled Menu operations must match the authorized mutation action")

	inserted, err := f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationManage, menu.AIDocumentExecutionApply, menuMutation(before, nil, insert...))
	require.NoError(t, err)
	root, _ := f.store.Stored(menuID)
	require.JSONEq(t, `[
		{"id":"home","label":"home","linkType":"custom","url":"/"},
		{"id":"contact","label":"","linkType":"page","targetSlug":"contact"},
		{"id":"about","label":"about","linkType":"custom","url":"/about"}
	]`, string(root.ItemsJSON))
	locked := f.store.LockedTargets()
	require.Equal(t, []menu.TargetReference{{LinkType: menu.LinkPage, Slug: "contact"}}, locked[len(locked)-1], "a source change locks its targets")

	moved, err := f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationManage, menu.AIDocumentExecutionApply,
		menuMutation(inserted.DocumentRevision, nil, menu.AIDocumentOperation{Kind: menu.AIDocumentMoveItem, ItemID: "contact", ParentID: "home"}))
	require.NoError(t, err)
	_, err = f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationManage, menu.AIDocumentExecutionApply,
		menuMutation(moved.DocumentRevision, nil, menu.AIDocumentOperation{Kind: menu.AIDocumentDeleteItem, ItemID: "about"}))
	require.NoError(t, err)
	root, _ = f.store.Stored(menuID)
	require.JSONEq(t, `[{"id":"home","label":"home","linkType":"custom","url":"/","children":[
		{"id":"contact","label":"","linkType":"page","targetSlug":"contact"}
	]}]`, string(root.ItemsJSON))
	require.Equal(t, map[string]string{"home": "home"}, f.labels(t, menuID, translation.DefaultLocale), "deleting an item reconciles the source values row")
	require.Len(t, f.audit.Records(), 4)

	f.store.MissTarget(menu.TargetReference{LinkType: menu.LinkPage, Slug: "gone"})
	_, err = f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(f.revision(t, menuID), nil, setText("contact", "target_slug", "gone")))
	requireCode(t, err, connect.CodeInvalidArgument, "items: page target does not exist")
}

func TestAIDocumentTargetLocaleLifecycleKeepsTheDocumentRevision(t *testing.T) {
	f := newMenuFixture(t)
	menuID, before := f.createAIMenu(t)

	created, err := f.execute(menuID, aiTarget, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(before, nil, menu.AIDocumentOperation{Kind: menu.AIDocumentCreateTranslation}))
	require.NoError(t, err)
	require.Equal(t, before, created.DocumentRevision)
	require.NotNil(t, created.TargetRevision)
	require.Equal(t, map[string]string{"about": "about", "home": "home"}, f.labels(t, menuID, aiTarget))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, f.lastAudit(t).ItemOperation)
	require.Equal(t, aiTarget, f.lastAudit(t).Locale)

	validated, err := f.execute(menuID, aiTarget, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionValidate,
		menuMutation(before, created.TargetRevision, setText("home", "label", "홈")))
	require.NoError(t, err)
	require.Equal(t, menu.AIDocumentApplyResult{}, validated)
	require.Equal(t, "home", f.labels(t, menuID, aiTarget)["home"])

	updated, err := f.execute(menuID, aiTarget, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(before, created.TargetRevision, setText("home", "label", "홈")))
	require.NoError(t, err)
	require.True(t, updated.Changed)
	require.Equal(t, before, updated.DocumentRevision)
	require.NotEqual(t, *created.TargetRevision, *updated.TargetRevision)
	require.Equal(t, map[string]string{"about": "about", "home": "홈"}, f.labels(t, menuID, aiTarget))
	require.Equal(t, map[string]string{"about": "about", "home": "home"}, f.labels(t, menuID, translation.DefaultLocale))
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, f.lastAudit(t).ItemOperation)
	signals := f.store.Signals()
	require.Equal(t, menu.AIDocumentChange{
		MenuID: menuID, Locale: aiTarget, DocumentRevision: before, TargetRevision: updated.TargetRevision,
		Changed: true, MemberID: f.memberID,
	}, signals[len(signals)-1])

	noChange, err := f.execute(menuID, aiTarget, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(before, updated.TargetRevision, setText("home", "label", "홈")))
	require.NoError(t, err)
	require.False(t, noChange.Changed)
	require.Equal(t, updated.TargetRevision, noChange.TargetRevision)
	require.Len(t, f.audit.Records(), 3, "a semantic no-op appends no audit")

	deleted, err := f.execute(menuID, aiTarget, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(before, updated.TargetRevision, menu.AIDocumentOperation{Kind: menu.AIDocumentDeleteTranslation}))
	require.NoError(t, err)
	require.True(t, deleted.Changed)
	require.Nil(t, deleted.TargetRevision)
	require.Nil(t, f.labels(t, menuID, aiTarget))
	require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, f.lastAudit(t).ItemOperation)
	signals = f.store.Signals()
	require.Equal(t, menu.AIDocumentChange{MenuID: menuID, Locale: aiTarget, DocumentRevision: before, Changed: true, Deleted: true, MemberID: f.memberID}, signals[len(signals)-1])
}

func TestAIDocumentTargetLabelSeedsAnAbsentLocaleFromTheSourceValues(t *testing.T) {
	f := newMenuFixture(t)
	menuID, before := f.createAIMenu(t)
	emptied, err := f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(before, nil, setText("about", "label", "")))
	require.NoError(t, err)

	missing, err := f.service.LoadAIDocument(f.ctx, menuID, aiTarget)
	require.NoError(t, err)
	require.False(t, missing.LocaleExists)
	require.Nil(t, missing.TargetRevision)
	created, err := f.execute(menuID, aiTarget, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(emptied.DocumentRevision, nil, setText("home", "label", "홈")))
	require.NoError(t, err)
	require.NotNil(t, created.TargetRevision)
	require.Equal(t, map[string]string{"about": "", "home": "홈"}, f.labels(t, menuID, aiTarget))
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, f.lastAudit(t).ItemOperation)

	loaded, err := f.service.LoadAIDocument(f.ctx, menuID, aiTarget)
	require.NoError(t, err)
	require.True(t, loaded.LocaleExists)
	require.Equal(t, created.TargetRevision, loaded.TargetRevision)
	require.Equal(t, map[string]string{"about": "", "home": "홈"}, loaded.Labels)
}

func TestAIDocumentSemanticNoOpReturnsCurrentRevisionsWithoutAuditOrSignal(t *testing.T) {
	f := newMenuFixture(t)
	menuID, before := f.createAIMenu(t)
	root, _ := f.store.Stored(menuID)

	result, err := f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(before, nil, menu.AIDocumentOperation{Kind: menu.AIDocumentNoop}, setName(root.Name), setText("home", "label", "home"), setText("home", "url", "/")))

	require.NoError(t, err)
	require.Equal(t, menu.AIDocumentApplyResult{DocumentRevision: before}, result)
	require.Len(t, f.audit.Records(), 1)
	require.Equal(t, []menu.AIDocumentChange{{MenuID: menuID, Locale: translation.DefaultLocale, DocumentRevision: before, MemberID: f.memberID}}, f.store.Signals(),
		"the store schedules no signal for an unchanged result")
}

func TestAIDocumentRevisionConflictsReportTheCurrentRevisions(t *testing.T) {
	f := newMenuFixture(t)
	menuID, before := f.createAIMenu(t)
	handles := []string{"menu:" + menuID + "/name"}
	stale := menuMutation(uuid.NewString(), nil, setName("Stale"))
	withHandles := func(snapshot menu.AIDocumentSnapshot) (menu.AIDocumentApply, error) {
		command, err := stale(snapshot)
		command.AffectedHandles = handles
		return command, err
	}

	_, err := f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply, withHandles)
	var conflict *aidocument.RevisionConflict
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, &aidocument.RevisionConflict{
		CurrentDocumentRevision: before,
		AffectedHandles:         handles,
		Kind:                    aidocument.ConflictDocumentRevision,
		Message:                 "Menu AI document revision conflict",
	}, conflict)
	require.EqualError(t, err, "Menu AI document revision conflict")

	targetRevision := uuid.NewString()
	_, err = f.execute(menuID, translation.DefaultLocale, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
		menuMutation(before, &targetRevision, setName("Stale")))
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, &aidocument.RevisionConflict{
		Kind:                    aidocument.ConflictTargetRevision,
		CurrentDocumentRevision: before,
		Message:                 "Menu AI document revision conflict",
	}, conflict)
	require.Len(t, f.audit.Records(), 1)
}

func TestAIDocumentCompiledValidationIssuesNameTheOperation(t *testing.T) {
	f := newMenuFixture(t)
	menuID, before := f.createAIMenu(t)
	source := translation.DefaultLocale
	for _, tc := range []struct {
		name      string
		locale    string
		action    menu.AIDocumentMutationAction
		operation menu.AIDocumentOperation
		want      menu.AIDocumentIssue
	}{
		{"missing item", source, menu.AIDocumentMutationEdit, setText("missing", "label", "Missing"),
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueInvalidOperation, Handle: "item:missing", Message: "Menu item does not exist"}},
		{"name requires text", source, menu.AIDocumentMutationEdit,
			menu.AIDocumentOperation{Kind: menu.AIDocumentSetName, Value: menu.AIDocumentValue{Kind: menu.AIDocumentBoolean, Boolean: true}},
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueInvalidOperation, Handle: "menu:" + menuID + "/name", Message: "Menu name requires text"}},
		{"label too long", source, menu.AIDocumentMutationEdit, setText("home", "label", strings.Repeat("x", 256)),
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueInvalidOperation, Handle: "item:home/label", Message: "Menu item label must be at most 255 characters"}},
		{"unsupported source field", source, menu.AIDocumentMutationEdit, setText("home", "color", "red"),
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueInvalidOperation, Handle: "item:home/color", Message: `unsupported Menu item field "color"`}},
		{"invalid visibility mode", source, menu.AIDocumentMutationEdit, setText("home", "visibility_mode", "secret"),
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueInvalidOperation, Handle: "menu:" + menuID + "/items", Message: `menu item "home" has an unsupported visibility mode`}},
		{"unknown link type", source, menu.AIDocumentMutationEdit, setText("home", "link_type", "bogus"),
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueInvalidOperation, Handle: "menu:" + menuID + "/items", Message: "item[0]: linkType must be specified"}},
		{"insert duplicate", source, menu.AIDocumentMutationManage, menu.AIDocumentOperation{Kind: menu.AIDocumentInsertItem, ItemID: "home", ParentID: menuID},
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueInvalidOperation, Handle: "item:home", Message: `menu item "home" already exists`}},
		{"move into own subtree", source, menu.AIDocumentMutationManage, menu.AIDocumentOperation{Kind: menu.AIDocumentMoveItem, ItemID: "home", ParentID: "home"},
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueInvalidOperation, Handle: "item:home", Message: "menu item move would create a cycle"}},
		{"create source translation", source, menu.AIDocumentMutationEdit, menu.AIDocumentOperation{Kind: menu.AIDocumentCreateTranslation},
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueInvalidOperation, Handle: "translation:" + source, Message: "translation create requires one absent non-source locale"}},
		{"delete absent translation", aiTarget, menu.AIDocumentMutationEdit, menu.AIDocumentOperation{Kind: menu.AIDocumentDeleteTranslation},
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueInvalidOperation, Handle: "translation:" + aiTarget, Message: "translation delete requires one existing non-source locale"}},
		{"target name", aiTarget, menu.AIDocumentMutationEdit, setName("Target name"),
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueTargetForbidden, Handle: "menu:" + menuID, Message: "only the source locale may change the Menu name"}},
		{"target structural field", aiTarget, menu.AIDocumentMutationEdit, setText("home", "url", "/target"),
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueTargetForbidden, Handle: "item:home/url", Message: "non-source locales may change only translated labels"}},
		{"target insert", aiTarget, menu.AIDocumentMutationManage, menu.AIDocumentOperation{Kind: menu.AIDocumentInsertItem, ItemID: "target", ParentID: menuID},
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueTargetForbidden, Handle: "item:target", Message: "only the source locale may insert Menu items"}},
		{"target delete", aiTarget, menu.AIDocumentMutationManage, menu.AIDocumentOperation{Kind: menu.AIDocumentDeleteItem, ItemID: "home"},
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueTargetForbidden, Handle: "item:home", Message: "only the source locale may delete Menu items"}},
		{"target move", aiTarget, menu.AIDocumentMutationManage, menu.AIDocumentOperation{Kind: menu.AIDocumentMoveItem, ItemID: "home", ParentID: menuID},
			menu.AIDocumentIssue{Code: menu.AIDocumentIssueTargetForbidden, Handle: "item:home", Message: "only the source locale may move Menu items"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.execute(menuID, tc.locale, tc.action, menu.AIDocumentExecutionApply, menuMutation(before, nil, tc.operation))
			var invalid *menu.AIDocumentValidationError
			require.ErrorAs(t, err, &invalid)
			require.EqualError(t, err, "Menu AI document operations are invalid")
			require.Equal(t, []menu.AIDocumentIssue{tc.want}, invalid.Issues)
		})
	}
	require.Equal(t, before, f.revision(t, menuID))
	root, _ := f.store.Stored(menuID)
	require.JSONEq(t, aiRootItems, string(root.ItemsJSON))
	require.Nil(t, f.labels(t, menuID, aiTarget))
	require.Len(t, f.audit.Records(), 1)
}

func TestAIDocumentRequestRejections(t *testing.T) {
	f := newMenuFixture(t)
	menuID, before := f.createAIMenu(t)
	outsider, outsiderIdentity, _ := memberContext(t)
	f.checker.For(outsiderIdentity).Deny(policyv1.Menu.Edit(menuID))
	inactive, inactiveIdentity, _ := memberContext(t)
	f.fence.Deactivate(inactiveIdentity)
	compilerFailure := errors.New("compiler rejected the snapshot")
	missingID := uuid.NewString()
	source := translation.DefaultLocale
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		menuID   string
		action   menu.AIDocumentMutationAction
		mode     menu.AIDocumentExecutionMode
		compiler menu.AIDocumentMutationCompiler
		code     connect.Code
		message  string
		cause    error
	}{
		{"blank menu", f.ctx, " ", menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply, menuMutation(before, nil), connect.CodeInvalidArgument, "document: Menu ID and locale are required", nil},
		{"unsupported action", f.ctx, menuID, 0, menu.AIDocumentExecutionApply, menuMutation(before, nil), connect.CodeInvalidArgument, "action: is not supported", nil},
		{"unsupported mode", f.ctx, menuID, menu.AIDocumentMutationEdit, 9, menuMutation(before, nil), connect.CodeInvalidArgument, "mode: is not supported", nil},
		{"missing compiler", f.ctx, menuID, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply, nil, connect.CodeUnavailable, "Menu AI document compiler unavailable", nil},
		{"missing menu", f.ctx, missingID, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply, menuMutation(before, nil), connect.CodeNotFound, "menu not found: " + missingID, nil},
		{"member without permission sees not found", outsider, menuID, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply, menuMutation(before, nil, setName("Outsider")), connect.CodeNotFound, "menu not found: " + menuID, nil},
		{"inactive member sees not found", inactive, menuID, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply, menuMutation(before, nil, setName("Inactive")), connect.CodeNotFound, "menu not found: " + menuID, nil},
		{"compiler error is returned unchanged", f.ctx, menuID, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
			func(menu.AIDocumentSnapshot) (menu.AIDocumentApply, error) {
				return menu.AIDocumentApply{}, compilerFailure
			}, 0, "", compilerFailure},
		{"compiled identity must match", f.ctx, menuID, menu.AIDocumentMutationEdit, menu.AIDocumentExecutionApply,
			func(snapshot menu.AIDocumentSnapshot) (menu.AIDocumentApply, error) {
				return menu.AIDocumentApply{MenuID: snapshot.ID, Locale: aiTarget, ExpectedDocumentRevision: before}, nil
			}, connect.CodeInvalidArgument, "mutation: compiled Menu identity and locale must match the locked state", nil},
		{"manage action with edit operations", f.ctx, menuID, menu.AIDocumentMutationManage, menu.AIDocumentExecutionApply, menuMutation(before, nil, setName("Manage")),
			connect.CodeInvalidArgument, "mutation: compiled Menu operations must match the authorized mutation action", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := f.service.ExecuteAIDocumentMutation(tc.ctx, tc.menuID, source, tc.action, tc.mode, tc.compiler)
			require.Equal(t, menu.AIDocumentApplyResult{}, result)
			if tc.cause != nil {
				require.Same(t, tc.cause, err)
				return
			}
			requireCode(t, err, tc.code, tc.message)
		})
	}
	require.Equal(t, before, f.revision(t, menuID))
	require.Len(t, f.audit.Records(), 1)
}

func TestLoadAIDocumentProjectsValuesOnlyAndDeniesOnlyAnExistingMenu(t *testing.T) {
	f := newMenuFixture(t)
	root := f.store.Seed(menu.Root{
		Name: "Main", SourceLocale: "en",
		ItemsJSON: []byte(`[{"id":"home","label":"루트 한글","linkType":"custom","url":"/"},{"id":"about","label":"루트 소개","linkType":"custom","url":"/about"}]`),
	}, uuid.New(),
		menu.LocaleValues{Locale: "en", Labels: map[string]string{"home": ""}},
		menu.LocaleValues{Locale: "ko", Labels: map[string]string{"home": "홈"}},
	)

	source, err := f.service.LoadAIDocument(f.ctx, root.ID, "en")
	require.NoError(t, err)
	require.True(t, source.SourceValuesStored)
	require.Equal(t, map[string]string{"home": ""}, source.Labels, "a missing source value never borrows the root label")
	require.Equal(t, "루트 소개", source.Items[1].Label)
	target, err := f.service.LoadAIDocument(f.ctx, root.ID, "ko")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"home": "홈"}, target.Labels)
	require.NotNil(t, target.TargetRevision)

	outsider, outsiderIdentity, _ := memberContext(t)
	f.checker.For(outsiderIdentity).Deny(policyv1.Menu.View(root.ID))
	_, err = f.service.LoadAIDocument(outsider, root.ID, "en")
	requireCode(t, err, connect.CodePermissionDenied, "no permission to view this menu")
	missing := uuid.NewString()
	f.checker.For(outsiderIdentity).Deny(policyv1.Menu.View(missing))
	_, err = f.service.LoadAIDocument(outsider, missing, "en")
	requireCode(t, err, connect.CodeNotFound, "menu not found: "+missing)

	unseeded := f.store.Seed(menu.Root{Name: "Unseeded", SourceLocale: "en", ItemsJSON: []byte(`[]`)}, uuid.New())
	_, err = f.service.LoadAIDocument(f.ctx, unseeded.ID, "en")
	requireCode(t, err, connect.CodeFailedPrecondition, "Menu source locale values are not initialized")
}
