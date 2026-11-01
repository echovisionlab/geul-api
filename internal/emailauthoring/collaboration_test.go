package emailauthoring_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emailauthoring/emailauthoringtest"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/emailcontent/emailcontenttest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// paragraphBatch upserts one paragraph in locale; a source batch also
// places the Block.
func paragraphBatch(revision uuid.UUID, locale, blockID, text string, source bool, contributors ...string) *contentv1.RichTextBlockMutationBatch {
	batch := &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
		ExpectedRevision:        revision.String(), ContributorMemberIds: contributors,
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{Locale: locale, Mutations: []*contentv1.RichTextBlockLocaleMutation{{
			Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{Block: &contentv1.RichTextBlockLocale{
				BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{}, Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}}}},
				}},
			}}},
		}}}},
	}
	if source {
		batch.BaseMutations = []*contentv1.RichTextBlockMutation{{Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{Node: &contentv1.RichTextBlockNode{
			Block:     &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}},
			Placement: &contentv1.ContentBlockPlacement{Index: 0},
		}}}}}
	}
	return batch
}

// templateRoom seeds a template with a Korean target row and a Member to
// credit.
func templateRoom(t *testing.T, h *harness) (emailauthoring.Template, uuid.UUID, string) {
	t.Helper()
	template := h.store.PutTemplate(emailauthoring.Template{Key: "room_" + uuid.NewString()[:6], Name: "Room"}, "Source subject")
	h.store.PutLocale(template.ID, emailcontenttest.Locale{LocaleMetadata: emailcontent.LocaleMetadata{Locale: "ko", Subject: ptr("대상 제목"), UpdatedAt: time.Now().UTC()}})
	document, _ := h.store.Document(uuid.MustParse(*template.ContentDocumentID))
	contributor := uuid.NewString()
	h.store.PutMember(contributor)
	return template, document.Revision, contributor
}

func TestApplyTemplateBlockBatchCreditsTheRoomContributorForTheSystemActor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template, revision, contributor := templateRoom(t, h)

	change, err := h.internal.ApplyTemplateBlockBatch(collabContext(t), emailauthoring.ApplyTemplateBlockBatchCommand{
		TemplateID: template.ID, Locale: "en", Batch: paragraphBatch(revision, "en", uuid.NewString(), "Hello", true, contributor),
	})

	require.NoError(t, err)
	require.True(t, change.Changed)
	require.True(t, change.SourceChanged)
	require.NotEqual(t, revision.String(), change.DocumentRevision)
	require.Len(t, h.audit.Records(), 1)
	record := h.audit.Records()[0]
	require.Equal(t, contributor, record.MemberID)
	require.Equal(t, sharedtelemetry.ActorKindMember, record.Kind)
	require.Equal(t, []string{"locale_content"}, record.ChangedFields)
	source, _ := h.store.Locale(template.ID, "en")
	require.NotNil(t, source.HTML, "a source change refreshes the source projection")
	target, _ := h.store.Locale(template.ID, "ko")
	require.NotNil(t, target.HTML, "a translation source change refreshes every target fallback")
	stored, _ := h.store.Template(template.ID)
	require.NotNil(t, stored.Variables)
}

