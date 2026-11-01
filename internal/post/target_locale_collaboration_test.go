package post_test

import (
	"errors"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// postParagraphBatch writes values into paragraphs blockIDs of locale,
// creating the paragraphs first when includeStructure is set.
func postParagraphBatch(expectedRevision, contributorMemberID, locale string, blockIDs, values []string, includeStructure bool) *contentv1.RichTextBlockMutationBatch {
	mutations := make([]*contentv1.RichTextBlockMutation, 0, len(blockIDs))
	localeMutations := make([]*contentv1.RichTextBlockLocaleMutation, 0, len(blockIDs))
	for index, blockID := range blockIDs {
		if includeStructure {
			mutations = append(mutations, &contentv1.RichTextBlockMutation{
				Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
					Node: &contentv1.RichTextBlockNode{
						Block: &contentv1.RichTextBlock{
							Id:    blockID,
							Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}},
						},
						Placement: &contentv1.ContentBlockPlacement{Index: uint32(index)},
					},
				}},
			})
		}
		localeMutations = append(localeMutations, &contentv1.RichTextBlockLocaleMutation{
			Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
				Block: postParagraphLocaleBlock(blockID, values[index]),
			}},
		})
	}
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    []string{contributorMemberID},
		BaseMutations:           mutations,
		LocaleMutationGroups:    []*contentv1.RichTextLocaleMutationGroup{{Locale: locale, Mutations: localeMutations}},
	}
}

// postParagraphLocaleBlock is a paragraph's locale value; an empty value
// is an explicit empty paragraph.
func postParagraphLocaleBlock(blockID, value string) *contentv1.RichTextBlockLocale {
	var content []*contentv1.RichTextInline
	if value != "" {
		content = []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: value}}}}
	}
	return &contentv1.RichTextBlockLocale{
		BlockId: blockID,
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Props: &contentv1.ParagraphLocaleProps{}, Content: content,
		}},
	}
}

// postFileStructureBatch adds a File Block, a structural change.
func postFileStructureBatch(expectedRevision, contributorMemberID string) *contentv1.RichTextBlockMutationBatch {
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    []string{contributorMemberID},
		BaseMutations: []*contentv1.RichTextBlockMutation{{
			Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
				Node: &contentv1.RichTextBlockNode{
					Block: &contentv1.RichTextBlock{
						Id: uuid.NewString(),
						Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{
							Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: uuid.NewString()}},
						}}},
					},
					Placement: &contentv1.ContentBlockPlacement{},
				},
			}},
		}},
	}
}

func postParagraphContentTargets(blockIDs ...string) []*managev1.AIDocumentFieldTarget {
	targets := make([]*managev1.AIDocumentFieldTarget, 0, len(blockIDs))
	for _, blockID := range blockIDs {
		targets = append(targets, &managev1.AIDocumentFieldTarget{
			Owner: &managev1.AIDocumentFieldTarget_BlockHandle{BlockHandle: blockID}, FieldHandle: "content",
		})
	}
	sort.Slice(targets, func(left, right int) bool { return targets[left].GetBlockHandle() < targets[right].GetBlockHandle() })
	return targets
}

// requireCollaborationDetail checks the reason a collaboration error
// carries to the room.
func requireCollaborationDetail(t *testing.T, err error, conflict intrav1.CollaborationConflictReason, rejection intrav1.CollaborationMutationRejectionReason) {
	t.Helper()
	var connectErr *connect.Error
	require.True(t, errors.As(errs.Wrap(err), &connectErr), err)
	for _, detail := range connectErr.Details() {
		value, detailErr := detail.Value()
		require.NoError(t, detailErr)
		switch value := value.(type) {
		case *intrav1.CollaborationConflictDetail:
			require.Equal(t, conflict, value.Reason)
			return
		case *intrav1.CollaborationMutationRejectionDetail:
			require.Equal(t, rejection, value.Reason)
			return
		}
	}
	t.Fatal("the error carries no collaboration detail")
}

// targetLifecycle creates or deletes a Post target locale through the AI
// document mutation, as an editor does.
func (h *harness) targetLifecycle(t *testing.T, editor principal, postID, locale string, remove bool) post.AIDocumentMutationResult {
	t.Helper()
	result, err := h.service.ExecuteAIDocumentMutation(editor.context(t), postID, locale, post.AIDocumentExecutionApply,
		func(state post.AIDocumentState) (post.AIDocumentMutation, error) {
			revision, contributor := uuid.MustParse(state.DocumentRevision), uuid.MustParse(state.ViewerMemberID)
			mutation := post.AIDocumentMutation{
				PostID: state.PostID, Locale: state.RequestedLocale,
				ObservedSourceLocale: state.SourceLocale, ObservedLocaleExists: state.LocaleExists,
				ExpectedRevision: revision, ExpectedTargetRevision: state.TargetRevision, ContributorMemberID: contributor,
				Batch: contentblock.Batch{DocumentID: uuid.MustParse(state.ContentDocumentID), ExpectedRevision: revision, ContributorMemberIDs: []uuid.UUID{contributor}},
			}
			if remove {
				mutation.DeleteTranslation = true
			} else {
				mutation.Metadata.EnsureLocale = true
			}
			return mutation, nil
		})
	require.NoError(t, err)
	return result
}

