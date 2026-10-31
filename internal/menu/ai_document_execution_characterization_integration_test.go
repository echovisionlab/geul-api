//go:build integration

package menu_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// These cases pin the observable outcome of every ExecuteAIDocumentMutation
// branch before the compiler is decomposed: the returned revisions, the root,
// values-only locale rows and Content Document revision, the Domain Audit
// record and the in-transaction content.updated signal.
func TestMenuAIDocumentExecutionCharacterizationIntegration(t *testing.T) {
	t.Parallel()
	fixture := newMenuAIDocumentExecutionFixture(t)
	source := translation.DefaultLocale
	target := "ko"
	if target == source {
		target = "en"
	}

	t.Run("source name apply advances document and signals", func(t *testing.T) {
		menuID, before := fixture.createMenu(t)
		events := &recordedMenuContentEvents{}
		result, err := fixture.service.domain.ExecuteAIDocumentMutation(
			events.context(fixture.ctx), menuID, source,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionApply,
			menuMutation(before, nil, menuSetName("Renamed "+menuID[:8])),
		)
		require.NoError(t, err)
		require.True(t, result.Changed)
		require.Nil(t, result.TargetRevision)
		require.NotEqual(t, before, result.DocumentRevision)
		state := fixture.state(t, menuID)
		require.Equal(t, result.DocumentRevision, state.revision)
		require.Equal(t, "Renamed "+menuID[:8], state.name)
		require.JSONEq(t, menuCharacterizationRootItems, state.items)
		require.JSONEq(t, `[{"id":"about","label":"about"},{"id":"home","label":"home"}]`, menuLocaleJSON(t, fixture.db, menuID, source))
		fixture.requireLastAudit(t, menuID, 2, `{"changed_fields":["name"]}`)
		events.requireOne(t, menuContentEventWant{
			menuID: menuID, locale: source, documentRevision: result.DocumentRevision,
			localeExists: true, documentStateChanged: true, memberID: fixture.memberID,
		})
	})

	t.Run("validate rolls every write back and reports no result", func(t *testing.T) {
		menuID, before := fixture.createMenu(t)
		events := &recordedMenuContentEvents{}
		result, err := fixture.service.domain.ExecuteAIDocumentMutation(
			events.context(fixture.ctx), menuID, source,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionValidate,
			menuMutation(before, nil, menuSetName("Validated "+menuID[:8])),
		)
		require.NoError(t, err)
		require.Equal(t, menudomain.AIDocumentApplyResult{}, result, "a dry run returns no unsaved revision")
		state := fixture.state(t, menuID)
		require.Equal(t, before, state.revision)
		require.Equal(t, "Menu "+menuID[:8], state.name)
		require.Len(t, menuAuditRows(t, fixture.db, menuID), 1)
		events.requireNone(t)
	})

	t.Run("source label writes source values only", func(t *testing.T) {
		menuID, before := fixture.createMenu(t)
		events := &recordedMenuContentEvents{}
		result, err := fixture.service.domain.ExecuteAIDocumentMutation(
			events.context(fixture.ctx), menuID, source,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionApply,
			menuMutation(before, nil, menuSetLabel("about", "About us")),
		)
		require.NoError(t, err)
		require.True(t, result.Changed)
		require.Nil(t, result.TargetRevision)
		state := fixture.state(t, menuID)
		require.Equal(t, result.DocumentRevision, state.revision)
		require.JSONEq(t, menuCharacterizationRootItems, state.items, "a source label never rewrites the root items")
		require.JSONEq(t, `[{"id":"about","label":"About us"},{"id":"home","label":"home"}]`, menuLocaleJSON(t, fixture.db, menuID, source))
		fixture.requireLastAudit(t, menuID, 2, `{"changed_fields":["items"]}`)
		events.requireOne(t, menuContentEventWant{
			menuID: menuID, locale: source, documentRevision: result.DocumentRevision,
			localeExists: true, documentStateChanged: true, memberID: fixture.memberID,
		})
	})

	t.Run("source unset label removes the stored value", func(t *testing.T) {
		menuID, before := fixture.createMenu(t)
		result, err := fixture.service.domain.ExecuteAIDocumentMutation(
			fixture.ctx, menuID, source,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionApply,
			menuMutation(before, nil, menudomain.AIDocumentOperation{
				Kind: menudomain.AIDocumentUnsetItemField, ItemID: "about", Field: "label",
			}),
		)
		require.NoError(t, err)
		require.True(t, result.Changed)
		require.JSONEq(t, `[{"id":"home","label":"home"}]`, menuLocaleJSON(t, fixture.db, menuID, source))
		fixture.requireLastAudit(t, menuID, 2, `{"changed_fields":["items"]}`)
	})

	t.Run("source item field rewrites root items", func(t *testing.T) {
		menuID, before := fixture.createMenu(t)
		result, err := fixture.service.domain.ExecuteAIDocumentMutation(
			fixture.ctx, menuID, source,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionApply,
			menuMutation(before, nil,
				menuSetText("about", "url", "/about-us"),
				menudomain.AIDocumentOperation{
					Kind: menudomain.AIDocumentSetItemField, ItemID: "home", Field: "open_in_new_tab",
					Value: menudomain.AIDocumentValue{Kind: menudomain.AIDocumentBoolean, Boolean: true},
				},
			),
		)
		require.NoError(t, err)
		require.True(t, result.Changed)
		state := fixture.state(t, menuID)
		require.Equal(t, result.DocumentRevision, state.revision)
		require.JSONEq(t, `[
			{"id":"home","label":"home","linkType":"custom","url":"/","openInNewTab":true},
			{"id":"about","label":"about","linkType":"custom","url":"/about-us"}
		]`, state.items)
		require.JSONEq(t, `[{"id":"about","label":"about"},{"id":"home","label":"home"}]`, menuLocaleJSON(t, fixture.db, menuID, source))
		fixture.requireLastAudit(t, menuID, 2, `{"changed_fields":["items"]}`)
	})

	t.Run("structural operations require manage", func(t *testing.T) {
		menuID, before := fixture.createMenu(t)
		insert := []menudomain.AIDocumentOperation{
			{Kind: menudomain.AIDocumentInsertItem, ItemID: "contact", ParentID: menuID, AfterID: "home"},
			menuSetText("contact", "link_type", "custom"),
			menuSetText("contact", "url", "/contact"),
		}
		_, err := fixture.service.domain.ExecuteAIDocumentMutation(
			fixture.ctx, menuID, source,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionApply,
			menuMutation(before, nil, insert...),
		)
		requireMenuConnectError(t, err, connect.CodeInvalidArgument,
			"mutation: compiled Menu operations must match the authorized mutation action")

		inserted, err := fixture.service.domain.ExecuteAIDocumentMutation(
			fixture.ctx, menuID, source,
			menudomain.AIDocumentMutationManage, menudomain.AIDocumentExecutionApply,
			menuMutation(before, nil, insert...),
		)
		require.NoError(t, err)
		require.True(t, inserted.Changed)
		require.JSONEq(t, `[
			{"id":"home","label":"home","linkType":"custom","url":"/"},
			{"id":"contact","label":"","linkType":"custom","url":"/contact"},
			{"id":"about","label":"about","linkType":"custom","url":"/about"}
		]`, fixture.state(t, menuID).items)
		require.JSONEq(t, `[{"id":"about","label":"about"},{"id":"home","label":"home"}]`, menuLocaleJSON(t, fixture.db, menuID, source),
			"an inserted item has no source label until one is set")
		fixture.requireLastAudit(t, menuID, 2, `{"changed_fields":["items"]}`)

		moved, err := fixture.service.domain.ExecuteAIDocumentMutation(
			fixture.ctx, menuID, source,
			menudomain.AIDocumentMutationManage, menudomain.AIDocumentExecutionApply,
			menuMutation(inserted.DocumentRevision, nil, menudomain.AIDocumentOperation{
				Kind: menudomain.AIDocumentMoveItem, ItemID: "contact", ParentID: "home",
			}),
		)
		require.NoError(t, err)
		require.True(t, moved.Changed)
		require.JSONEq(t, `[
			{"id":"home","label":"home","linkType":"custom","url":"/","children":[
				{"id":"contact","label":"","linkType":"custom","url":"/contact"}
			]},
			{"id":"about","label":"about","linkType":"custom","url":"/about"}
		]`, fixture.state(t, menuID).items)
		fixture.requireLastAudit(t, menuID, 3, `{"changed_fields":["items"]}`)

		deleted, err := fixture.service.domain.ExecuteAIDocumentMutation(
			fixture.ctx, menuID, source,
			menudomain.AIDocumentMutationManage, menudomain.AIDocumentExecutionApply,
			menuMutation(moved.DocumentRevision, nil, menudomain.AIDocumentOperation{
				Kind: menudomain.AIDocumentDeleteItem, ItemID: "about",
			}),
		)
		require.NoError(t, err)
		require.True(t, deleted.Changed)
		state := fixture.state(t, menuID)
		require.Equal(t, deleted.DocumentRevision, state.revision)
		require.JSONEq(t, `[
			{"id":"home","label":"home","linkType":"custom","url":"/","children":[
				{"id":"contact","label":"","linkType":"custom","url":"/contact"}
			]}
		]`, state.items)
		require.JSONEq(t, `[{"id":"home","label":"home"}]`, menuLocaleJSON(t, fixture.db, menuID, source),
			"deleting an item reconciles the source values row")
		fixture.requireLastAudit(t, menuID, 4, `{"changed_fields":["items"]}`)
	})

	t.Run("target label update keeps the document revision", func(t *testing.T) {
		menuID, before := fixture.createMenu(t)
		created, err := fixture.service.domain.ExecuteAIDocumentMutation(
			fixture.ctx, menuID, target,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionApply,
			menuMutation(before, nil, menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentCreateTranslation}),
		)
		require.NoError(t, err)
		require.Equal(t, before, created.DocumentRevision)
		require.NotNil(t, created.TargetRevision)
		require.JSONEq(t, `[{"id":"about","label":"about"},{"id":"home","label":"home"}]`, menuLocaleJSON(t, fixture.db, menuID, target))
		fixture.requireLastAudit(t, menuID, 2, `{"changed_fields":["locale_content"],"locale":"`+target+`","item_operation":"created"}`)

		events := &recordedMenuContentEvents{}
		validated, err := fixture.service.domain.ExecuteAIDocumentMutation(
			events.context(fixture.ctx), menuID, target,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionValidate,
			menuMutation(before, created.TargetRevision, menuSetLabel("home", "홈")),
		)
		require.NoError(t, err)
		require.Equal(t, menudomain.AIDocumentApplyResult{}, validated, "a dry run returns no unsaved target revision")
		require.JSONEq(t, `[{"id":"about","label":"about"},{"id":"home","label":"home"}]`, menuLocaleJSON(t, fixture.db, menuID, target))
		events.requireNone(t)

		updated, err := fixture.service.domain.ExecuteAIDocumentMutation(
			events.context(fixture.ctx), menuID, target,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionApply,
			menuMutation(before, created.TargetRevision, menuSetLabel("home", "홈")),
		)
		require.NoError(t, err)
		require.True(t, updated.Changed)
		require.Equal(t, before, updated.DocumentRevision)
		require.NotNil(t, updated.TargetRevision)
		require.NotEqual(t, *created.TargetRevision, *updated.TargetRevision)
		require.Equal(t, before, fixture.state(t, menuID).revision)
		require.JSONEq(t, `[{"id":"about","label":"about"},{"id":"home","label":"홈"}]`, menuLocaleJSON(t, fixture.db, menuID, target))
		require.JSONEq(t, `[{"id":"about","label":"about"},{"id":"home","label":"home"}]`, menuLocaleJSON(t, fixture.db, menuID, source))
		fixture.requireLastAudit(t, menuID, 3, `{"changed_fields":["locale_content"],"locale":"`+target+`","item_operation":"updated"}`)
		events.requireOne(t, menuContentEventWant{
			menuID: menuID, locale: target, documentRevision: before, targetRevision: updated.TargetRevision,
			localeExists: true, memberID: fixture.memberID,
		})

		events = &recordedMenuContentEvents{}
		deleted, err := fixture.service.domain.ExecuteAIDocumentMutation(
			events.context(fixture.ctx), menuID, target,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionApply,
			menuMutation(before, updated.TargetRevision, menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentDeleteTranslation}),
		)
		require.NoError(t, err)
		require.True(t, deleted.Changed)
		require.Equal(t, before, deleted.DocumentRevision)
		require.Nil(t, deleted.TargetRevision)
		require.False(t, menuLocaleExists(t, fixture.db, menuID, target))
		fixture.requireLastAudit(t, menuID, 4, `{"changed_fields":["locale_content"],"locale":"`+target+`","item_operation":"deleted"}`)
		events.requireOne(t, menuContentEventWant{
			menuID: menuID, locale: target, documentRevision: before, memberID: fixture.memberID,
		})
	})

	t.Run("semantic no-op returns current revisions without audit or signal", func(t *testing.T) {
		menuID, before := fixture.createMenu(t)
		events := &recordedMenuContentEvents{}
		result, err := fixture.service.domain.ExecuteAIDocumentMutation(
			events.context(fixture.ctx), menuID, source,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionApply,
			menuMutation(before, nil,
				menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentNoop},
				menuSetName("Menu "+menuID[:8]),
				menuSetLabel("home", "home"),
				menuSetText("home", "url", "/"),
			),
		)
		require.NoError(t, err)
		require.Equal(t, menudomain.AIDocumentApplyResult{DocumentRevision: before}, result)
		require.Equal(t, before, fixture.state(t, menuID).revision)
		require.Len(t, menuAuditRows(t, fixture.db, menuID), 1)
		events.requireNone(t)
	})

	t.Run("revision conflicts", func(t *testing.T) {
		menuID, before := fixture.createMenu(t)
		stale := testutil.IntegrationUUID()
		_, err := fixture.service.domain.ExecuteAIDocumentMutation(
			fixture.ctx, menuID, source,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionApply,
			menuMutationWithHandles(stale, nil, []string{"menu:" + menuID + "/name"}, menuSetName("Stale")),
		)
		var conflict *menudomain.AIDocumentRevisionConflict
		require.ErrorAs(t, err, &conflict)
		require.Equal(t, &menudomain.AIDocumentRevisionConflict{
			CurrentDocumentRevision: before,
			AffectedHandles:         []string{"menu:" + menuID + "/name"},
		}, conflict)
		require.EqualError(t, err, "Menu AI document revision conflict")

		targetRevision := testutil.IntegrationUUID()
		_, err = fixture.service.domain.ExecuteAIDocumentMutation(
			fixture.ctx, menuID, source,
			menudomain.AIDocumentMutationEdit, menudomain.AIDocumentExecutionApply,
			menuMutation(before, &targetRevision, menuSetName("Stale")),
		)
		require.ErrorAs(t, err, &conflict)
		require.Equal(t, &menudomain.AIDocumentRevisionConflict{
			Target: true, CurrentDocumentRevision: before,
		}, conflict)
		require.Equal(t, "Menu "+menuID[:8], fixture.state(t, menuID).name)
		require.Len(t, menuAuditRows(t, fixture.db, menuID), 1)
	})

	t.Run("compiled validation issues", func(t *testing.T) {
		menuID, before := fixture.createMenu(t)
		longLabel := strings.Repeat("x", 256)
		cases := []struct {
			name      string
			locale    string
			action    menudomain.AIDocumentMutationAction
			operation menudomain.AIDocumentOperation
			want      menudomain.AIDocumentIssue
		}{
			{
				name: "missing item", locale: source, action: menudomain.AIDocumentMutationEdit,
				operation: menuSetLabel("missing", "Missing"),
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueInvalidOperation, Handle: "item:missing",
					Message: "Menu item does not exist",
				},
			},
			{
				name: "name requires text", locale: source, action: menudomain.AIDocumentMutationEdit,
				operation: menudomain.AIDocumentOperation{
					Kind:  menudomain.AIDocumentSetName,
					Value: menudomain.AIDocumentValue{Kind: menudomain.AIDocumentBoolean, Boolean: true},
				},
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueInvalidOperation, Handle: "menu:" + menuID + "/name",
					Message: "Menu name requires text",
				},
			},
			{
				name: "label too long", locale: source, action: menudomain.AIDocumentMutationEdit,
				operation: menuSetLabel("home", longLabel),
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueInvalidOperation, Handle: "item:home/label",
					Message: "Menu item label must be at most 255 characters",
				},
			},
			{
				name: "unsupported source field", locale: source, action: menudomain.AIDocumentMutationEdit,
				operation: menuSetText("home", "color", "red"),
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueInvalidOperation, Handle: "item:home/color",
					Message: `unsupported Menu item field "color"`,
				},
			},
			{
				name: "invalid visibility mode", locale: source, action: menudomain.AIDocumentMutationEdit,
				operation: menuSetText("home", "visibility_mode", "secret"),
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueInvalidOperation, Handle: "menu:" + menuID + "/items",
					Message: `menu item "home" has an unsupported visibility mode`,
				},
			},
			{
				name: "insert duplicate", locale: source, action: menudomain.AIDocumentMutationManage,
				operation: menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentInsertItem, ItemID: "home", ParentID: menuID},
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueInvalidOperation, Handle: "item:home",
					Message: `menu item "home" already exists`,
				},
			},
			{
				name: "move into own subtree", locale: source, action: menudomain.AIDocumentMutationManage,
				operation: menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentMoveItem, ItemID: "home", ParentID: "home"},
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueInvalidOperation, Handle: "item:home",
					Message: "menu item move would create a cycle",
				},
			},
			{
				name: "create source translation", locale: source, action: menudomain.AIDocumentMutationEdit,
				operation: menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentCreateTranslation},
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueInvalidOperation, Handle: "translation:" + source,
					Message: "translation create requires one absent non-source locale",
				},
			},
			{
				name: "delete absent translation", locale: target, action: menudomain.AIDocumentMutationEdit,
				operation: menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentDeleteTranslation},
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueInvalidOperation, Handle: "translation:" + target,
					Message: "translation delete requires one existing non-source locale",
				},
			},
			{
				name: "target name", locale: target, action: menudomain.AIDocumentMutationEdit,
				operation: menuSetName("Target name"),
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueTargetForbidden, Handle: "menu:" + menuID,
					Message: "only the source locale may change the Menu name",
				},
			},
			{
				name: "target structural field", locale: target, action: menudomain.AIDocumentMutationEdit,
				operation: menuSetText("home", "url", "/target"),
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueTargetForbidden, Handle: "item:home/url",
					Message: "non-source locales may change only translated labels",
				},
			},
			{
				name: "target insert", locale: target, action: menudomain.AIDocumentMutationManage,
				operation: menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentInsertItem, ItemID: "target", ParentID: menuID},
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueTargetForbidden, Handle: "item:target",
					Message: "only the source locale may insert Menu items",
				},
			},
			{
				name: "target delete", locale: target, action: menudomain.AIDocumentMutationManage,
				operation: menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentDeleteItem, ItemID: "home"},
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueTargetForbidden, Handle: "item:home",
					Message: "only the source locale may delete Menu items",
				},
			},
			{
				name: "target move", locale: target, action: menudomain.AIDocumentMutationManage,
				operation: menudomain.AIDocumentOperation{Kind: menudomain.AIDocumentMoveItem, ItemID: "home", ParentID: menuID},
				want: menudomain.AIDocumentIssue{
					Code: menudomain.AIDocumentIssueTargetForbidden, Handle: "item:home",
					Message: "only the source locale may move Menu items",
				},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := fixture.service.domain.ExecuteAIDocumentMutation(
					fixture.ctx, menuID, tc.locale, tc.action, menudomain.AIDocumentExecutionApply,
					menuMutation(before, nil, tc.operation),
				)
				var invalid *menudomain.AIDocumentValidationError
				require.ErrorAs(t, err, &invalid)
				require.EqualError(t, err, "Menu AI document operations are invalid")
				require.Equal(t, []menudomain.AIDocumentIssue{tc.want}, invalid.Issues)
			})
		}
		state := fixture.state(t, menuID)
		require.Equal(t, before, state.revision)
		require.JSONEq(t, menuCharacterizationRootItems, state.items)
		require.False(t, menuLocaleExists(t, fixture.db, menuID, target))
		require.Len(t, menuAuditRows(t, fixture.db, menuID), 1)
	})

	t.Run("request rejections", func(t *testing.T) {
		menuID, before := fixture.createMenu(t)
		compilerFailure := errors.New("compiler rejected the snapshot")
		missingID := testutil.IntegrationUUID()
		cases := []struct {
			name     string
			ctx      context.Context
			menuID   string
			locale   string
			action   menudomain.AIDocumentMutationAction
			mode     menudomain.AIDocumentExecutionMode
			compiler menudomain.AIDocumentMutationCompiler
			code     connect.Code
			message  string
			cause    error
		}{
			{
				name: "blank menu", ctx: fixture.ctx, menuID: " ", locale: source,
				action: menudomain.AIDocumentMutationEdit, mode: menudomain.AIDocumentExecutionApply,
				compiler: menuMutation(before, nil), code: connect.CodeInvalidArgument,
				message: "document: Menu ID and locale are required",
			},
			{
				name: "unsupported action", ctx: fixture.ctx, menuID: menuID, locale: source,
				action: 0, mode: menudomain.AIDocumentExecutionApply,
				compiler: menuMutation(before, nil), code: connect.CodeInvalidArgument,
				message: "action: is not supported",
			},
			{
				name: "unsupported mode", ctx: fixture.ctx, menuID: menuID, locale: source,
				action: menudomain.AIDocumentMutationEdit, mode: 9,
				compiler: menuMutation(before, nil), code: connect.CodeInvalidArgument,
				message: "mode: is not supported",
			},
			{
				name: "missing compiler", ctx: fixture.ctx, menuID: menuID, locale: source,
				action: menudomain.AIDocumentMutationEdit, mode: menudomain.AIDocumentExecutionApply,
				code: connect.CodeUnavailable, message: "Menu AI document compiler unavailable",
			},
			{
				name: "missing menu", ctx: fixture.ctx, menuID: missingID, locale: source,
				action: menudomain.AIDocumentMutationEdit, mode: menudomain.AIDocumentExecutionApply,
				compiler: menuMutation(before, nil), code: connect.CodeNotFound,
				message: "menu not found: " + missingID,
			},
			{
				name: "member without permission sees not found", ctx: fixture.outsiderCtx, menuID: menuID, locale: source,
				action: menudomain.AIDocumentMutationEdit, mode: menudomain.AIDocumentExecutionApply,
				compiler: menuMutation(before, nil, menuSetName("Outsider")), code: connect.CodeNotFound,
				message: "menu not found: " + menuID,
			},
			{
				name: "compiler error is returned unchanged", ctx: fixture.ctx, menuID: menuID, locale: source,
				action: menudomain.AIDocumentMutationEdit, mode: menudomain.AIDocumentExecutionApply,
				compiler: func(menudomain.AIDocumentSnapshot) (menudomain.AIDocumentApply, error) {
					return menudomain.AIDocumentApply{}, compilerFailure
				},
				cause: compilerFailure,
			},
			{
				name: "compiled identity must match", ctx: fixture.ctx, menuID: menuID, locale: source,
				action: menudomain.AIDocumentMutationEdit, mode: menudomain.AIDocumentExecutionApply,
				compiler: func(snapshot menudomain.AIDocumentSnapshot) (menudomain.AIDocumentApply, error) {
					return menudomain.AIDocumentApply{MenuID: snapshot.ID, Locale: target, ExpectedDocumentRevision: before}, nil
				},
				code:    connect.CodeInvalidArgument,
				message: "mutation: compiled Menu identity and locale must match the locked state",
			},
			{
				name: "manage action with edit operations", ctx: fixture.ctx, menuID: menuID, locale: source,
				action: menudomain.AIDocumentMutationManage, mode: menudomain.AIDocumentExecutionApply,
				compiler: menuMutation(before, nil, menuSetName("Manage")), code: connect.CodeInvalidArgument,
				message: "mutation: compiled Menu operations must match the authorized mutation action",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				result, err := fixture.service.domain.ExecuteAIDocumentMutation(
					tc.ctx, tc.menuID, tc.locale, tc.action, tc.mode, tc.compiler,
				)
				require.Equal(t, menudomain.AIDocumentApplyResult{}, result)
				if tc.cause != nil {
					require.Same(t, tc.cause, err)
					return
				}
				requireMenuConnectError(t, err, tc.code, tc.message)
			})
		}
		require.Equal(t, before, fixture.state(t, menuID).revision)
		require.Len(t, menuAuditRows(t, fixture.db, menuID), 1)
	})
}