func TestApplyTemplateBlockBatchRequiresOneCurrentExistingContributor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template, revision, contributor := templateRoom(t, h)
	blockID := uuid.NewString()

	_, err := h.internal.ApplyTemplateBlockBatch(collabContext(t), emailauthoring.ApplyTemplateBlockBatchCommand{
		TemplateID: template.ID, Locale: "en", Batch: paragraphBatch(revision, "en", blockID, "x", true, contributor, uuid.NewString()),
	})
	require.EqualError(t, wrapped(err), "invalid_argument: contributor_member_ids: Email authoring mutation requires exactly one origin Member")
	_, err = h.internal.ApplyTemplateBlockBatch(collabContext(t), emailauthoring.ApplyTemplateBlockBatchCommand{
		TemplateID: template.ID, Locale: "en", Batch: paragraphBatch(revision, "en", blockID, "x", true, uuid.NewString()),
	})
	require.EqualError(t, wrapped(err), "invalid_argument: contributor_member_ids: origin Member does not exist")
	h.store.StaleContributors = errDown
	_, err = h.internal.ApplyTemplateBlockBatch(collabContext(t), emailauthoring.ApplyTemplateBlockBatchCommand{
		TemplateID: template.ID, Locale: "en", Batch: paragraphBatch(revision, "en", blockID, "x", true, contributor),
	})
	require.ErrorIs(t, err, errDown)
	h.store.StaleContributors = nil
	h.store.Freeze(template.ID)
	_, err = h.internal.ApplyTemplateBlockBatch(collabContext(t), emailauthoring.ApplyTemplateBlockBatchCommand{
		TemplateID: template.ID, Locale: "en", Batch: paragraphBatch(revision, "en", blockID, "x", true, contributor),
	})
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	document, _ := h.store.Document(uuid.MustParse(*template.ContentDocumentID))
	require.Equal(t, revision, document.Revision)
	require.Empty(t, h.audit.Records())
}

func TestApplyTemplateBlockBatchWritesATargetRoomWithoutMovingTheSourceRevision(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template, revision, contributor := templateRoom(t, h)
	h.store.PutSession("session", newPrincipal(t).user())
	room, err := h.internal.LoadTemplateDocument(t.Context(), emailauthoring.LoadTemplateDocumentQuery{TemplateID: template.ID, Locale: "ko", SessionID: "session"})
	require.NoError(t, err)

	change, err := h.internal.ApplyTemplateBlockBatch(collabContext(t), emailauthoring.ApplyTemplateBlockBatchCommand{
		TemplateID: template.ID, Locale: "ko", ExpectedTargetRevision: room.TargetRevision,
		Batch: paragraphBatch(revision, "ko", uuid.NewString(), "번역", false, contributor),
	})

	require.NoError(t, err)
	require.True(t, change.Changed)
	require.Equal(t, revision.String(), change.DocumentRevision)
	require.NotNil(t, change.TargetRevision)
	require.Equal(t, contributor, h.audit.Records()[0].MemberID)
	require.Equal(t, "ko", h.audit.Records()[0].Locale)
}

func TestUpdateTemplateLocaleMetadataChangesTheSourceSubjectOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template, revision, contributor := templateRoom(t, h)
	subject := "New subject"
	command := emailauthoring.UpdateTemplateLocaleMetadataCommand{
		TemplateID: template.ID, Locale: "en", ExpectedRevision: revision.String(), Subject: &subject, Contributors: []string{contributor},
	}

	change, err := h.internal.UpdateTemplateLocaleMetadata(collabContext(t), command)
	require.NoError(t, err)
	require.True(t, change.Changed)
	require.True(t, change.SourceChanged)
	source, _ := h.store.Locale(template.ID, "en")
	require.Equal(t, "New subject", *source.Subject)
	require.Equal(t, contributor, h.audit.Records()[0].MemberID)

	command.ExpectedRevision = change.DocumentRevision
	unchanged, err := h.internal.UpdateTemplateLocaleMetadata(collabContext(t), command)
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Equal(t, change.DocumentRevision, unchanged.DocumentRevision)
	require.Len(t, h.audit.Records(), 1)

	_, err = h.internal.UpdateTemplateLocaleMetadata(collabContext(t), emailauthoring.UpdateTemplateLocaleMetadataCommand{
		TemplateID: template.ID, Locale: "en", ExpectedRevision: "not-a-uuid", Subject: &subject, Contributors: []string{contributor},
	})
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
}

