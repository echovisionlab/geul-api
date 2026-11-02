package legal_test

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/legal/legaltest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// paragraphBatch adds a paragraph in locale to a document at revision.
func paragraphBatch(revision, locale, text string, contributors ...string) *contentv1.RichTextBlockMutationBatch {
	blockID := uuid.NewString()
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
		ExpectedRevision:        revision,
		ContributorMemberIds:    contributors,
		BaseMutations: []*contentv1.RichTextBlockMutation{{
			Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
				Node: &contentv1.RichTextBlockNode{
					Block: &contentv1.RichTextBlock{
						Id:    blockID,
						Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}},
					},
					Placement: &contentv1.ContentBlockPlacement{Index: 1},
				},
			}},
		}},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: locale,
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{
				Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
					Block: &contentv1.RichTextBlockLocale{
						BlockId: blockID,
						Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
							Props:   &contentv1.ParagraphLocaleProps{},
							Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}}}},
						}},
					},
				}},
			}},
		}},
	}
}

func TestLoadBlockDocumentAuthorizesTheCollaborationSession(t *testing.T) {
	h := newHarness(t)
	author := newPrincipal()
	archived, revision := h.putVersion(t, legal.Terms, "archived", nil)
	h.store.PutSession("author-session", author.user())
	banned := newPrincipal().user()
	banned.Banned = true
	h.store.PutSession("banned-session", banned)
	pending := newPrincipal().user()
	pending.Onboarded = false
	h.store.PutSession("pending-session", pending)

	document, err := h.internal.LoadBlockDocument(t.Context(), legal.Terms, legal.LoadBlockDocumentQuery{ID: archived.ID, Locale: "en", SessionID: "author-session"})
	require.NoError(t, err)
	require.Equal(t, revision, document.Snapshot.Document.Revision.String())
	require.Equal(t, legal.LocaleMetadata{Locale: "en", Title: &archived.Title}, document.SourceMetadata)
	require.True(t, document.LocaleExists)
	viewArchived, err := policyv1.TermsHistory.ViewArchived(archived.ID)
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{authztest.Of(author.identityID, viewArchived, nil)},
		h.checker.Checks(), "an archived version opens with view_archived")

	_, err = h.internal.LoadBlockDocument(t.Context(), legal.Terms, legal.LoadBlockDocumentQuery{ID: archived.ID, Locale: "en"})
	requireAnswer(t, err, connect.CodeUnauthenticated, "")
	_, err = h.internal.LoadBlockDocument(t.Context(), legal.Terms, legal.LoadBlockDocumentQuery{ID: archived.ID, Locale: "en", SessionID: "expired"})
	requireAnswer(t, err, connect.CodeUnauthenticated, "")
	_, err = h.internal.LoadBlockDocument(t.Context(), legal.Terms, legal.LoadBlockDocumentQuery{ID: archived.ID, Locale: "en", SessionID: "banned-session"})
	requireAnswer(t, err, connect.CodePermissionDenied, "account is banned")
	_, err = h.internal.LoadBlockDocument(t.Context(), legal.Terms, legal.LoadBlockDocumentQuery{ID: archived.ID, Locale: "en", SessionID: "pending-session"})
	requireAnswer(t, err, connect.CodePermissionDenied, "no permission to edit this terms")
	_, err = h.internal.LoadBlockDocument(t.Context(), legal.Terms, legal.LoadBlockDocumentQuery{ID: archived.ID, Locale: "en-US", SessionID: "author-session"})
	requireAnswer(t, err, connect.CodeInvalidArgument, "locale: must be an exact canonical locale")
}

func TestApplyBlockBatchSavesTheSourceRoomAndPlansItsOG(t *testing.T) {
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			h := newHarness(t)
			past := time.Now().Add(-time.Hour)
			active, revision := h.putVersion(t, kind, "active", func(v *legal.Version) { v.EffectiveFrom = &past })
			contributor := uuid.NewString()

			change, err := h.internal.ApplyBlockBatch(t.Context(), kind, legal.ApplyBlockBatchCommand{
				ID: active.ID, Locale: "en", Batch: paragraphBatch(revision, "en", "Added", contributor),
			})
			require.NoError(t, err)
			require.True(t, change.Changed)
			require.True(t, change.SourceChanged)
			require.Equal(t, "en", change.Locale)
			require.Equal(t, h.revision(t, kind, active.ID), change.DocumentRevision)
			require.Equal(t, [][]string{{contributor}}, h.store.Contributions())
			require.Equal(t, []legaltest.OGRequest{{Kind: kind, ID: active.ID, Reason: string(kind) + "_block_document_saved"}}, h.store.OGRequests())
			require.Empty(t, h.audit.Records(), "a source room save records no audit")

			_, err = h.internal.ApplyBlockBatch(t.Context(), kind, legal.ApplyBlockBatchCommand{
				ID: active.ID, Locale: "en", Batch: paragraphBatch(revision, "en", "Stale", contributor),
			})
			requireAnswer(t, err, connect.CodeFailedPrecondition, string(kind)+" document changed since it was loaded; reload before saving")
		})
	}
}

