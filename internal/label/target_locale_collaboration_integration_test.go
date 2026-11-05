//go:build integration

package label_test

import (
	"context"
	"errors"
	"testing"
	"time"

	label "github.com/echovisionlab/geul-api/internal/label"
	labelpostgres "github.com/echovisionlab/geul-api/internal/label/postgres"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/optional"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLabelTargetLocaleCASSeedAndDeleteIntegration(t *testing.T) {
	t.Parallel()
	db := orytest.NewIntegrationDB(t)
	stack := orytest.SetupOryStack(t)
	identityID := uuid.NewString()
	orytest.SeedPostIntegrationIdentity(t, db, identityID, "Label target admin")
	requireInternalResourceAdmin(t, stack.SpiceDBClient, identityID)
	store := newCreativeContentIntegrationStore(t, stack.SpiceDBClient)
	labelID := seedInternalLabel(t, db, store, orytest.PostIntegrationMemberID(identityID))
	attachInternalResourcePolicy(t, stack.SpiceDBClient, labelID)
	now := time.Now().UTC().Truncate(time.Microsecond)
	title := "Label target locale"
	require.NoError(t, seedLabelLocaleTitle(t.Context(), db, labelID, "en", title, now))
	documentRevision := attachCreativeContentIntegrationDocument(
		t, db, store, "label", labelID, "en", "source description",
	)
	documentID := labelDocumentIDForIntegration(t, db, labelID)
	memberID := labelOwnerIDForIntegration(t, db, labelID)
	sessionID := orytest.InsertPostIntegrationSession(t, db, identityID)
	auditWriter := apitelemetry.NewDurableWriter(db)
	memberCtx := labelTargetMemberContext(t, identityID)
	aiService := newTestService(t, integrationDeps{
		DB: db, SpiceDB: stack.SpiceDBClient, Audit: auditWriter, Publisher: &capturingAsyncPublisher{},
		ContentBlocks: store, Runtime: labelTargetIntegrationRuntime{},
	})
	internal := label.NewInternalConnectHandler(aiService)

	missing := loadLabelTargetRoomIntegration(t, internal, sessionID, labelID, "ko")
	requireLabelMissingTargetRoomIntegration(t, missing.Msg, documentRevision)
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

	requireLabelTargetBlockDeletesRejectedIntegration(t, memberCtx, internal, aiService, labelID, documentID, documentRevision, memberID, blockID, koEdited.Msg.TargetRevision)

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

	requireLabelTargetForbiddenMutationsIntegration(t, memberCtx, internal, labelID, documentRevision, memberID, blockID, explicitEmpty.Msg.TargetRevision)

	sourceEdited, err := internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "en",
		Batch: labelTargetParagraphBatch(documentRevision, memberID.String(), "en", blockID, "source changed"),
	}))
	require.NoError(t, err)
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
}

// requireLabelMissingTargetRoomIntegration checks that a room for a missing
// "ko" target renders the current "en" source without a target revision.
func requireLabelMissingTargetRoomIntegration(t *testing.T, missing *intrav1.LoadLabelBlockDocumentResponse, documentRevision string) {
	t.Helper()
	require.False(t, missing.LocaleExists)
	require.Nil(t, missing.TargetRevision)
	require.Equal(t, documentRevision, missing.DocumentRevision)
	require.Equal(t, "en", missing.SourceMetadata.GetLocale())
	require.Equal(t, "ko", missing.GetLocale())
	require.Equal(t, "source description", labelFirstBlockText(t, missing.Document),
		"a target collaboration room must render missing target units from the current source")
}

// requireLabelTargetBlockDeletesRejectedIntegration checks that neither a
// collaboration batch nor a DCDP mutation may delete a target locale block.
func requireLabelTargetBlockDeletesRejectedIntegration(
	t *testing.T,
	memberCtx context.Context,
	internal *label.InternalConnectHandler,
	aiService *label.Service,
	labelID string,
	documentID uuid.UUID,
	documentRevision string,
	memberID uuid.UUID,
	blockID string,
	targetRevision *string,
) {
	t.Helper()
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
	_, err := internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "ko", ExpectedTargetRevision: targetRevision, Batch: localeDelete,
	}))
	requireLabelMutationRejectionIntegration(
		t, err,
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
	)

	_, dcdpDeleteErr := aiService.ExecuteAIDocumentMutation(memberCtx, labelID, "ko", label.AIDocumentExecutionApply,
		func(label.AIDocumentState) (label.AIDocumentMutation, error) {
			return label.AIDocumentMutation{
				LabelID: labelID, Locale: "ko", ExpectedRevision: documentRevision,
				ExpectedTargetRevision: targetRevision, ExpectedSource: "en", ExpectedPresence: true,
				ContributorMemberID: memberID,
				Batch: &contentblock.Batch{
					DocumentID: documentID, ExpectedRevision: uuid.MustParse(documentRevision),
					ContributorMemberIDs: []uuid.UUID{memberID},
					LocaleGroups:         []contentblock.LocaleMutationGroup{{Locale: "ko", Deletes: []uuid.UUID{uuid.MustParse(blockID)}}},
				},
			}, nil
		})
	requireLabelMutationRejectionIntegration(
		t, dcdpDeleteErr,
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
	)
}