func TestLoadTemplateDocumentAuthorizesTheRoomSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template, _, _ := templateRoom(t, h)
	editor := newPrincipal(t)
	h.store.PutSession("session", editor.user())

	_, err := h.internal.LoadTemplateDocument(t.Context(), emailauthoring.LoadTemplateDocumentQuery{TemplateID: template.ID, Locale: "ko", SessionID: "missing"})
	require.Equal(t, connect.CodeUnauthenticated, codeOf(err), "an invalid session needs a new login")
	h.store.Fail("ResolveSessionPrincipal", errDown)
	_, err = h.internal.LoadTemplateDocument(t.Context(), emailauthoring.LoadTemplateDocumentQuery{TemplateID: template.ID, Locale: "ko", SessionID: "session"})
	require.Equal(t, connect.CodeInternal, codeOf(err), "a failed lookup is not an invalid session")
	require.ErrorIs(t, err, errDown)
	h.store.Fail("ResolveSessionPrincipal", nil)

	document, err := h.internal.LoadTemplateDocument(t.Context(), emailauthoring.LoadTemplateDocumentQuery{TemplateID: template.ID, Locale: "ko", SessionID: "session"})
	require.NoError(t, err)
	require.Equal(t, "ko", document.Locale)
	require.NotNil(t, document.TargetRevision)
	require.Equal(t, "대상 제목", *document.State.TargetMetadata.Subject)
	require.Contains(t, h.checker.Checks(), checkOf(editor.identityID)(policyv1.EmailTemplate.Edit(template.ID)))

	h.store.Freeze(template.ID)
	_, err = h.internal.LoadTemplateDocument(t.Context(), emailauthoring.LoadTemplateDocumentQuery{TemplateID: template.ID, Locale: "ko", SessionID: "session"})
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
}

func TestLoadTemplateDocumentDeniesAnEditorWithoutEdit(t *testing.T) {
	t.Parallel()
	store, units := emailauthoringtest.New()
	internal, err := emailauthoring.NewInternalService(emailauthoring.InternalDeps{
		Units: units, SpiceDB: &authztest.Checker{}, Fence: &identitystatetest.Fence{}, Audit: newHarness(t).audit,
	})
	require.NoError(t, err)
	template := store.PutTemplate(emailauthoring.Template{Key: "room", Name: "Room"}, "S")
	store.PutSession("session", newPrincipal(t).user())

	_, err = internal.LoadTemplateDocument(t.Context(), emailauthoring.LoadTemplateDocumentQuery{TemplateID: template.ID, Locale: "en", SessionID: "session"})
	require.EqualError(t, wrapped(err), "permission_denied: no permission to edit this collaboration resource")
}

// layoutRoom seeds a layout with one source unit, a Korean target row and a
// Member to credit.
func layoutRoom(t *testing.T, h *harness) (emailauthoring.Layout, uuid.UUID, string) {
	t.Helper()
	layout := h.store.PutLayout(emailauthoring.Layout{Key: "frame_" + uuid.NewString()[:6], Name: "Frame"}, emailauthoringtest.LayoutSourceHTML)
	html, text, err := email.ApplyLayoutLocaleValues(emailauthoringtest.LayoutSourceHTML, map[string]string{layoutHandle: "안녕"})
	require.NoError(t, err)
	h.store.PutLayoutTranslation(layout.ID, "ko", email.LayoutTranslationEntry{LayoutTranslationDocument: email.LayoutTranslationDocument{ContentHTML: html, ContentText: text, UpdatedAt: time.Now().UTC()}})
	document, _ := h.store.Document(uuid.MustParse(layout.ContentDocumentID))
	contributor := uuid.NewString()
	h.store.PutMember(contributor)
	return layout, document.Revision, contributor
}

const layoutHandle = "unit:0d55f289-ff23-43fe-8547-a85109cdd449:text"

