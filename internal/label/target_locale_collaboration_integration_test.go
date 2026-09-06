//go:build integration

package label

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLabelTargetLocaleCASSeedDeleteAndProviderDeliveryIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	identityID := uuid.NewString()
	seedExternalKratosIdentityWithTraits(t, db, identityID, "Label target admin")
	requireInternalResourceAdmin(t, stack.SpiceDBClient, identityID)
	store := newCreativeContentIntegrationStore(t, stack.SpiceDBClient)
	labelID := seedInternalLabel(t, db, store, integrationMemberID(identityID))
	attachInternalResourcePolicy(t, stack.SpiceDBClient, labelID)
	now := time.Now().UTC().Truncate(time.Microsecond)
	title := "Label target locale"
	require.NoError(t, saveLabelSourceLocaleDocumentState(t.Context(), db, labelID, "en", translationLocaleDocumentSaveInput{
		Title: &title, OverwriteNullFields: true, Now: now,
	}))
	documentRevision := attachCreativeContentIntegrationDocument(
		t, db, store, labelContentEntity, labelID, "en", "source description",
	)
	documentID := labelDocumentIDForIntegration(t, db, labelID)
	memberID := labelOwnerIDForIntegration(t, db, labelID)
	sessionID := testutil.InsertPostIntegrationSession(t, db, identityID)
	auditWriter := apitelemetry.NewDurableWriter(db)
	memberCtx := labelTargetMemberContext(t, identityID)
	internal := NewAuditedInternalLabelService(
		db, &capturingAsyncPublisher{}, stack.SpiceDBClient,
		auditWriter,
		Dependencies{Translation: labeladapter.NewTranslation(), Runtime: labelTargetIntegrationRuntime{}},
		WithInternalLabelContentBlockStore(store),
		WithInternalLabelCheckpoints(testcollaboration.NewCheckpoints(db, stack.SpiceDBClient)),
	)
	aiService := NewAuditedLabelService(
		db, stack.SpiceDBClient,
		&fakeIdentityManager{identity: postIntegrationIdentity(identityID, "en")},
		&recordingArtistFileDeleter{}, &capturingAsyncPublisher{}, auditWriter,
		Dependencies{
			Translation: labeladapter.NewTranslation(),
			Members:     labeladapter.NewMemberProjection(db, ""),
			Runtime:     labelTargetIntegrationRuntime{},
		},
		WithLabelContentBlockStore(store),
	)

	missing := loadLabelTargetRoomIntegration(t, internal, sessionID, labelID, "ko")
	require.False(t, missing.Msg.LocaleExists)
	require.Nil(t, missing.Msg.TargetRevision)
	require.Equal(t, documentRevision, missing.Msg.DocumentRevision)
	require.Equal(t, "en", missing.Msg.SourceMetadata.GetLocale())
	require.Equal(t, "ko", missing.Msg.GetLocale())
	require.Equal(t, "source description", labelFirstBlockText(t, missing.Msg.Document),
		"a target collaboration room must render missing target units from the current source")
	_, err := internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "ko",
		Batch: labelTargetParagraphBatch(documentRevision, memberID.String(), "ko", labelFirstBlockID(t, missing.Msg.Document), "blocked"),
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	koCreated := executeLabelTargetLifecycleIntegration(
		t, memberCtx, aiService, labelID, "ko", documentRevision, nil, false,
	)
	require.True(t, koCreated.Content.Changed)
	require.Equal(t, documentRevision, koCreated.Content.DocumentRevision.String())
	require.NotNil(t, koCreated.TargetRevision)
	require.Equal(t, 1, labelLocaleOverlayCountIntegration(t, db, labelID, "ko"), "DCDP create seeds every source stable unit")
	frCreated := executeLabelTargetLifecycleIntegration(
		t, memberCtx, aiService, labelID, "fr", documentRevision, nil, false,
	)
	require.NotNil(t, frCreated.TargetRevision)
	frUpdated := executeLabelTargetValueIntegration(
		t, memberCtx, aiService, labelID, "fr", "DCDP updated fr",
	)
	require.True(t, frUpdated.Content.Changed)
	require.NotNil(t, frUpdated.TargetRevision)
	require.NotEqual(t, frCreated.TargetRevision, frUpdated.TargetRevision)
	frRevisionBefore := *frUpdated.TargetRevision
	deCreated := executeLabelTargetValueIntegration(
		t, memberCtx, aiService, labelID, "de", "DCDP first write de",
	)
	require.True(t, deCreated.Content.Changed)
	require.NotNil(t, deCreated.TargetRevision)
	require.Equal(t, 1, labelLocaleOverlayCountIntegration(t, db, labelID, "de"), "DCDP first write seeds and mutates in one transaction")

	koLoaded := loadLabelTargetRoomIntegration(t, internal, sessionID, labelID, "ko")
	blockID := labelFirstBlockID(t, koLoaded.Msg.Document)
	koEdited, err := internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "ko", ExpectedTargetRevision: koLoaded.Msg.TargetRevision,
		Batch: labelTargetParagraphBatch(documentRevision, memberID.String(), "ko", blockID, "edited ko"),
	}))
	require.NoError(t, err)
	require.True(t, koEdited.Msg.Changed)
	require.Equal(t, documentRevision, koEdited.Msg.DocumentRevision)
	require.NotNil(t, koEdited.Msg.TargetRevision)
	require.NotEqual(t, *koLoaded.Msg.TargetRevision, *koEdited.Msg.TargetRevision)
	frAfterKO := loadLabelTargetRoomIntegration(t, internal, sessionID, labelID, "fr")
	require.Equal(t, frRevisionBefore, *frAfterKO.Msg.TargetRevision, "unrelated target locales have independent CAS")

	_, err = internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "ko", ExpectedTargetRevision: koLoaded.Msg.TargetRevision,
		Batch: labelTargetParagraphBatch(documentRevision, memberID.String(), "ko", blockID, "stale"),
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	localeDelete := &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		ExpectedRevision:        documentRevision,
		ContributorMemberIds:    []string{memberID.String()},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: "ko",
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{Operation: &contentv1.RichTextBlockLocaleMutation_Delete{
				Delete: &contentv1.DeleteRichTextBlockLocale{BlockId: blockID},
			}}},
		}},
	}
	_, err = internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "ko", ExpectedTargetRevision: koEdited.Msg.TargetRevision, Batch: localeDelete,
	}))
	requireLabelMutationRejectionIntegration(
		t, err,
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
	)

	dcdpDeleteErr := db.Transaction(func(tx *gorm.DB) error {
		_, mutationErr := aiService.applyAIDocumentMode(
			t.Context(), tx,
			AIDocumentMutation{
				LabelID: labelID, Locale: "ko", ExpectedRevision: documentRevision,
				ExpectedTargetRevision: koEdited.Msg.TargetRevision, ExpectedSource: "en", ExpectedPresence: true,
				ContributorMemberID: memberID,
				Batch: &contentblock.Batch{
					DocumentID: documentID, ExpectedRevision: uuid.MustParse(documentRevision),
					ContributorMemberIDs: []uuid.UUID{memberID},
					LocaleGroups:         []contentblock.LocaleMutationGroup{{Locale: "ko", Deletes: []uuid.UUID{uuid.MustParse(blockID)}}},
				},
			},
			uuid.MustParse(documentRevision), memberID, documentID,
			labelAuthorizedAIDocumentFence(labelAIDocumentRoot{ID: labelID, DocumentID: &documentID}, "en"),
		)
		return mutationErr
	})
	requireLabelMutationRejectionIntegration(
		t, dcdpDeleteErr,
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
	)

	koAfterRejectedDeletes := loadLabelTargetRoomIntegration(t, internal, sessionID, labelID, "ko")
	require.Equal(t, koEdited.Msg.TargetRevision, koAfterRejectedDeletes.Msg.TargetRevision)
	require.Equal(t, documentRevision, koAfterRejectedDeletes.Msg.DocumentRevision)
	explicitEmpty, err := internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "ko", ExpectedTargetRevision: koAfterRejectedDeletes.Msg.TargetRevision,
		Batch: labelTargetParagraphBatch(documentRevision, memberID.String(), "ko", blockID, ""),
	}))
	require.NoError(t, err)
	require.True(t, explicitEmpty.Msg.Changed)
	require.NotEqual(t, koAfterRejectedDeletes.Msg.TargetRevision, explicitEmpty.Msg.TargetRevision)
	require.Equal(t, 1, labelLocaleOverlayCountIntegration(t, db, labelID, "ko"), "explicit empty preserves the locale unit")

	structural := labelTargetParagraphBatch(documentRevision, memberID.String(), "ko", blockID, "forbidden")
	structural.BaseMutations = []*contentv1.RichTextBlockMutation{{Operation: &contentv1.RichTextBlockMutation_Delete{
		Delete: &contentv1.DeleteRichTextBlock{BlockId: blockID},
	}}}
	_, err = internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "ko", ExpectedTargetRevision: explicitEmpty.Msg.TargetRevision, Batch: structural,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	requireLabelMutationRejectionIntegration(
		t, err,
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
	)
	shared := labelTargetParagraphBatch(documentRevision, memberID.String(), "ko", blockID, "forbidden")
	shared.BaseMutations = []*contentv1.RichTextBlockMutation{{Operation: &contentv1.RichTextBlockMutation_Upsert{
		Upsert: &contentv1.UpsertRichTextBlock{Node: &contentv1.RichTextBlockNode{
			Block:     &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}},
			Placement: &contentv1.ContentBlockPlacement{},
		}},
	}}}
	_, err = internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "ko", ExpectedTargetRevision: explicitEmpty.Msg.TargetRevision, Batch: shared,
	}))
	requireLabelMutationRejectionIntegration(
		t, err,
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_SHARED_FIELD_FORBIDDEN,
	)
	fileMutation := labelTargetParagraphBatch(documentRevision, memberID.String(), "ko", blockID, "forbidden")
	fileMutation.BaseMutations = []*contentv1.RichTextBlockMutation{{Operation: &contentv1.RichTextBlockMutation_Upsert{
		Upsert: &contentv1.UpsertRichTextBlock{Node: &contentv1.RichTextBlockNode{
			Block: &contentv1.RichTextBlock{Id: uuid.NewString(), Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{
				Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_MissingAttachment{
					MissingAttachment: &contentv1.MissingAttachment{
						FormerFileId: uuid.NewString(), MediaKind: contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_FILE,
					},
				}},
			}}}},
			Placement: &contentv1.ContentBlockPlacement{},
		}},
	}}}
	_, err = internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "ko", ExpectedTargetRevision: explicitEmpty.Msg.TargetRevision, Batch: fileMutation,
	}))
	requireLabelMutationRejectionIntegration(
		t, err,
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_FILE_RELATION_FORBIDDEN,
	)
	targetTitle := "forbidden target title"
	_, err = internal.UpdateLabelLocaleMetadata(memberCtx, connect.NewRequest(&intrav1.UpdateLabelLocaleMetadataRequest{
		LabelId: labelID, Locale: "ko", ExpectedRevision: documentRevision,
		ExpectedTargetRevision: explicitEmpty.Msg.TargetRevision, ContributorMemberIds: []string{memberID.String()},
		Title: &targetTitle,
	}))
	requireLabelMutationRejectionIntegration(
		t, err,
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_DOCUMENT_METADATA_FORBIDDEN,
	)

	sourceEdited, err := internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "en",
		Batch: labelTargetParagraphBatch(documentRevision, memberID.String(), "en", blockID, "source changed"),
	}))
	require.NoError(t, err)
	require.NotEqual(t, documentRevision, sourceEdited.Msg.DocumentRevision)
	require.NotEqual(t, documentRevision, sourceEdited.Msg.DocumentRevision)
	koAfterSource := loadLabelTargetRoomIntegration(t, internal, sessionID, labelID, "ko")
	require.NotEqual(t, *explicitEmpty.Msg.TargetRevision, *koAfterSource.Msg.TargetRevision)
	require.Equal(t, sourceEdited.Msg.DocumentRevision, koAfterSource.Msg.DocumentRevision)

	deleted := executeLabelTargetLifecycleIntegration(
		t, memberCtx, aiService, labelID, "ko",
		sourceEdited.Msg.DocumentRevision, koAfterSource.Msg.TargetRevision, true,
	)
	require.True(t, deleted.Content.Changed)
	require.Equal(t, sourceEdited.Msg.DocumentRevision, deleted.Content.DocumentRevision.String())
	koAfterDelete := loadLabelTargetRoomIntegration(t, internal, sessionID, labelID, "ko")
	require.False(t, koAfterDelete.Msg.LocaleExists)
	require.Nil(t, koAfterDelete.Msg.TargetRevision)
	require.Equal(t, sourceEdited.Msg.DocumentRevision, koAfterDelete.Msg.DocumentRevision)

	providerCandidate := &translation.Candidate{
		ContentDocumentRevision:   sourceEdited.Msg.DocumentRevision,
		ContentBlockLocaleOverlay: labelTargetLocaleOverlay(blockID, "ja", "provider ja"),
	}
	providerJob := &model.TranslationJob{
		EntityType: labelContentEntity, EntityID: labelID, SourceLocale: "en", TargetLocale: "ja",
		RequestedByMemberID: integrationMemberID(identityID),
	}
	providerJobWithoutRequester := *providerJob
	providerJobWithoutRequester.RequestedByMemberID = ""
	err = db.Transaction(func(tx *gorm.DB) error {
		return ApplyTypedTranslationCandidateWithDB(
			t.Context(), tx, store, &providerJobWithoutRequester, providerCandidate,
			translation.EntryWrite{Now: time.Now().UTC()}, auditWriter,
		)
	})
	require.ErrorContains(t, err, "requires its requesting Member")
	requireLabelTargetLocaleAuditCountIntegration(t, db, labelID, 7)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ApplyTypedTranslationCandidateWithDB(t.Context(), tx, store, providerJob, providerCandidate, translation.EntryWrite{Now: time.Now().UTC()}, auditWriter)
	}))
	jaLoaded := loadLabelTargetRoomIntegration(t, internal, sessionID, labelID, "ja")
	require.True(t, jaLoaded.Msg.LocaleExists)
	require.NotNil(t, jaLoaded.Msg.TargetRevision)
	require.Equal(t, sourceEdited.Msg.DocumentRevision, jaLoaded.Msg.DocumentRevision, "provider target delivery must not bump the shared document revision")
	require.Equal(t, sourceEdited.Msg.DocumentRevision, jaLoaded.Msg.DocumentRevision, "provider target delivery must not bump the shared document revision")
	require.Equal(t, 1, labelLocaleOverlayCountIntegration(t, db, labelID, "ja"))

	providerReplacement := &translation.Candidate{
		ContentDocumentRevision: sourceEdited.Msg.DocumentRevision,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: "ja",
		},
		ContentBlockLocaleDeletes: []string{blockID},
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ApplyTypedTranslationCandidateWithDB(t.Context(), tx, store, providerJob, providerReplacement, translation.EntryWrite{Now: time.Now().UTC()}, auditWriter)
	}))
	jaAfterReplacement := loadLabelTargetRoomIntegration(t, internal, sessionID, labelID, "ja")
	require.True(t, jaAfterReplacement.Msg.LocaleExists)
	require.NotNil(t, jaAfterReplacement.Msg.TargetRevision)
	require.NotEqual(t, jaLoaded.Msg.TargetRevision, jaAfterReplacement.Msg.TargetRevision)
	require.Equal(t, sourceEdited.Msg.DocumentRevision, jaAfterReplacement.Msg.DocumentRevision)
	require.Zero(t, labelLocaleOverlayCountIntegration(t, db, labelID, "ja"), "provider whole replacement may remove omitted locale units")
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ApplyTypedTranslationCandidateWithDB(
			t.Context(), tx, store, providerJob, providerReplacement,
			translation.EntryWrite{Now: time.Now().UTC()}, auditWriter,
		)
	}))
	jaAfterNoopReplacement := loadLabelTargetRoomIntegration(t, internal, sessionID, labelID, "ja")
	require.Equal(t, jaAfterReplacement.Msg.TargetRevision, jaAfterNoopReplacement.Msg.TargetRevision)
	requireLabelTargetLocaleAuditsIntegration(t, db, labelID, integrationMemberID(identityID), map[string][]string{
		"de": {string(sharedtelemetry.AuditItemOperationCreated)},
		"fr": {string(sharedtelemetry.AuditItemOperationCreated), string(sharedtelemetry.AuditItemOperationUpdated)},
		"ja": {string(sharedtelemetry.AuditItemOperationCreated), string(sharedtelemetry.AuditItemOperationUpdated)},
		"ko": {
			string(sharedtelemetry.AuditItemOperationCreated),
			string(sharedtelemetry.AuditItemOperationUpdated),
			string(sharedtelemetry.AuditItemOperationUpdated),
			string(sharedtelemetry.AuditItemOperationDeleted),
		},
	})

	syncArtistIntegrationGlobalRole(t, stack.SpiceDBClient, identityID, policyv1.Role.User())
	_, err = internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "ja", ExpectedTargetRevision: jaAfterReplacement.Msg.TargetRevision,
		Batch: labelTargetParagraphBatch(
			sourceEdited.Msg.DocumentRevision, memberID.String(), "ja", blockID, "revoked write",
		),
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "each Collab save must recheck the current exact edit authority")
	require.Equal(t, *jaAfterReplacement.Msg.TargetRevision, labelTargetRevisionIntegration(
		t, db, labelID, sourceEdited.Msg.DocumentRevision, "ja",
	))
	require.Zero(t, labelLocaleOverlayCountIntegration(t, db, labelID, "ja"))
	requireLabelTargetLocaleAuditCountIntegration(t, db, labelID, 9)
}