// requireLabelTargetForbiddenMutationsIntegration checks that a target locale
// rejects structural, shared-field, file-relation and document-metadata edits.
func requireLabelTargetForbiddenMutationsIntegration(
	t *testing.T,
	memberCtx context.Context,
	internal *label.InternalConnectHandler,
	labelID string,
	documentRevision string,
	memberID uuid.UUID,
	blockID string,
	targetRevision *string,
) {
	t.Helper()
	structural := labelTargetParagraphBatch(documentRevision, memberID.String(), "ko", blockID, "forbidden")
	structural.BaseMutations = []*contentv1.RichTextBlockMutation{{Operation: &contentv1.RichTextBlockMutation_Delete{
		Delete: &contentv1.DeleteRichTextBlock{BlockId: blockID},
	}}}
	_, err := internal.ApplyLabelBlockBatch(memberCtx, connect.NewRequest(&intrav1.ApplyLabelBlockBatchRequest{
		LabelId: labelID, Locale: "ko", ExpectedTargetRevision: targetRevision, Batch: structural,
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
		LabelId: labelID, Locale: "ko", ExpectedTargetRevision: targetRevision, Batch: shared,
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
		LabelId: labelID, Locale: "ko", ExpectedTargetRevision: targetRevision, Batch: fileMutation,
	}))
	requireLabelMutationRejectionIntegration(
		t, err,
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_FILE_RELATION_FORBIDDEN,
	)
	targetTitle := "forbidden target title"
	_, err = internal.UpdateLabelLocaleMetadata(memberCtx, connect.NewRequest(&intrav1.UpdateLabelLocaleMetadataRequest{
		LabelId: labelID, Locale: "ko", ExpectedRevision: documentRevision,
		ExpectedTargetRevision: targetRevision, ContributorMemberIds: []string{memberID.String()},
		Title: &targetTitle,
	}))
	requireLabelMutationRejectionIntegration(
		t, err,
		intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_DOCUMENT_METADATA_FORBIDDEN,
	)
}

type labelTargetIntegrationRuntime struct{ labelpostgres.Runtime }

func (labelTargetIntegrationRuntime) RequestCurrentWithDB(context.Context, *gorm.DB, string, string) (string, error) {
	return "", nil
}

func executeLabelTargetLifecycleIntegration(
	t *testing.T,
	ctx context.Context,
	service *label.Service, labelID string,
	locale string,
	documentRevision string,
	targetRevision *string,
	deleteTarget bool,
) label.AIDocumentMutationResult {
	t.Helper()
	result, err := service.ExecuteAIDocumentMutation(
		ctx, labelID, locale, label.AIDocumentExecutionApply,
		func(state label.AIDocumentState) (label.AIDocumentMutation, error) {
			memberID, parseErr := uuid.Parse(state.ViewerMemberID)
			if parseErr != nil {
				return label.AIDocumentMutation{}, parseErr
			}
			return label.AIDocumentMutation{
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
	service *label.Service, labelID string,
	locale string,
	text string,
) label.AIDocumentMutationResult {
	t.Helper()
	result, err := service.ExecuteAIDocumentMutation(
		ctx, labelID, locale, label.AIDocumentExecutionApply,
		func(state label.AIDocumentState) (label.AIDocumentMutation, error) {
			memberID, parseErr := uuid.Parse(state.ViewerMemberID)
			if parseErr != nil {
				return label.AIDocumentMutation{}, parseErr
			}
			blockID := state.Document.GetBase().GetNodes()[0].GetBlock().GetId()
			protoBatch := labelTargetParagraphBatch(
				state.Revision, memberID.String(), locale, blockID, text,
			)
			batch, conversionErr := contentblock.BatchFromRichTextProto(state.ContentDocumentID, protoBatch)
			if conversionErr != nil {
				return label.AIDocumentMutation{}, conversionErr
			}
			return label.AIDocumentMutation{
				LabelID: state.LabelID, Locale: locale, ExpectedRevision: state.Revision,
				ExpectedTargetRevision: optional.Clone(state.TargetRevision),
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
	service *label.InternalConnectHandler, sessionID string,
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
	var documentID string
	require.NoError(t, db.Table("label").Select("content_document_id::text").Where("id = ?", labelID).Scan(&documentID).Error)
	parsed, err := uuid.Parse(documentID)
	require.NoError(t, err)
	return parsed
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
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	require.NoError(t, err)
	return revision
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