func TestSaveLayoutDocumentCreditsTheRoomContributorForTheSystemActor(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	layout, revision, contributor := layoutRoom(t, h)

	saved, err := h.internal.SaveLayoutDocument(collabContext(t), emailauthoring.SaveLayoutDocumentCommand{
		LayoutID: layout.ID, Locale: "en", ExpectedDocumentRevision: revision.String(),
		ContentHTML: "<p><!--geul-unit:text:0d55f289-ff23-43fe-8547-a85109cdd449-->Welcome</p>{{content}}", Contributors: []string{contributor},
	})

	require.NoError(t, err)
	require.NotEqual(t, revision.String(), saved.DocumentRevision)
	source, _ := h.store.LayoutTranslation(layout.ID, "en")
	require.Contains(t, *source.ContentHTML, "Welcome")
	require.Len(t, h.audit.Records(), 1)
	require.Equal(t, contributor, h.audit.Records()[0].MemberID)
	require.Equal(t, sharedtelemetry.AuditEmailLayoutUpdated, h.audit.Records()[0].Action)

	_, err = h.internal.SaveLayoutDocument(collabContext(t), emailauthoring.SaveLayoutDocumentCommand{
		LayoutID: layout.ID, Locale: "en", ExpectedDocumentRevision: revision.String(),
		ContentHTML: *source.ContentHTML, Contributors: []string{contributor},
	})
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(err), "a stale room revision is a collaboration conflict")
}

func TestSaveLayoutDocumentStoresTargetValuesOnlyWhenTheyChange(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	layout, revision, contributor := layoutRoom(t, h)
	room, err := h.internal.LoadLayoutDocument(t.Context(), emailauthoring.LoadLayoutDocumentQuery{LayoutID: layout.ID, Locale: "ko"})
	require.NoError(t, err)
	require.Equal(t, []emailauthoring.LayoutUnitValue{{Handle: layoutHandle, Value: "안녕"}}, room.LocaleValues)
	require.Len(t, room.Units, 1)

	unchanged, err := h.internal.SaveLayoutDocument(collabContext(t), emailauthoring.SaveLayoutDocumentCommand{
		LayoutID: layout.ID, Locale: "ko", ExpectedDocumentRevision: revision.String(), ExpectedTargetRevision: room.TargetRevision,
		LocaleValues: []*emailauthoring.LayoutUnitValue{{Handle: layoutHandle, Value: "안녕"}}, Contributors: []string{contributor},
	})
	require.NoError(t, err)
	require.Equal(t, room.TargetRevision, unchanged.TargetRevision)
	require.Empty(t, h.audit.Records(), "an unchanged target records nothing")

	saved, err := h.internal.SaveLayoutDocument(collabContext(t), emailauthoring.SaveLayoutDocumentCommand{
		LayoutID: layout.ID, Locale: "ko", ExpectedDocumentRevision: revision.String(), ExpectedTargetRevision: room.TargetRevision,
		LocaleValues: []*emailauthoring.LayoutUnitValue{{Handle: layoutHandle, Value: "반가워"}}, Contributors: []string{contributor},
	})
	require.NoError(t, err)
	require.Equal(t, revision.String(), saved.DocumentRevision)
	require.NotEqual(t, room.TargetRevision, saved.TargetRevision)
	require.Equal(t, contributor, h.audit.Records()[0].MemberID)

	_, err = h.internal.SaveLayoutDocument(collabContext(t), emailauthoring.SaveLayoutDocumentCommand{
		LayoutID: layout.ID, Locale: "ko", ExpectedDocumentRevision: revision.String(), ExpectedTargetRevision: room.TargetRevision,
		LocaleValues: []*emailauthoring.LayoutUnitValue{{Handle: layoutHandle, Value: "x"}}, Contributors: []string{contributor},
	})
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(err), "the stale target revision conflicts")
	_, err = h.internal.SaveLayoutDocument(collabContext(t), emailauthoring.SaveLayoutDocumentCommand{
		LayoutID: layout.ID, Locale: "ko", ExpectedDocumentRevision: revision.String(), ExpectedTargetRevision: saved.TargetRevision,
		LocaleValues: []*emailauthoring.LayoutUnitValue{{Handle: "unknown", Value: "x"}}, Contributors: []string{contributor},
	})
	require.EqualError(t, wrapped(err), "invalid_argument: locale_values: contains an unknown Email Layout unit handle")
}