func TestApplyBlockBatchRejectsWhatTheRoomMayNotSave(t *testing.T) {
	h := newHarness(t)
	draft, revision := h.putVersion(t, legal.Privacy, "draft", nil)
	contributor := uuid.NewString()

	_, err := h.internal.ApplyBlockBatch(t.Context(), legal.Privacy, legal.ApplyBlockBatchCommand{ID: draft.ID, Locale: "en"})
	requireAnswer(t, err, connect.CodeInvalidArgument, "batch is required")
	target := "tr1_any"
	_, err = h.internal.ApplyBlockBatch(t.Context(), legal.Privacy, legal.ApplyBlockBatchCommand{
		ID: draft.ID, Locale: "en", ExpectedTargetRevision: &target, Batch: paragraphBatch(revision, "en", "x", contributor),
	})
	requireAnswer(t, err, connect.CodeInvalidArgument, "expected_target_revision: source legal room cannot carry a target revision")
	_, err = h.internal.ApplyBlockBatch(t.Context(), legal.Privacy, legal.ApplyBlockBatchCommand{
		ID: draft.ID, Locale: "en", Batch: paragraphBatch(revision, "ko", "x", contributor),
	})
	requireAnswer(t, err, connect.CodeInvalidArgument, "source legal room mutation must match the current source locale")

	h.store.Fail = map[string]error{"RequireCurrentContributors": errors.New("contributor no longer authorized")}
	_, err = h.internal.ApplyBlockBatch(t.Context(), legal.Privacy, legal.ApplyBlockBatchCommand{
		ID: draft.ID, Locale: "en", Batch: paragraphBatch(revision, "en", "x", contributor),
	})
	require.Error(t, err)
	require.Equal(t, mutation.StepRollback, h.units.Steps()[len(h.units.Steps())-1])
	require.Equal(t, revision, h.revision(t, legal.Privacy, draft.ID))
}

func TestApplyBlockBatchSavesATargetRoomAsItsLocaleContent(t *testing.T) {
	h := newHarness(t)
	draft, revision := h.putVersion(t, legal.Terms, "draft", nil)
	h.store.PutTranslation(legal.Terms, draft.ID, legal.Translation{Locale: "ko", Title: ptr("Korean"), UpdatedAt: time.Now().Add(-time.Hour)})
	loaded, err := h.internal.LoadBlockDocument(t.Context(), legal.Terms, legal.LoadBlockDocumentQuery{ID: draft.ID, Locale: "ko", SessionID: "s"})
	require.Error(t, err, "no session")
	_ = loaded
	member := newPrincipal()
	h.store.PutSession("member-session", member.user())
	loaded, err = h.internal.LoadBlockDocument(t.Context(), legal.Terms, legal.LoadBlockDocumentQuery{ID: draft.ID, Locale: "ko", SessionID: "member-session"})
	require.NoError(t, err)
	require.NotNil(t, loaded.TargetRevision)

	batch := paragraphBatch(revision, "ko", "번역", member.memberID)
	batch.BaseMutations = nil
	batch.LocaleMutationGroups[0].Mutations[0].GetUpsert().Block.BlockId = sourceBlockID(t, h, legal.Terms, draft.ID)
	ctx := member.context(t)
	change, err := h.internal.ApplyBlockBatch(ctx, legal.Terms, legal.ApplyBlockBatchCommand{
		ID: draft.ID, Locale: "ko", ExpectedTargetRevision: loaded.TargetRevision, Batch: batch,
	})
	require.NoError(t, err)
	require.True(t, change.Changed)
	require.Equal(t, revision, change.DocumentRevision, "a target save never advances the shared revision")
	require.NotEqual(t, *loaded.TargetRevision, *change.TargetRevision)
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, "ko", records[0].Locale)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, records[0].ItemOperation)
	stored, _ := h.store.StoredTranslation(legal.Terms, draft.ID, "ko")
	require.Contains(t, stored.Projection.HTML, "번역", "the target's derived content is rebuilt")

	_, err = h.internal.ApplyBlockBatch(ctx, legal.Terms, legal.ApplyBlockBatchCommand{
		ID: draft.ID, Locale: "ko", ExpectedTargetRevision: loaded.TargetRevision, Batch: batch,
	})
	requireAnswer(t, err, connect.CodeFailedPrecondition, "terms target locale changed since it was loaded; reload before saving")
}

