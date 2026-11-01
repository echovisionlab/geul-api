package page_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/page/pagetest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// room is a collaboration room of one Page locale, opened by an editor's
// session.
type room struct {
	h        *harness
	page     page.Page
	editor   principal
	session  string
	document page.RoomDocument
}

func openRoom(t *testing.T, h *harness, value page.Page, locale string) room {
	t.Helper()
	editor := h.member(newPrincipal())
	session := uuid.NewString()
	h.store.PutSession(session, editor.user())
	document, err := h.service.LoadPageBlockDocument(t.Context(), page.LoadPageBlockDocumentQuery{PageID: value.ID, Locale: locale, SessionID: session})
	require.NoError(t, err)
	return room{h: h, page: value, editor: editor, session: session, document: document}
}

func TestLoadPageBlockDocumentAuthorizesTheSessionAndLocksIt(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Room", func(value *page.Page) {
		value.DocumentLayout = page.DocumentLayout{ContentHeight: page.ContentHeightViewport, PageChrome: page.PlacementFlow, Footer: page.PlacementPinned}
	})
	opened := openRoom(t, h, stored, "en")
	require.True(t, h.store.LockedSession(opened.session))
	require.Equal(t, "en", opened.document.Locale)
	require.True(t, opened.document.LocaleExists)
	require.Nil(t, opened.document.TargetRevision, "the source room has no target revision")
	require.Equal(t, "Room", *opened.document.SourceMetadata.Title)
	require.Equal(t, stored.DocumentLayout, opened.document.DocumentLayout)
	require.Equal(t, h.revision(t, stored.ID).String(), opened.document.DocumentRevision)

	h.store.PutLocale(stored.ID, page.Locale{Locale: "ko", Title: text("방")})
	target := openRoom(t, h, stored, "ko")
	require.True(t, target.document.LocaleExists)
	require.NotNil(t, target.document.TargetRevision)
	require.Equal(t, "방", *target.document.LocaleMetadata.Title)
}

func TestLoadPageBlockDocumentRejectsSessionsThatMayNotEdit(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Room")
	query := func(session string) page.LoadPageBlockDocumentQuery {
		return page.LoadPageBlockDocumentQuery{PageID: stored.ID, Locale: "en", SessionID: session}
	}
	_, err := h.service.LoadPageBlockDocument(t.Context(), query(uuid.NewString()))
	requireCode(t, err, connect.CodeUnauthenticated, "")

	banned := h.member(newPrincipal())
	bannedUser := banned.user()
	bannedUser.Banned = true
	h.store.PutSession("banned", bannedUser)
	_, err = h.service.LoadPageBlockDocument(t.Context(), query("banned"))
	requireCode(t, err, connect.CodePermissionDenied, "account is banned")

	inactive := h.member(newPrincipal())
	h.store.PutSession("inactive", inactive.user())
	h.fence.Deactivate(inactive.identityID)
	_, err = h.service.LoadPageBlockDocument(t.Context(), query("inactive"))
	requireCode(t, err, connect.CodePermissionDenied, "no permission to edit this page")

	viewer := h.member(newPrincipal())
	h.store.PutSession("viewer", viewer.user())
	edit, err := policyv1.Page.Edit(stored.ID)
	require.NoError(t, err)
	h.checker.For(viewer.identityID).Deny(edit, nil)
	_, err = h.service.LoadPageBlockDocument(t.Context(), query("viewer"))
	requireCode(t, err, connect.CodePermissionDenied, "no permission to edit this page")
	require.False(t, h.store.LockedSession("viewer"))

	// The editor document answers the same denial as a missing Page.
	_, err = h.service.GetPageEditorDocument(auth.WithUser(t.Context(), func() *auth.UserInfo {
		user := viewer.user()
		user.SessionID = "viewer"
		return user
	}()), page.GetPageEditorDocumentQuery{PageID: stored.ID, Locale: "en"})
	requireCode(t, err, connect.CodeNotFound, "page not found: "+stored.ID)
}