type labelTargetIntegrationRuntime struct{ Runtime }

func (labelTargetIntegrationRuntime) RequestCurrentWithDB(context.Context, *gorm.DB, string, string) (string, error) {
	return "", nil
}

func executeLabelTargetLifecycleIntegration(
	t *testing.T,
	ctx context.Context,
	service *LabelService,
	labelID string,
	locale string,
	documentRevision string,
	targetRevision *string,
	deleteTarget bool,
) AIDocumentMutationResult {
	t.Helper()
	result, err := service.ExecuteAIDocumentMutation(
		ctx, labelID, locale, AIDocumentExecutionApply,
		func(state AIDocumentState) (AIDocumentMutation, error) {
			memberID, parseErr := uuid.Parse(state.ViewerMemberID)
			if parseErr != nil {
				return AIDocumentMutation{}, parseErr
			}
			return AIDocumentMutation{
				LabelID: labelID, Locale: locale, ExpectedRevision: documentRevision,
				ExpectedTargetRevision: targetRevision, ExpectedSource: state.SourceLocale,
				ExpectedPresence: targetRevision != nil, ContributorMemberID: memberID,
				CreateTranslation: !deleteTarget, DeleteTranslation: deleteTarget,
			}, nil
		},
	)
	require.NoError(t, err)
	return result
}