// sourceBlockID is the first base Block of a version's document.
func sourceBlockID(t *testing.T, h *harness, kind legal.PolicyKind, id string) string {
	t.Helper()
	record := h.stored(t, kind, id)
	document, ok := h.store.Document(uuid.MustParse(*record.ContentDocumentID))
	require.True(t, ok)
	return document.Blocks[0].ID.String()
}

func TestUpdateLocaleMetadataChangesTheSourceTitleAsARevision(t *testing.T) {
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			h := newHarness(t)
			draft, revision := h.putVersion(t, kind, "draft", nil)
			contributor := uuid.NewString()

			change, err := h.internal.UpdateLocaleMetadata(t.Context(), kind, legal.UpdateLocaleMetadataCommand{
				EntityID: draft.ID, Locale: "en", ExpectedRevision: revision, Title: ptr("  Updated title "), Contributors: []string{contributor},
			})
			require.NoError(t, err)
			require.True(t, change.Changed)
			require.True(t, change.SourceChanged)
			require.NotEqual(t, revision, change.DocumentRevision)
			require.Equal(t, "Updated title", h.stored(t, kind, draft.ID).Title)
			require.Empty(t, h.audit.Records(), "a source title edit appends no Domain Audit")

			unchanged, err := h.internal.UpdateLocaleMetadata(t.Context(), kind, legal.UpdateLocaleMetadataCommand{
				EntityID: draft.ID, Locale: "en", ExpectedRevision: change.DocumentRevision, Title: ptr("Updated title"), Contributors: []string{contributor},
			})
			require.NoError(t, err)
			require.False(t, unchanged.Changed)
			require.Equal(t, change.DocumentRevision, unchanged.DocumentRevision)
			_, err = h.internal.UpdateLocaleMetadata(t.Context(), kind, legal.UpdateLocaleMetadataCommand{
				EntityID: draft.ID, Locale: "en", ExpectedRevision: change.DocumentRevision, Title: ptr(" "), Contributors: []string{contributor},
			})
			requireAnswer(t, err, connect.CodeInvalidArgument, "title: must not be empty")
		})
	}
}

func TestUpdateLocaleMetadataWritesATargetTitle(t *testing.T) {
	h := newHarness(t)
	past := time.Now().Add(-time.Hour)
	draft, revision := h.putVersion(t, legal.Privacy, "active", func(v *legal.Version) { v.EffectiveFrom = &past })
	h.store.PutTranslation(legal.Privacy, draft.ID, legal.Translation{Locale: "ko", Title: ptr("Before"), UpdatedAt: time.Now().Add(-time.Hour)})
	member := newPrincipal()
	h.store.PutSession("member-session", member.user())
	loaded, err := h.internal.LoadBlockDocument(t.Context(), legal.Privacy, legal.LoadBlockDocumentQuery{ID: draft.ID, Locale: "ko", SessionID: "member-session"})
	require.NoError(t, err)

	change, err := h.internal.UpdateLocaleMetadata(member.context(t), legal.Privacy, legal.UpdateLocaleMetadataCommand{
		EntityID: draft.ID, Locale: "ko", ExpectedRevision: revision, ExpectedTargetRevision: loaded.TargetRevision,
		Title: ptr("After"), Contributors: []string{member.memberID},
	})
	require.NoError(t, err)
	require.True(t, change.Changed)
	require.Equal(t, revision, change.DocumentRevision)
	stored, _ := h.store.StoredTranslation(legal.Privacy, draft.ID, "ko")
	require.Equal(t, ptr("After"), stored.Title)
	require.Equal(t, []legaltest.OGRequest{{Kind: legal.Privacy, ID: draft.ID, Locale: "ko", Reason: "privacy_locale_title_saved"}}, h.store.OGRequests())

	_, err = h.internal.UpdateLocaleMetadata(t.Context(), legal.Privacy, legal.UpdateLocaleMetadataCommand{
		EntityID: draft.ID, Locale: "ko", ExpectedRevision: revision, Title: ptr("x"), Contributors: []string{"not-a-uuid"},
	})
	requireAnswer(t, err, connect.CodeInvalidArgument, "contributor_member_ids: must be a canonical UUID")
	_ = auth.ErrSessionPrincipalInvalid
}
