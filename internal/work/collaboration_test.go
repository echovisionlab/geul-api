package work_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/echovisionlab/geul-api/internal/work/worktest"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// session admits p to a room through a stored session and returns its ID.
func (h *harness) session(p principal, change ...func(*auth.UserInfo)) string {
	user := p.user()
	for _, apply := range change {
		apply(user)
	}
	sessionID := uuid.NewString()
	h.store.PutSession(sessionID, user)
	return sessionID
}

// paragraphBatch adds one paragraph with text in locale at revision.
func paragraphBatch(revision uuid.UUID, contributor, locale, text string) *contentv1.RichTextBlockMutationBatch {
	blockID := uuid.NewString()
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		ExpectedRevision:        revision.String(),
		ContributorMemberIds:    []string{contributor},
		BaseMutations: []*contentv1.RichTextBlockMutation{{
			Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
				Node: &contentv1.RichTextBlockNode{
					Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{
						Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
					}},
					Placement: &contentv1.ContentBlockPlacement{Index: 0},
				},
			}},
		}},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: locale,
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{
				Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
					Block: &contentv1.RichTextBlockLocale{BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Paragraph{
						Paragraph: &contentv1.ParagraphBlockLocale{
							Props: &contentv1.ParagraphLocaleProps{},
							Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
								Text: &contentv1.RichTextStyledText{Text: text},
							}}},
						},
					}},
				}},
			}},
		}},
	}
}

func TestLoadWorkBlockDocumentOpensTheRoomOfAnEditorSession(t *testing.T) {
	h := newHarness(t)
	editor := newPrincipal()
	value := h.seedWork(t, "Source")
	h.store.PutLocale(value.ID, work.LocaleMetadata{Locale: "en", Title: stringPointer("Source"), Summary: stringPointer("Source summary")})
	h.store.PutLocale(value.ID, work.LocaleMetadata{Locale: "ko", Title: stringPointer("제목")})
	sessionID := h.session(editor)

	source, err := h.service.LoadWorkBlockDocument(collabContext(t), work.LoadWorkBlockDocumentQuery{WorkID: value.ID, Locale: "en", SessionID: sessionID})
	require.NoError(t, err)
	require.NotNil(t, source.Document)
	require.Equal(t, h.revision(t, value.ID).String(), source.DocumentRevision)
	require.Equal(t, work.RoomMetadata{Locale: "en", Title: stringPointer("Source"), Summary: stringPointer("Source summary")}, source.SourceMetadata)
	require.Equal(t, source.SourceMetadata, *source.LocaleMetadata)
	require.True(t, source.LocaleExists)
	require.Nil(t, source.TargetRevision)
	require.Equal(t, []string{sessionID}, h.store.LockedSessions())
	edit, err := policyv1.Work.Edit(value.ID)
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{authztest.Of(editor.identityID, edit, nil)}, h.checker.Checks())

	target, err := h.service.LoadWorkBlockDocument(collabContext(t), work.LoadWorkBlockDocumentQuery{WorkID: value.ID, Locale: "ko", SessionID: sessionID})
	require.NoError(t, err)
	require.True(t, target.LocaleExists)
	require.Equal(t, work.RoomMetadata{Locale: "ko", Title: stringPointer("제목"), Summary: stringPointer("Source summary")}, *target.LocaleMetadata,
		"a target without its own summary shows the source summary")
	require.NotNil(t, target.TargetRevision)

	absent, err := h.service.LoadWorkBlockDocument(collabContext(t), work.LoadWorkBlockDocumentQuery{WorkID: value.ID, Locale: "ja", SessionID: sessionID})
	require.NoError(t, err)
	require.False(t, absent.LocaleExists)
	require.Nil(t, absent.LocaleMetadata)
}