func executeLabelTargetValueIntegration(
	t *testing.T,
	ctx context.Context,
	service *LabelService,
	labelID string,
	locale string,
	text string,
) AIDocumentMutationResult {
	t.Helper()
	result, err := service.ExecuteAIDocumentMutation(
		ctx, labelID, locale, AIDocumentExecutionApply,
		func(state AIDocumentState) (AIDocumentMutation, error) {
			memberID, parseErr := uuid.Parse(state.ViewerMemberID)
			if parseErr != nil {
				return AIDocumentMutation{}, parseErr
			}
			blockID := state.Document.GetBase().GetNodes()[0].GetBlock().GetId()
			protoBatch := labelTargetParagraphBatch(
				state.Revision, memberID.String(), locale, blockID, text,
			)
			batch, conversionErr := contentblock.BatchFromRichTextProto(state.ContentDocumentID, protoBatch)
			if conversionErr != nil {
				return AIDocumentMutation{}, conversionErr
			}
			return AIDocumentMutation{
				LabelID: state.LabelID, Locale: locale, ExpectedRevision: state.Revision,
				ExpectedTargetRevision: cloneLabelTargetRevision(state.TargetRevision),
				ExpectedSource:         state.SourceLocale, ExpectedPresence: state.LocaleExists,
				ContributorMemberID: memberID, Batch: &batch,
			}, nil
		},
	)
	require.NoError(t, err)
	return result
}