func TestUpdatePageLocaleMetadataOfATargetKeepsTheSharedRevision(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Source")
	h.store.PutLocale(stored.ID, page.Locale{Locale: "ko", Title: text("이전")})
	target := openRoom(t, h, stored, "ko")
	revision := h.revision(t, stored.ID)
	command := page.UpdatePageLocaleMetadataCommand{
		PageID: stored.ID, Locale: "ko", ExpectedRevision: target.document.DocumentRevision,
		ExpectedTargetRevision: target.document.TargetRevision, Title: text("제목"),
		ContributorMemberIDs: []string{target.editor.memberID},
	}

	saved, err := h.service.UpdatePageLocaleMetadata(t.Context(), command)

	require.NoError(t, err)
	require.True(t, saved.Changed)
	require.False(t, saved.SourceChanged)
	require.Equal(t, []string{"ko"}, saved.ChangedLocales)
	require.NotEqual(t, *target.document.TargetRevision, *saved.TargetRevision)
	require.Equal(t, revision, h.revision(t, stored.ID), "a target write never advances the shared revision")
	locale, _ := h.store.StoredLocale(stored.ID, "ko")
	require.Equal(t, "제목", *locale.Title)
	require.Equal(t, []pagetest.OGRequest{{PageID: stored.ID, Locale: "ko", Reason: "page_locale_metadata_saved"}}, h.store.OGRequests())
	require.Equal(t, page.LocaleUpdateChange{
		Fields: []string{"title"}, DocumentRevision: revision.String(), Contributors: command.ContributorMemberIDs,
		Source: page.SourceCollaboration, Locale: "ko", TargetRevision: saved.TargetRevision,
	}, h.store.Updates()[0].Locale)
	require.Len(t, h.audit.Records(), 1)
	require.Equal(t, target.editor.memberID, h.audit.Records()[0].MemberID)

	// The revision the room loaded is stale now.
	_, err = h.service.UpdatePageLocaleMetadata(t.Context(), command)
	requireCode(t, err, connect.CodeFailedPrecondition, "")
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
}

func TestUpdatePageLocaleMetadataOfTheSourceAdvancesTheRevision(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Source")
	source := openRoom(t, h, stored, "en")
	command := page.UpdatePageLocaleMetadataCommand{
		PageID: stored.ID, Locale: "en", ExpectedRevision: source.document.DocumentRevision,
		Title: text("  New title  "), SetSummary: text("Summary"), ContributorMemberIDs: []string{source.editor.memberID},
	}

	saved, err := h.service.UpdatePageLocaleMetadata(t.Context(), command)

	require.NoError(t, err)
	require.True(t, saved.Changed)
	require.True(t, saved.SourceChanged)
	require.Nil(t, saved.TargetRevision)
	require.NotEqual(t, source.document.DocumentRevision, saved.DocumentRevision)
	locale, _ := h.store.StoredLocale(stored.ID, "en")
	require.Equal(t, "New title", *locale.Title, "a source title is trimmed")
	require.Equal(t, "Summary", *locale.Summary)
	require.Equal(t, page.LocaleUpdateChange{
		Fields: []string{"title", "summary"}, DocumentRevision: saved.DocumentRevision, Contributors: command.ContributorMemberIDs,
		Source: page.SourceCollaboration, Locale: "en", DocumentStateChanged: true,
	}, h.store.Updates()[0].Locale)

	for _, invalid := range []page.UpdatePageLocaleMetadataCommand{
		{PageID: stored.ID, Locale: "en", ExpectedRevision: saved.DocumentRevision, Title: text(" "), ContributorMemberIDs: command.ContributorMemberIDs},
		{PageID: stored.ID, Locale: "en", ExpectedRevision: saved.DocumentRevision, ExpectedTargetRevision: text("r"), Title: text("T"), ContributorMemberIDs: command.ContributorMemberIDs},
		{PageID: stored.ID, Locale: "en", ExpectedRevision: saved.DocumentRevision, ContributorMemberIDs: command.ContributorMemberIDs},
	} {
		_, err := h.service.UpdatePageLocaleMetadata(t.Context(), invalid)
		requireCode(t, err, connect.CodeInvalidArgument, "")
	}
}

func TestCollaborationSavesRequireEveryContributorToStillEdit(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Source")
	source := openRoom(t, h, stored, "en")
	save := func(contributors ...string) error {
		_, err := h.service.UpdatePageLocaleMetadata(t.Context(), page.UpdatePageLocaleMetadataCommand{
			PageID: stored.ID, Locale: "en", ExpectedRevision: source.document.DocumentRevision,
			Title: text("Changed"), ContributorMemberIDs: contributors,
		})
		return err
	}
	requireCode(t, save(uuid.NewString()), connect.CodeInvalidArgument, "contributor_member_ids: contains a Member that does not exist")

	departed := newPrincipal()
	h.store.PutMember(departed.memberID, "Departed", "")
	requireCode(t, save(departed.memberID), connect.CodePermissionDenied, "no permission to edit this page contributor")

	edit, err := policyv1.Page.Edit(stored.ID)
	require.NoError(t, err)
	h.checker.For(source.editor.identityID).Deny(edit, nil)
	requireCode(t, save(source.editor.memberID), connect.CodePermissionDenied, "no permission to edit this page contributor")
	require.Empty(t, h.audit.Records())
	locale, _ := h.store.StoredLocale(stored.ID, "en")
	require.Equal(t, "Source", *locale.Title)
}