// menuCharacterizationRootItems is the root item JSON every fixture Menu starts with.
const menuCharacterizationRootItems = `[
	{"id":"home","label":"home","linkType":"custom","url":"/"},
	{"id":"about","label":"about","linkType":"custom","url":"/about"}
]`

type menuAIDocumentExecutionFixture struct {
	db          *gorm.DB
	service     menuIntegrationService
	ctx         context.Context
	outsiderCtx context.Context
	memberID    string
}

func newMenuAIDocumentExecutionFixture(t *testing.T) menuAIDocumentExecutionFixture {
	t.Helper()
	db := testutil.NewIntegrationDB(t)
	spiceDB := testutil.IntegrationSpiceDB(t)
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Menu execution admin "+identityID[:8])
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	outsiderIdentityID := testutil.IntegrationUUID()
	outsiderMemberID := testutil.SeedPostIntegrationIdentity(t, db, outsiderIdentityID, "Menu execution outsider "+outsiderIdentityID[:8])
	return menuAIDocumentExecutionFixture{
		db:          db,
		service:     newMenuIntegrationService(db, apitelemetry.NewDurableWriter(db), spiceDB),
		ctx:         testutil.NewAuditContext(t, identityID, memberID),
		outsiderCtx: testutil.NewAuditContext(t, outsiderIdentityID, outsiderMemberID),
		memberID:    memberID,
	}
}