func loadLabelTargetRoomIntegration(
	t *testing.T,
	service *InternalLabelService,
	sessionID string,
	labelID string,
	locale string,
) *connect.Response[intrav1.LoadLabelBlockDocumentResponse] {
	t.Helper()
	response, err := service.LoadLabelBlockDocument(t.Context(), connect.NewRequest(&intrav1.LoadLabelBlockDocumentRequest{
		LabelId: labelID, Locale: locale, Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID},
	}))
	require.NoError(t, err)
	return response
}

func labelTargetParagraphBatch(revision, memberID, locale, blockID, text string) *contentv1.RichTextBlockMutationBatch {
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		ExpectedRevision:        revision, ContributorMemberIds: []string{memberID},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: locale,
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{
				Upsert: &contentv1.UpsertRichTextBlockLocale{Block: labelTargetLocaleOverlay(blockID, locale, text).Blocks[0]},
			}}},
		}},
	}
}

func labelTargetLocaleOverlay(blockID, locale, text string) *contentv1.RichTextLocaleOverlay {
	return &contentv1.RichTextLocaleOverlay{Locale: locale, Blocks: []*contentv1.RichTextBlockLocale{{
		BlockId: blockID,
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Props:   &contentv1.ParagraphLocaleProps{},
			Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}}}},
		}},
	}}}
}