func TestLoadWorkBlockDocumentRefusesASessionThatMayNotEdit(t *testing.T) {
	editor := newPrincipal()
	for name, test := range map[string]struct {
		session func(h *harness) string
		archive bool
		code    connect.Code
	}{
		"no session":      {session: func(*harness) string { return "" }, code: connect.CodeUnauthenticated},
		"stale session":   {session: func(*harness) string { return uuid.NewString() }, code: connect.CodeUnauthenticated},
		"banned":          {session: func(h *harness) string { return h.session(editor, func(u *auth.UserInfo) { u.Banned = true }) }, code: connect.CodePermissionDenied},
		"not onboarded":   {session: func(h *harness) string { return h.session(editor, func(u *auth.UserInfo) { u.Onboarded = false }) }, code: connect.CodePermissionDenied},
		"inactive":        {session: func(h *harness) string { h.fence.Deactivate(editor.identityID); return h.session(editor) }, code: connect.CodePermissionDenied},
		"denied":          {session: func(h *harness) string { return h.session(editor) }, code: connect.CodeNotFound},
		"archived reader": {session: func(h *harness) string { return h.session(editor) }, archive: true, code: connect.CodeNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			value := h.seedWork(t, "Room", func(value *work.Work) {
				if test.archive {
					value.Status = work.StatusArchived
				}
			})
			edit, err := policyv1.Work.Edit(value.ID)
			require.NoError(t, err)
			viewArchived, err := policyv1.Work.ViewArchived(value.ID)
			require.NoError(t, err)
			if name == "denied" {
				h.checker.Deny(edit, nil)
			}
			h.checker.Deny(viewArchived, nil)

			_, err = h.service.LoadWorkBlockDocument(collabContext(t), work.LoadWorkBlockDocumentQuery{WorkID: value.ID, Locale: "en", SessionID: test.session(h)})
			requireCode(t, err, test.code, "")
			require.Empty(t, h.store.LockedSessions())
		})
	}
}

func TestApplyWorkBlockBatchSavesTheSourceRoomForItsContributors(t *testing.T) {
	h := newHarness(t)
	contributor := newPrincipal()
	h.store.PutMember(contributor.memberID, worktest.Member{Nickname: "Writer"})
	value := h.seedWork(t, "Room")
	revision := h.revision(t, value.ID)

	saved, err := h.service.ApplyWorkBlockBatch(collabContext(t), work.ApplyWorkBlockBatchCommand{
		WorkID: value.ID, Locale: "en", Batch: paragraphBatch(revision, contributor.memberID, "en", "Hello"),
	})

	require.NoError(t, err)
	require.True(t, saved.Content.Changed)
	require.True(t, saved.Content.TranslationSourceChanged)
	require.Equal(t, "en", saved.SourceLocale)
	require.NotEqual(t, revision, h.revision(t, value.ID))
	require.True(t, h.stored(t, value.ID).UpdatedAt.After(value.UpdatedAt) || h.stored(t, value.ID).UpdatedAt.Equal(value.UpdatedAt))
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditWorkUpdated, records[0].Action)
	require.Equal(t, contributor.memberID, records[0].MemberID, "the save is attributed to its contributor")
	require.Equal(t, []work.ContentUpdate{{WorkID: value.ID, Kind: work.LocaleUpdate, Locale: work.LocaleChange{
		Fields: []string{"content"}, DocumentRevision: saved.Content.DocumentRevision.String(), Contributors: []string{contributor.memberID},
		Source: work.SourceCollaboration, Locale: "en", DocumentStateChanged: true,
	}}}, h.store.Updates())
}

func TestApplyWorkBlockBatchRejectsARevokedRoomAStaleRevisionAndATargetRevision(t *testing.T) {
	h := newHarness(t)
	contributor := newPrincipal()
	h.store.PutMember(contributor.memberID, worktest.Member{Nickname: "Writer"})
	value := h.seedWork(t, "Room")
	revision := h.revision(t, value.ID)

	_, err := h.service.ApplyWorkBlockBatch(collabContext(t), work.ApplyWorkBlockBatchCommand{
		WorkID: value.ID, Locale: "en", Batch: paragraphBatch(uuid.New(), contributor.memberID, "en", "Stale"),
	})
	requireCode(t, err, connect.CodeFailedPrecondition, "work content revision changed; reload before saving")
	_, err = h.service.ApplyWorkBlockBatch(collabContext(t), work.ApplyWorkBlockBatchCommand{
		WorkID: value.ID, Locale: "en", Batch: paragraphBatch(revision, contributor.memberID, "en", "Target"), ExpectedTargetRevision: stringPointer("target"),
	})
	requireCode(t, err, connect.CodeInvalidArgument, "expected_target_revision: source Work room cannot carry a target revision")
	h.store.RevokeRoom(value.ID)
	_, err = h.service.ApplyWorkBlockBatch(collabContext(t), work.ApplyWorkBlockBatchCommand{
		WorkID: value.ID, Locale: "en", Batch: paragraphBatch(revision, contributor.memberID, "en", "Revoked"),
	})
	requireCode(t, err, connect.CodePermissionDenied, "")
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	require.Equal(t, revision, h.revision(t, value.ID))
	require.Empty(t, h.audit.Records())
	require.Empty(t, h.store.Updates())
}