// createMenu stores a two-item Menu through the RPC and returns its ID and
// current Content Document revision.
func (f menuAIDocumentExecutionFixture) createMenu(t *testing.T) (string, string) {
	t.Helper()
	suffix := testutil.IntegrationUUID()[:8]
	created, err := f.service.CreateMenu(f.ctx, connect.NewRequest(&managev1.CreateMenuRequest{
		Name: "pending " + suffix,
		Items: []*managev1.MenuItem{
			menuAuditItems("home", "/")[0],
			menuAuditItems("about", "/about")[0],
		},
	}))
	require.NoError(t, err)
	menuID := created.Msg.Id
	// Names embed the Menu ID so each case can predict its unchanged name.
	require.NoError(t, f.db.Exec("UPDATE menu SET name = ? WHERE id = ?", "Menu "+menuID[:8], menuID).Error)
	_, revision := menuDocumentState(t, f.db, menuID)
	return menuID, revision
}

type menuCharacterizationState struct {
	name     string
	items    string
	revision string
}

func (f menuAIDocumentExecutionFixture) state(t *testing.T, menuID string) menuCharacterizationState {
	t.Helper()
	var row struct {
		Name  string `gorm:"column:name"`
		Items string `gorm:"column:items"`
	}
	require.NoError(t, f.db.Table("menu").Select("name, items::text AS items").Where("id = ?", menuID).Take(&row).Error)
	_, revision := menuDocumentState(t, f.db, menuID)
	return menuCharacterizationState{name: row.Name, items: row.Items, revision: revision}
}