func labelFirstBlockID(t *testing.T, document *contentv1.LocalizedRichTextDocument) string {
	t.Helper()
	require.NotNil(t, document)
	require.NotEmpty(t, document.GetBase().GetNodes())
	return document.GetBase().GetNodes()[0].GetBlock().GetId()
}

func labelFirstBlockText(t *testing.T, document *contentv1.LocalizedRichTextDocument) string {
	t.Helper()
	blockID := labelFirstBlockID(t, document)
	for _, block := range document.GetLocaleOverlay().GetBlocks() {
		if block.GetBlockId() != blockID {
			continue
		}
		return block.GetParagraph().GetContent()[0].GetText().GetText()
	}
	t.Fatalf("localized Block %s is missing", blockID)
	return ""
}

func labelDocumentIDForIntegration(t *testing.T, db *gorm.DB, labelID string) uuid.UUID {
	t.Helper()
	documentID, err := loadLabelContentDocumentID(t.Context(), db, labelID)
	require.NoError(t, err)
	return documentID
}

func labelOwnerIDForIntegration(t *testing.T, db *gorm.DB, labelID string) uuid.UUID {
	t.Helper()
	var memberID string
	require.NoError(t, db.Table("label_owner").Select("member_id::text").Where("label_id = ?::uuid", labelID).Scan(&memberID).Error)
	parsed, err := uuid.Parse(memberID)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, parsed)
	return parsed
}