// room loads a collaboration room of postID in locale.
func (h *harness) room(t *testing.T, sessionID, postID, locale string) post.RoomDocument {
	t.Helper()
	room, err := h.service.LoadPostBlockDocument(t.Context(), post.LoadPostBlockDocumentQuery{PostID: postID, Locale: locale, SessionID: &sessionID})
	require.NoError(t, err)
	return room
}

func TestPostTargetLocaleRoomsKeepTheirOwnRevisions(t *testing.T) {
	h := newHarness(t)
	editor := h.member(newPrincipal(), "Editor")
	value := h.seedPost("Rooms", editor.memberID)
	sessionID := uuid.NewString()
	h.store.PutSession(sessionID, editor.user())
	blockIDs := []string{uuid.NewString(), uuid.NewString()}

	source, err := h.service.ApplyPostBlockBatch(t.Context(), post.ApplyPostBlockBatchCommand{
		PostID: value.ID, Locale: "en",
		Batch:                postParagraphBatch(h.revision(t, value.ID).String(), editor.memberID, "en", blockIDs, []string{"first", ""}, true),
		AffectedLocaleValues: postParagraphContentTargets(blockIDs...),
	})
	require.NoError(t, err)
	require.True(t, source.Changed)

	missing := h.room(t, sessionID, value.ID, "ko")
	require.False(t, missing.LocaleExists)
	require.Nil(t, missing.TargetRevision)
	require.Equal(t, source.DocumentRevision, missing.DocumentRevision)
	editorUser := editor.user()
	editorUser.SessionID = auth.SessionID(sessionID)
	editorCtx := auth.WithUser(editor.context(t), editorUser)
	for _, locale := range []string{"en", "ko"} {
		room := h.room(t, sessionID, value.ID, locale)
		firstPaint, err := h.service.GetPostEditorDocument(editorCtx, post.GetPostEditorDocumentQuery{PostID: value.ID, Locale: locale})
		require.NoError(t, err)
		require.True(t, proto.Equal(room.Document, firstPaint.Document), "the editor's first paint is the room bootstrap")
		require.Equal(t, room.DocumentRevision, firstPaint.DocumentRevision)
		require.Equal(t, room.TargetRevision, firstPaint.TargetRevision)
		require.True(t, firstPaint.Editable)
	}

	_, err = h.service.ApplyPostBlockBatch(t.Context(), post.ApplyPostBlockBatchCommand{
		PostID: value.ID, Locale: "ko",
		Batch: postParagraphBatch(source.DocumentRevision, editor.memberID, "ko", blockIDs[:1], []string{"blocked"}, false),
	})
	requireCode(t, err, connect.CodeFailedPrecondition, "", "a missing target room cannot create itself")

	ko := h.targetLifecycle(t, editor, value.ID, "ko", false)
	require.True(t, ko.Result.Changed)
	require.Equal(t, source.DocumentRevision, ko.Result.DocumentRevision.String(), "creating a target keeps the shared revision")
	require.NotNil(t, ko.TargetRevision)
	ja := h.targetLifecycle(t, editor, value.ID, "ja", false)
	jaRevision := *ja.TargetRevision

	koRoom := h.room(t, sessionID, value.ID, "ko")
	require.Equal(t, ko.TargetRevision, koRoom.TargetRevision)
	edited, err := h.service.ApplyPostBlockBatch(t.Context(), post.ApplyPostBlockBatchCommand{
		PostID: value.ID, Locale: "ko", ExpectedTargetRevision: koRoom.TargetRevision,
		Batch: postParagraphBatch(koRoom.DocumentRevision, editor.memberID, "ko", blockIDs[:1], []string{"edited"}, false),
	})
	require.NoError(t, err)
	require.True(t, edited.Changed)
	require.Equal(t, source.DocumentRevision, edited.DocumentRevision, "a target edit keeps the shared revision")
	require.NotEqual(t, *koRoom.TargetRevision, *edited.TargetRevision)
	_, err = h.service.ApplyPostBlockBatch(t.Context(), post.ApplyPostBlockBatchCommand{
		PostID: value.ID, Locale: "ko", ExpectedTargetRevision: koRoom.TargetRevision,
		Batch: postParagraphBatch(koRoom.DocumentRevision, editor.memberID, "ko", blockIDs[:1], []string{"stale"}, false),
	})
	requireCode(t, err, connect.CodeFailedPrecondition, "")
	requireCollaborationDetail(t, err, intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED, 0)
	require.Equal(t, jaRevision, *h.room(t, sessionID, value.ID, "ja").TargetRevision, "one target's edit leaves the others")

	for _, batch := range []*contentv1.RichTextBlockMutationBatch{
		postParagraphBatch(source.DocumentRevision, editor.memberID, "ko", []string{uuid.NewString()}, []string{"forbidden"}, true),
		postFileStructureBatch(source.DocumentRevision, editor.memberID),
	} {
		_, err = h.service.ApplyPostBlockBatch(t.Context(), post.ApplyPostBlockBatchCommand{
			PostID: value.ID, Locale: "ko", ExpectedTargetRevision: edited.TargetRevision, Batch: batch,
		})
		requireCode(t, err, connect.CodeInvalidArgument, "")
		requireCollaborationDetail(t, err, 0, intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN)
	}

	second, err := h.service.ApplyPostBlockBatch(t.Context(), post.ApplyPostBlockBatchCommand{
		PostID: value.ID, Locale: "en",
		Batch:                postParagraphBatch(source.DocumentRevision, editor.memberID, "en", blockIDs[1:], []string{"source changed"}, false),
		AffectedLocaleValues: postParagraphContentTargets(blockIDs[1]),
	})
	require.NoError(t, err)
	require.NotEqual(t, source.DocumentRevision, second.DocumentRevision)
	koAfter, jaAfter := h.room(t, sessionID, value.ID, "ko"), h.room(t, sessionID, value.ID, "ja")
	require.NotEqual(t, *edited.TargetRevision, *koAfter.TargetRevision, "a source write moves every target revision")
	require.NotEqual(t, jaRevision, *jaAfter.TargetRevision)
	require.Equal(t, second.DocumentRevision, koAfter.DocumentRevision)

	jaBeforeDelete := *jaAfter.TargetRevision
	deleted := h.targetLifecycle(t, editor, value.ID, "ko", true)
	require.True(t, deleted.Result.Changed)
	require.Nil(t, deleted.TargetRevision)
	require.Equal(t, second.DocumentRevision, deleted.Result.DocumentRevision.String())
	_, exists := h.store.Locale(value.ID, "ko")
	require.False(t, exists)
	require.Equal(t, jaBeforeDelete, *h.room(t, sessionID, value.ID, "ja").TargetRevision)
}