func (f menuAIDocumentExecutionFixture) requireLastAudit(t *testing.T, menuID string, count int, attributes string) {
	t.Helper()
	rows := menuAuditRows(t, f.db, menuID)
	require.Len(t, rows, count)
	last := rows[len(rows)-1]
	require.Equal(t, string(sharedtelemetry.AuditMenuUpdated), last.Action)
	require.Equal(t, f.memberID, last.ActorMemberID)
	require.JSONEq(t, attributes, string(last.Attributes))
}

func menuMutationWithHandles(
	expectedDocumentRevision string,
	expectedTargetRevision *string,
	handles []string,
	operations ...menudomain.AIDocumentOperation,
) menudomain.AIDocumentMutationCompiler {
	compile := menuMutation(expectedDocumentRevision, expectedTargetRevision, operations...)
	return func(snapshot menudomain.AIDocumentSnapshot) (menudomain.AIDocumentApply, error) {
		command, err := compile(snapshot)
		command.AffectedHandles = handles
		return command, err
	}
}

func menuSetName(name string) menudomain.AIDocumentOperation {
	return menudomain.AIDocumentOperation{
		Kind: menudomain.AIDocumentSetName, Value: menudomain.AIDocumentValue{Kind: menudomain.AIDocumentText, Text: name},
	}
}