func labelLocaleOverlayCountIntegration(t *testing.T, db *gorm.DB, labelID, locale string) int {
	t.Helper()
	var count int
	require.NoError(t, db.Raw(`
		SELECT count(*)
		FROM content_block_locale AS locale_row
		JOIN content_block AS block ON block.id = locale_row.block_id
		JOIN label AS root ON root.content_document_id = block.document_id
		WHERE root.id = ?::uuid AND locale_row.locale = ?`, labelID, locale).Scan(&count).Error)
	return count
}

func labelTargetRevisionIntegration(t *testing.T, db *gorm.DB, labelID, documentRevision, locale string) string {
	t.Helper()
	var updatedAt time.Time
	require.NoError(t, db.Table("label_translation").Select("updated_at").
		Where("entity_id = ?::uuid AND locale = ?", labelID, locale).
		Scan(&updatedAt).Error)
	revision, err := deriveLabelTargetRevision(documentRevision, updatedAt)
	require.NoError(t, err)
	return revision
}

func labelTargetMemberContext(t *testing.T, identityID string) context.Context {
	t.Helper()
	base := artistIntegrationAdminCtx(identityID)
	user := auth.GetUser(base)
	require.NotNil(t, user)
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		uuid.NewString(),
		sharedtelemetry.MemberActor{
			IdentityID: identityID,
			MemberID:   integrationMemberID(identityID),
			SessionID:  uuid.NewString(),
		},
	)
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(base, requestContext), user)
}