func TestUpdateWorkLocaleMetadataSavesTheSourceTitleAndRequestsItsOG(t *testing.T) {
	h := newHarness(t)
	contributor := newPrincipal()
	h.store.PutMember(contributor.memberID, worktest.Member{Nickname: "Writer"})
	value := h.seedWork(t, "Source")
	revision := h.revision(t, value.ID)
	command := work.UpdateWorkLocaleMetadataCommand{
		WorkID: value.ID, Locale: "en", Title: stringPointer("  Retitled  "), ExpectedRevision: revision.String(),
		ContributorMemberIDs: []string{contributor.memberID},
	}

	saved, err := h.service.UpdateWorkLocaleMetadata(collabContext(t), command)

	require.NoError(t, err)
	require.True(t, saved.Content.Changed)
	require.True(t, saved.Content.TranslationSourceChanged)
	source, _ := h.store.Locale(value.ID, "en")
	require.Equal(t, "Retitled", *source.Title)
	require.Equal(t, []worktest.OGRequest{{WorkID: value.ID, Locale: "en", Reason: "work_locale_metadata_saved"}}, h.store.OGRequests())
	require.Equal(t, []string{"title"}, h.store.Updates()[0].Locale.Fields)

	command.ExpectedRevision, command.Title = saved.Content.DocumentRevision.String(), stringPointer(" Retitled ")
	unchanged, err := h.service.UpdateWorkLocaleMetadata(collabContext(t), command)
	require.NoError(t, err)
	require.False(t, unchanged.Content.Changed, "a padded copy of the title is no change")
	require.Len(t, h.store.Updates(), 1)

	command.Title = stringPointer(" ")
	_, err = h.service.UpdateWorkLocaleMetadata(collabContext(t), command)
	requireCode(t, err, connect.CodeInvalidArgument, "source_title: cannot be empty")
}

func TestUpdateWorkLocaleMetadataTrimsATargetTitleUnderItsRevision(t *testing.T) {
	h := newHarness(t)
	contributor := newPrincipal()
	h.store.PutMember(contributor.memberID, worktest.Member{Nickname: "Writer"})
	value := h.seedWork(t, "Source")
	h.store.PutLocale(value.ID, work.LocaleMetadata{Locale: "fr", Title: stringPointer("Ancien")})
	room, err := h.service.LoadWorkBlockDocument(collabContext(t), work.LoadWorkBlockDocumentQuery{WorkID: value.ID, Locale: "fr", SessionID: h.session(contributor)})
	require.NoError(t, err)
	update := func(title string, targetRevision *string) (work.RoomSave, error) {
		return h.service.UpdateWorkLocaleMetadata(collabContext(t), work.UpdateWorkLocaleMetadataCommand{
			WorkID: value.ID, Locale: "fr", Title: &title, ExpectedRevision: room.DocumentRevision,
			ExpectedTargetRevision: targetRevision, ContributorMemberIDs: []string{contributor.memberID},
		})
	}

	_, err = update("Nouveau", nil)
	requireCode(t, err, connect.CodeFailedPrecondition, "")
	saved, err := update("  Nouveau titre ", room.TargetRevision)
	require.NoError(t, err)
	require.True(t, saved.Content.Changed)
	require.NotNil(t, saved.TargetRevision)
	target, _ := h.store.Locale(value.ID, "fr")
	require.Equal(t, "Nouveau titre", *target.Title)
	require.Equal(t, room.DocumentRevision, saved.Content.DocumentRevision.String(), "a target save keeps the shared revision")
	require.Equal(t, []worktest.OGRequest{{WorkID: value.ID, Locale: "fr", Reason: "work_target_locale_metadata_saved"}}, h.store.OGRequests())
	require.Equal(t, work.LocaleChange{
		Fields: []string{"title"}, DocumentRevision: room.DocumentRevision, Contributors: []string{contributor.memberID},
		Source: work.SourceCollaboration, Locale: "fr", TargetRevision: saved.TargetRevision,
	}, h.store.Updates()[0].Locale)

	again, err := update(" Nouveau titre  ", saved.TargetRevision)
	require.NoError(t, err)
	require.False(t, again.Content.Changed, "a padded copy of the current title is not a change")
}