func menuSetLabel(itemID, label string) menudomain.AIDocumentOperation {
	return menuSetText(itemID, "label", label)
}

func menuSetText(itemID, field, value string) menudomain.AIDocumentOperation {
	return menudomain.AIDocumentOperation{
		Kind: menudomain.AIDocumentSetItemField, ItemID: itemID, Field: field,
		Value: menudomain.AIDocumentValue{Kind: menudomain.AIDocumentText, Text: value},
	}
}

// requireMenuConnectError asserts what the client sees once the boundary
// maps the use-case error.
func requireMenuConnectError(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	require.Error(t, err)
	mapped := errs.Wrap(err)
	require.Equal(t, code, mapped.Code())
	require.Equal(t, message, mapped.Message())
}

// recordedMenuContentEvents captures the content.updated signals a mutation
// schedules inside its transaction.
type recordedMenuContentEvents struct {
	mu     sync.Mutex
	events []*managev1.ContentUpdatedEvent
}

func (r *recordedMenuContentEvents) context(ctx context.Context) context.Context {
	return contentsignal.WithInteractivePublisher(ctx, r)
}

func (r *recordedMenuContentEvents) NotifyProtobufWithExecutor(
	_ context.Context,
	_ eventpkg.DBTX,
	signal string,
	message proto.Message,
) error {
	if signal != eventpkg.SignalContentUpdated {
		return errors.New("unexpected signal " + signal)
	}
	event, ok := message.(*managev1.ContentUpdatedEvent)
	if !ok {
		return errors.New("unexpected content.updated payload")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, proto.Clone(event).(*managev1.ContentUpdatedEvent))
	return nil
}

func (r *recordedMenuContentEvents) requireNone(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Empty(t, r.events)
}

type menuContentEventWant struct {
	menuID               string
	locale               string
	documentRevision     string
	targetRevision       *string
	localeExists         bool
	documentStateChanged bool
	memberID             string
}

func (r *recordedMenuContentEvents) requireOne(t *testing.T, want menuContentEventWant) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Len(t, r.events, 1)
	event := r.events[0]
	require.NotZero(t, event.GetTimestampMs())
	event.TimestampMs = 0
	expected := &managev1.ContentUpdatedEvent{
		EntityType:           managev1.ContentEntityType_CONTENT_ENTITY_TYPE_MENU,
		EntityId:             want.menuID,
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		DocumentRevision:     &want.documentRevision,
		TargetRevision:       want.targetRevision,
		Locale:               &want.locale,
		LocaleExists:         &want.localeExists,
		DocumentStateChanged: want.documentStateChanged,
		ContributorMemberIds: []string{want.memberID},
		ChangedFields: []*managev1.ContentUpdatedField{{
			Path: "document.content", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT,
		}},
	}
	require.True(t, proto.Equal(expected, event), "content.updated:\nwant %v\n got %v", expected, event)
}