func TestPostTranslationInterchangeDerivesTargetRevisionsAndAdvancesStrictly(t *testing.T) {
	h := newHarness(t)
	editor := h.member(newPrincipal(), "Editor")
	value := h.seedPost("Interchange", editor.memberID)
	sessionID := uuid.NewString()
	h.store.PutSession(sessionID, editor.user())
	blockIDs := []string{uuid.NewString(), uuid.NewString()}
	source, err := h.service.ApplyPostBlockBatch(t.Context(), post.ApplyPostBlockBatchCommand{
		PostID: value.ID, Locale: "en",
		Batch:                postParagraphBatch(h.revision(t, value.ID).String(), editor.memberID, "en", blockIDs, []string{"first", "second"}, true),
		AffectedLocaleValues: postParagraphContentTargets(blockIDs...),
	})
	require.NoError(t, err)
	interchange, err := post.NewTranslationInterchange(h.audit)
	require.NoError(t, err)
	job := post.TranslationJob{EntityType: "post", EntityID: value.ID, SourceLocale: "en", TargetLocale: "ko", RequestedByMemberID: editor.memberID}
	now := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	candidate := func(blockID, text string) *translation.Candidate {
		return &translation.Candidate{
			ContentDocumentRevision: source.DocumentRevision,
			ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{
				Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{postParagraphLocaleBlock(blockID, text)},
			},
		}
	}
	apply := func(candidate *translation.Candidate, expected *string) (post.TranslationInterchangeMutationResult, error) {
		var result post.TranslationInterchangeMutationResult
		err := h.units.Transaction(t.Context(), false, func(tx mutation.Tx[post.Store]) error {
			var err error
			result, err = interchange.ApplyCandidate(editor.context(t), tx.Store(), job, candidate, translation.EntryWrite{Now: now}, expected)
			return err
		})
		return result, err
	}

	first, err := apply(candidate(blockIDs[0], "target first"), nil)
	require.NoError(t, err)
	require.True(t, first.Changed)
	require.Equal(t, first.Revision, *h.room(t, sessionID, value.ID, "ko").TargetRevision, "the result names the room's target revision")
	second, err := apply(candidate(blockIDs[1], "target second"), &first.Revision)
	require.NoError(t, err)
	require.True(t, second.Changed)
	require.NotEqual(t, first.Revision, second.Revision, "a write at the same instant still advances the target")
	_, err = apply(candidate(blockIDs[1], "target second"), &first.Revision)
	requireCode(t, err, connect.CodeFailedPrecondition, "", "a stale target revision is rejected")

	room := h.room(t, sessionID, value.ID, "ko")
	require.Equal(t, source.DocumentRevision, room.DocumentRevision, "a translation keeps the shared revision")
	require.Equal(t, second.Revision, *room.TargetRevision)
	var operations []sharedtelemetry.AuditItemOperation
	for _, record := range h.audit.Records() {
		if record.Locale == "ko" {
			require.Equal(t, editor.memberID, record.MemberID)
			operations = append(operations, record.ItemOperation)
		}
	}
	require.Equal(t, []sharedtelemetry.AuditItemOperation{sharedtelemetry.AuditItemOperationCreated, sharedtelemetry.AuditItemOperationUpdated}, operations)
}