func TestTemplateAIDocumentAuthorizesBeforeCompilingAndRollsBackValidation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template, _, _ := templateRoom(t, h)
	editor := newPrincipal(t)
	h.store.PutMember(editor.memberID)
	documents := h.internal.TemplateAIDocuments()
	compile := func(state emailcontent.AIDocumentState) (emailcontent.AIDocumentMutation, error) {
		documentID := state.DocumentID
		expected := uuid.MustParse(state.DocumentRevision)
		contributor := uuid.MustParse(state.ViewerMemberID)
		return emailcontent.AIDocumentMutation{
			Reference: state.Reference, Locale: state.Locale, ExpectedDocumentRevision: state.DocumentRevision,
			ExpectedTargetRevision: state.TargetRevision, ExpectedSource: state.SourceLocale, ExpectedPresence: state.LocaleExists,
			ContributorMember: contributor, SetSubject: true, Subject: "AI subject",
			Batch: &contentblock.Batch{DocumentID: documentID, ExpectedRevision: expected, ContributorMemberIDs: []uuid.UUID{contributor}},
		}, nil
	}

	validated, err := documents.ExecuteAIDocumentMutation(editor.ctx, template.ID, "en", emailcontent.AIDocumentExecutionValidate, compile)
	require.NoError(t, err)
	require.True(t, validated.Changed)
	require.Equal(t, mutation.StepRollback, lastStep(h.units))
	source, _ := h.store.Locale(template.ID, "en")
	require.Equal(t, "Source subject", *source.Subject)
	require.Empty(t, h.audit.Records())

	applied, err := documents.ExecuteAIDocumentMutation(editor.ctx, template.ID, "en", emailcontent.AIDocumentExecutionApply, compile)
	require.NoError(t, err)
	require.True(t, applied.Changed)
	source, _ = h.store.Locale(template.ID, "en")
	require.Equal(t, "AI subject", *source.Subject)
	require.Equal(t, editor.memberID, h.audit.Records()[0].MemberID)
	updates := h.store.ContentUpdates()
	require.Len(t, updates, 1)
	require.Equal(t, "email_template", updates[0].Entity)

	denied := newHarness(t)
	deniedTemplate, _, _ := templateRoom(t, denied)
	denied.checker.Deny(policyv1.EmailTemplate.Edit(deniedTemplate.ID))
	compiled := false
	_, err = denied.internal.TemplateAIDocuments().ExecuteAIDocumentMutation(editor.ctx, deniedTemplate.ID, "en", emailcontent.AIDocumentExecutionApply,
		func(state emailcontent.AIDocumentState) (emailcontent.AIDocumentMutation, error) {
			compiled = true
			return compile(state)
		})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	require.False(t, compiled, "a denied editor never reaches the compiler")
}

