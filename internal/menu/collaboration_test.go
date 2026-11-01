package menu_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/menu/menutest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// seedRoom is a Menu with an English source and an existing Korean target.
func (f *menuFixture) seedRoom(t *testing.T, itemID string) menu.Root {
	t.Helper()
	return f.store.Seed(menu.Root{
		Name: "Main", SourceLocale: "en",
		ItemsJSON: []byte(`[{"id":"` + itemID + `","label":"legacy","linkType":"custom","url":"/posts"}]`),
	}, uuid.New(),
		menu.LocaleValues{Locale: "en", Labels: map[string]string{itemID: "Posts"}},
		menu.LocaleValues{Locale: "ko", Labels: map[string]string{}},
	)
}

func roomSave(snapshot menu.AIDocumentSnapshot, contributor string) menu.SaveMenuDocumentCommand {
	return menu.SaveMenuDocumentCommand{
		MenuID: snapshot.ID, Locale: snapshot.Locale, Name: snapshot.Name, Items: snapshot.Items,
		RequestedLabels: snapshot.Labels, ContributorMemberIDs: []string{contributor},
		ExpectedDocumentRevision: snapshot.DocumentRevision, ExpectedTargetRevision: snapshot.TargetRevision,
	}
}

func TestCollaborationSavesPersistTheExactTargetAndSourceRooms(t *testing.T) {
	f := newMenuFixture(t)
	itemID := uuid.NewString()
	root := f.seedRoom(t, itemID)
	contributor := uuid.NewString()

	target, err := f.service.LoadMenuDocument(t.Context(), menu.LoadMenuDocumentQuery{MenuID: root.ID, Locale: "ko"})
	require.NoError(t, err)
	require.True(t, target.LocaleExists)
	require.NotNil(t, target.TargetRevision)
	save := roomSave(target, contributor)
	save.RequestedLabels = map[string]string{itemID: "게시물"}
	targetSave, err := f.service.SaveMenuDocument(t.Context(), save)
	require.NoError(t, err)
	require.Equal(t, target.DocumentRevision, targetSave.DocumentRevision)
	require.NotEqual(t, target.TargetRevision, targetSave.TargetRevision)
	require.Equal(t, map[string]string{itemID: "게시물"}, f.labels(t, root.ID, "ko"))
	require.Equal(t, []menutest.ContributorCheck{{MenuID: root.ID, Contributors: []string{contributor}, Permission: menu.ContributorEdit}}, f.store.Contributors())
	require.Equal(t, []mutation.Step{mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit}, f.lastSteps())

	source, err := f.service.LoadMenuDocument(t.Context(), menu.LoadMenuDocumentQuery{MenuID: root.ID, Locale: "en"})
	require.NoError(t, err)
	newID := uuid.NewString()
	save = roomSave(source, contributor)
	save.Items = append(save.Items, menu.AIDocumentItem{ID: newID, LinkType: "custom", URL: stringPointer("/about")})
	save.RequestedLabels[newID] = "About"
	sourceSave, err := f.service.SaveMenuDocument(t.Context(), save)
	require.NoError(t, err)
	require.NotEqual(t, source.DocumentRevision, sourceSave.DocumentRevision)
	require.Equal(t, menu.ContributorManage, f.store.Contributors()[1].Permission, "a new item changes the topology")
	stored, _ := f.store.Stored(root.ID)
	require.JSONEq(t, `[
		{"id":"`+itemID+`","label":"legacy","linkType":"custom","url":"/posts"},
		{"id":"`+newID+`","label":"","linkType":"custom","url":"/about"}
	]`, string(stored.ItemsJSON), "the root keeps its labels; the room's labels are values")
	require.Equal(t, map[string]string{itemID: "Posts", newID: "About"}, f.labels(t, root.ID, "en"))

	records := f.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, "ko", records[0].Locale)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, records[0].ItemOperation)
	require.Equal(t, contributor, records[0].MemberID)
	require.Equal(t, []string{"items"}, records[1].ChangedFields)
}

func TestCollaborationSaveRejectsStaleRevisionsTargetStructureAndInactiveContributors(t *testing.T) {
	f := newMenuFixture(t)
	itemID := uuid.NewString()
	root := f.seedRoom(t, itemID)
	contributor := uuid.NewString()
	target, err := f.service.LoadMenuDocument(t.Context(), menu.LoadMenuDocumentQuery{MenuID: root.ID, Locale: "ko"})
	require.NoError(t, err)

	stale := roomSave(target, contributor)
	stale.ExpectedDocumentRevision = uuid.NewString()
	_, err = f.service.SaveMenuDocument(t.Context(), stale)
	requireCode(t, err, connect.CodeFailedPrecondition, "Menu document changed; reload before saving")

	staleTarget := roomSave(target, contributor)
	staleTarget.ExpectedTargetRevision = stringPointer(uuid.NewString())
	_, err = f.service.SaveMenuDocument(t.Context(), staleTarget)
	requireCode(t, err, connect.CodeFailedPrecondition, "Menu target changed; reload before saving")

	structure := roomSave(target, contributor)
	structure.Name = "Renamed in a target room"
	_, err = f.service.SaveMenuDocument(t.Context(), structure)
	requireCode(t, err, connect.CodeInvalidArgument, "Menu target room cannot change shared structure")

	foreign := roomSave(target, contributor)
	foreign.RequestedLabels = map[string]string{"unknown": "x"}
	_, err = f.service.SaveMenuDocument(t.Context(), foreign)
	requireCode(t, err, connect.CodeInvalidArgument, "requested_labels: contains a label not owned by this locale")

	unchanged, err := f.service.SaveMenuDocument(t.Context(), roomSave(target, contributor))
	require.NoError(t, err)
	require.Equal(t, menu.AIDocumentApplyResult{DocumentRevision: target.DocumentRevision, TargetRevision: target.TargetRevision}, unchanged)
	require.Empty(t, f.store.Contributors(), "an unchanged save authorizes no contributor")

	denied := errors.New("a collaboration contributor is no longer active")
	f.store.ContributorsErr = denied
	save := roomSave(target, contributor)
	save.RequestedLabels = map[string]string{itemID: "게시물"}
	_, err = f.service.SaveMenuDocument(t.Context(), save)
	require.ErrorIs(t, err, denied)
	require.Equal(t, map[string]string{}, f.labels(t, root.ID, "ko"))
	require.Empty(t, f.audit.Records())

	for _, invalid := range []menu.SaveMenuDocumentCommand{
		{MenuID: root.ID, Locale: " ko", ExpectedDocumentRevision: target.DocumentRevision, ContributorMemberIDs: []string{contributor}},
		{MenuID: "not-a-uuid", Locale: "ko", ExpectedDocumentRevision: target.DocumentRevision, ContributorMemberIDs: []string{contributor}},
		{MenuID: root.ID, Locale: "ko", ExpectedDocumentRevision: target.DocumentRevision, ContributorMemberIDs: []string{contributor, contributor}},
	} {
		_, err = f.service.SaveMenuDocument(t.Context(), invalid)
		requireCode(t, err, connect.CodeInvalidArgument, "")
	}
}