func TestUpdatePageDocumentMetadataSavesTheLayoutFromTheSourceRoom(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Source")
	source := openRoom(t, h, stored, "en")
	layout := page.DocumentLayout{ContentHeight: page.ContentHeightViewport, PageChrome: page.PlacementPinned, Footer: page.PlacementFlow, HideTableOfContents: true}
	command := page.UpdatePageDocumentMetadataCommand{
		PageID: stored.ID, Locale: "en", ExpectedRevision: source.document.DocumentRevision,
		DocumentLayout: &layout, ContributorMemberIDs: []string{source.editor.memberID},
	}

	saved, err := h.service.UpdatePageDocumentMetadata(source.editor.context(t), command)

	require.NoError(t, err)
	require.True(t, saved.Changed)
	updated, _ := h.store.Page(stored.ID)
	require.Equal(t, layout, updated.DocumentLayout)
	require.Equal(t, []page.ContentUpdate{{PageID: stored.ID, Kind: page.LayoutUpdate, Locale: page.LocaleUpdateChange{
		DocumentRevision: saved.DocumentRevision, Contributors: command.ContributorMemberIDs,
	}}}, h.store.Updates())
	require.Equal(t, []string{"document_layout"}, h.audit.Records()[0].ChangedFields)

	command.ExpectedRevision = saved.DocumentRevision
	unchanged, err := h.service.UpdatePageDocumentMetadata(source.editor.context(t), command)
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Len(t, h.audit.Records(), 1)

	command.DocumentLayout = nil
	_, err = h.service.UpdatePageDocumentMetadata(source.editor.context(t), command)
	requireCode(t, err, connect.CodeInvalidArgument, "document_layout: document layout is required")
	command.DocumentLayout, command.Locale = &layout, "ko"
	_, err = h.service.UpdatePageDocumentMetadata(source.editor.context(t), command)
	requireCode(t, err, connect.CodeFailedPrecondition, "")
}

func TestCreatePageVersionCheckpointStoresOnlyANewSourceDocument(t *testing.T) {
	h := newHarness(t)
	stored := h.seedPage("Source")
	source := openRoom(t, h, stored, "en")
	command := page.CreatePageVersionCheckpointCommand{
		PageID: stored.ID, Locale: "en", ExpectedRevision: source.document.DocumentRevision,
		ContributorMemberIDs: []string{source.editor.memberID},
	}

	created, err := h.service.CreatePageVersionCheckpoint(collabContext(t), command)
	require.NoError(t, err)
	require.NotNil(t, created.VersionID)
	versions := h.store.Versions(stored.ID)
	require.Len(t, versions, 1)
	require.Equal(t, int32(1), versions[0].Version)
	require.Equal(t, []string{source.editor.memberID}, versions[0].ContributorMemberIDs)
	require.Equal(t, sharedtelemetry.ActorKindSystem, h.audit.Records()[0].Kind)
	require.Equal(t, created.VersionID, &h.audit.Records()[0].VersionID)
	require.Equal(t, []string{source.editor.memberID}, h.audit.Records()[0].ContributorMemberIDs)

	again, err := h.service.CreatePageVersionCheckpoint(collabContext(t), command)
	require.NoError(t, err)
	require.Nil(t, again.VersionID, "the same document is no new version")
	require.Len(t, h.store.Versions(stored.ID), 1)
	require.Len(t, h.audit.Records(), 1)

	command.ExpectedRevision = uuid.NewString()
	_, err = h.service.CreatePageVersionCheckpoint(collabContext(t), command)
	requireCode(t, err, connect.CodeFailedPrecondition, "")
}

func TestApplyPageBlockBatchValidatesItsBatchBeforeAnyRead(t *testing.T) {
	h := newHarness(t)
	_, err := h.service.ApplyPageBlockBatch(t.Context(), page.ApplyPageBlockBatchCommand{PageID: uuid.NewString(), Locale: "en"})
	requireCode(t, err, connect.CodeInvalidArgument, "batch is required")
	_, err = h.service.ApplyPageBlockBatch(t.Context(), page.ApplyPageBlockBatchCommand{PageID: "not-a-uuid", Locale: "en", Batch: nil})
	requireCode(t, err, connect.CodeInvalidArgument, "")
	require.Empty(t, h.units.Steps())
}

// collabContext is a request editor-collab makes as itself.
func collabContext(t *testing.T) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.SystemActor{ServiceName: "geul-collab"})
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(t.Context(), requestContext)
}