func requireLabelTargetLocaleAuditsIntegration(
	t *testing.T,
	db *gorm.DB,
	labelID string,
	memberID string,
	want map[string][]string,
) {
	t.Helper()
	var rows []struct {
		Locale        string `gorm:"column:locale"`
		ItemOperation string `gorm:"column:item_operation"`
		ActorMemberID string `gorm:"column:actor_member_id"`
	}
	require.NoError(t, db.Raw(`
		SELECT attributes->>'locale' AS locale,
		       attributes->>'item_operation' AS item_operation,
		       actor_member_id::text AS actor_member_id
		FROM public.domain_audit
		WHERE action = ? AND target_type = 'label' AND target_id = ?
		  AND attributes->'changed_fields' = '["locale_content"]'::jsonb
		  AND attributes->>'locale' <> (SELECT source_locale FROM public.label WHERE id = ?::uuid)
		ORDER BY attributes->>'locale', occurred_at, audit_id
	`, sharedtelemetry.AuditLabelUpdated, labelID, labelID).Scan(&rows).Error)
	got := make(map[string][]string)
	for _, row := range rows {
		require.Equal(t, memberID, row.ActorMemberID)
		got[row.Locale] = append(got[row.Locale], row.ItemOperation)
	}
	require.Equal(t, want, got)
}

func requireLabelTargetLocaleAuditCountIntegration(t *testing.T, db *gorm.DB, labelID string, want int64) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
		SELECT count(*)
		FROM public.domain_audit
		WHERE action = ? AND target_type = 'label' AND target_id = ?
		  AND attributes->'changed_fields' = '["locale_content"]'::jsonb
		  AND attributes->>'locale' <> (SELECT source_locale FROM public.label WHERE id = ?::uuid)
	`, sharedtelemetry.AuditLabelUpdated, labelID, labelID).Scan(&count).Error)
	require.Equal(t, want, count)
}

func requireLabelMutationRejectionIntegration(
	t *testing.T,
	err error,
	want intrav1.CollaborationMutationRejectionReason,
) {
	t.Helper()
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr))
	for _, detail := range connectErr.Details() {
		value, detailErr := detail.Value()
		require.NoError(t, detailErr)
		if rejection, ok := value.(*intrav1.CollaborationMutationRejectionDetail); ok {
			require.Equal(t, want, rejection.Reason)
			return
		}
	}
	t.Fatal("missing CollaborationMutationRejectionDetail")
}