func TestLayoutAIDocumentReplacesTargetValuesAndRejectsAStaleTarget(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	layout, _, _ := layoutRoom(t, h)
	editor := newPrincipal(t)
	h.store.PutMember(editor.memberID)
	documents := h.internal.LayoutAIDocuments()
	replace := func(value string) emailauthoring.EmailLayoutAIDocumentMutationCompiler {
		return func(state emailauthoring.EmailLayoutAIDocumentState) (emailauthoring.EmailLayoutAIDocumentMutation, error) {
			return emailauthoring.EmailLayoutAIDocumentMutation{
				LayoutID: state.LayoutID, Locale: state.Locale, ExpectedDocumentRevision: state.DocumentRevision,
				ExpectedTargetRevision: state.TargetRevision, ExpectedSource: state.SourceLocale, ExpectedPresence: state.LocaleExists,
				ContributorMemberID: uuid.MustParse(state.ViewerMemberID), ReplaceValues: true,
				Values: map[string]string{layoutHandle: value},
			}, nil
		}
	}

	result, err := documents.ExecuteAIDocumentMutation(editor.ctx, layout.ID, "ko", emailauthoring.EmailLayoutAIDocumentExecutionApply, replace("반가워"))
	require.NoError(t, err)
	require.True(t, result.Changed)
	state, err := documents.Load(editor.ctx, layout.ID, "ko")
	require.NoError(t, err)
	require.Equal(t, "반가워", *state.Units[0].LocaleValue)
	require.Equal(t, result.TargetRevision, state.TargetRevision)
	require.Equal(t, editor.memberID, h.audit.Records()[0].MemberID)
	require.Equal(t, "email_layout", h.store.ContentUpdates()[0].Entity)

	_, err = documents.ExecuteAIDocumentMutation(editor.ctx, layout.ID, "ko", emailauthoring.EmailLayoutAIDocumentExecutionApply,
		func(state emailauthoring.EmailLayoutAIDocumentState) (emailauthoring.EmailLayoutAIDocumentMutation, error) {
			mutation, err := replace("x")(state)
			stale := "stale"
			mutation.ExpectedTargetRevision = &stale
			return mutation, err
		})
	var conflict *emailauthoring.EmailLayoutAIDocumentRevisionConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, emailauthoring.EmailLayoutAIDocumentTargetRevisionConflict, conflict.Kind)
	require.Len(t, h.audit.Records(), 1)
}

func TestRequireTranslationSourceMutableLocksTheRootBeforeDeliveryAuthority(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template := h.store.PutTemplate(emailauthoring.Template{Key: "source", Name: "Source"}, "S")
	layout := h.store.PutLayout(emailauthoring.Layout{Key: "frame", Name: "Frame"}, "<main>{{content}}</main>")
	h.store.Freeze(layout.ID)

	require.NoError(t, emailauthoring.RequireTranslationSourceMutable(t.Context(), h.store, "email_template", template.ID))
	err := emailauthoring.RequireTranslationSourceMutable(t.Context(), h.store, "email_layout", layout.ID)
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(err))
	err = emailauthoring.RequireTranslationSourceMutable(t.Context(), h.store, "email_template", uuid.NewString())
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	err = emailauthoring.RequireTranslationSourceMutable(t.Context(), h.store, "campaign", template.ID)
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
}

func TestTemplateTranslationJobsFinishWhileInterchangeEditsStayFrozen(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	template, revision, contributor := templateRoom(t, h)
	h.store.Freeze(template.ID)
	joined := emailauthoring.Joined{Store: h.store, Audit: h.audit}
	subject := "Provider subject"

	err := emailauthoring.ApplyTemplateTranslationCandidate(t.Context(), joined, emailcontent.TranslationJob{
		EntityType: "email_template", EntityID: template.ID, SourceLocale: "en", TargetLocale: "ko", RequestedByMemberID: contributor,
	}, &translation.Candidate{Title: &subject, ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"}}, translation.EntryWrite{})
	require.NoError(t, err, "an accepted job finishes on a frozen template")
	target, _ := h.store.Locale(template.ID, "ko")
	require.Equal(t, "Provider subject", *target.Subject)

	_, err = emailauthoring.ApplyTemplateTranslationInterchange(t.Context(), h.store, template.ID, "en", emailcontent.InterchangeMutation{
		TargetLocale: "ko", ExpectedDocumentRevision: revision.String(), ExpectedPresence: true,
		Subject: &subject, ContributorMemberID: contributor,
	})
	require.Equal(t, connect.CodeFailedPrecondition, codeOf(err), "an interchange edit keeps the delivery freeze")

	err = emailauthoring.ApplyTemplateTranslationCandidate(t.Context(), joined, emailcontent.TranslationJob{EntityType: "campaign"},
		&translation.Candidate{ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{}}, translation.EntryWrite{})
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err))
}
