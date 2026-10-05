//go:build integration

package release

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type releaseTargetIntegrationFileAuthorizer struct{}

func (releaseTargetIntegrationFileAuthorizer) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

func TestReleaseTargetLocaleCASIsIndependentAndDeletePreservesSharedRevisionIntegration(t *testing.T) {
	postgres := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true})
	store, err := contentblock.NewGeneratedStore(releaseTargetIntegrationFileAuthorizer{})
	require.NoError(t, err)

	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	releaseID, documentID := uuid.NewString(), uuid.New()
	firstBlock, secondBlock, creditID := uuid.New(), uuid.New(), uuid.NewString()
	var created contentblock.Snapshot
	require.NoError(t, postgres.DB.Transaction(func(tx *gorm.DB) error {
		var createErr error
		created, createErr = store.CreateDocument(ctx, tx, contentblock.CreateInput{ID: documentID, Profile: releaseContentProfile, SourceLocale: "en"})
		return createErr
	}))
	replace, err := contentblock.ReplaceFromRichTextProto(documentID, created.Document.Revision, releaseTargetIntegrationDocument(firstBlock, secondBlock))
	require.NoError(t, err)
	var seeded contentblock.Result
	require.NoError(t, postgres.DB.Transaction(func(tx *gorm.DB) error {
		var replaceErr error
		seeded, replaceErr = store.ReplaceSnapshot(ctx, tx, replace, func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: "en"}, nil
		})
		return replaceErr
	}))
	documentIDString := documentID.String()
	require.NoError(t, postgres.DB.Create(&model.Release{ID: releaseID, ContentDocumentID: &documentIDString, SourceLocale: "en"}).Error)
	require.NoError(t, postgres.DB.Exec("INSERT INTO release_translation (entity_id, locale, title, created_at, updated_at) VALUES (?::uuid, 'en', 'Source title', ?, ?)", releaseID, now, now).Error)
	creditedName := "Source credit"
	require.NoError(t, postgres.DB.Create(&model.ReleaseCredit{ID: creditID, ReleaseID: releaseID, CreditedName: &creditedName, CreatedAt: now}).Error)
	require.NoError(t, postgres.DB.Exec("INSERT INTO release_credit_locale (credit_id, locale, note, updated_at) VALUES (?::uuid, 'en', 'Source note', ?)", creditID, now).Error)

	frBatch := releaseTargetIntegrationBatch(t, documentID, seeded.DocumentRevision, firstBlock, "fr", "Bonjour")
	frTitle := "Titre"
	var fr releaseTargetLocaleMutationResult
	require.NoError(t, postgres.DB.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		fr, applyErr = applyReleaseTargetLocaleMutation(ctx, tx, store, releaseTargetLocaleMutationInput{
			ReleaseID: releaseID, DocumentID: documentID, Locale: "fr",
			ExpectedDocumentRevision: seeded.DocumentRevision, AllowCreate: true,
			Batch: frBatch, SetTitle: true, Title: &frTitle,
			CreditNotePatch: map[string]string{creditID: "Note francaise"},
			Now:             now.Add(time.Second), Fence: internalReleaseContentFence(releaseID),
		})
		return applyErr
	}))
	require.True(t, fr.Content.Changed)
	require.Equal(t, seeded.DocumentRevision, fr.Content.DocumentRevision)
	require.NotEmpty(t, fr.TargetRevision)

	frSnapshot, err := store.LoadSnapshot(ctx, postgres.DB, documentID, "en")
	require.NoError(t, err)
	require.Equal(t, seeded.DocumentRevision, frSnapshot.Document.Revision)
	frDocument, err := contentblock.SnapshotToLocalizedRichTextDocument(frSnapshot, "fr")
	require.NoError(t, err)
	require.Len(t, frDocument.GetLocaleOverlay().GetBlocks(), 2, "absent target creation must seed every source block")
	var frMetadata releaseLocaleMetadataRow
	require.NoError(t, postgres.DB.Table("release_translation").Where("entity_id = ?::uuid AND locale = 'fr'", releaseID).Take(&frMetadata).Error)
	require.NotNil(t, frMetadata.Title)
	require.Equal(t, frTitle, *frMetadata.Title)
	frNotes, err := loadReleaseCreditLocaleNotes(ctx, postgres.DB, releaseID, "fr")
	require.NoError(t, err)
	require.Equal(t, map[string]string{creditID: "Note francaise"}, frNotes)

	interactiveDelete := contentblock.Batch{
		DocumentID:       documentID,
		ExpectedRevision: seeded.DocumentRevision,
		LocaleGroups: []contentblock.LocaleMutationGroup{{
			Locale: "fr", Deletes: []uuid.UUID{firstBlock},
		}},
	}
	err = postgres.DB.Transaction(func(tx *gorm.DB) error {
		_, applyErr := applyReleaseTargetLocaleMutation(ctx, tx, store, releaseTargetLocaleMutationInput{
			ReleaseID: releaseID, DocumentID: documentID, Locale: "fr",
			ExpectedDocumentRevision: seeded.DocumentRevision, ExpectedTargetRevision: &fr.TargetRevision,
			Batch: interactiveDelete, Now: now.Add(2 * time.Second), Fence: internalReleaseContentFence(releaseID),
		})
		return applyErr
	})
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	frAfterRejectedDelete, err := loadReleaseTargetLocaleState(ctx, postgres.DB, store, releaseID, documentID, "fr", false)
	require.NoError(t, err)
	require.Equal(t, fr.TargetRevision, frAfterRejectedDelete.TargetRevision)

	koBatch := releaseTargetIntegrationBatch(t, documentID, seeded.DocumentRevision, secondBlock, "ko", "둘째")
	var ko releaseTargetLocaleMutationResult
	require.NoError(t, postgres.DB.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		ko, applyErr = applyReleaseTargetLocaleMutation(ctx, tx, store, releaseTargetLocaleMutationInput{
			ReleaseID: releaseID, DocumentID: documentID, Locale: "ko",
			ExpectedDocumentRevision: seeded.DocumentRevision, AllowCreate: true,
			Batch: koBatch, Now: now.Add(3 * time.Second), Fence: internalReleaseContentFence(releaseID),
		})
		return applyErr
	}))

	frSecond := releaseTargetIntegrationBatch(t, documentID, seeded.DocumentRevision, secondBlock, "fr", "Deuxieme")
	require.NoError(t, postgres.DB.Transaction(func(tx *gorm.DB) error {
		_, applyErr := applyReleaseTargetLocaleMutation(ctx, tx, store, releaseTargetLocaleMutationInput{
			ReleaseID: releaseID, DocumentID: documentID, Locale: "fr",
			ExpectedDocumentRevision: seeded.DocumentRevision, ExpectedTargetRevision: &fr.TargetRevision,
			Batch: frSecond, Now: now.Add(4 * time.Second), Fence: internalReleaseContentFence(releaseID),
		})
		return applyErr
	}))
	koState, err := loadReleaseTargetLocaleState(ctx, postgres.DB, store, releaseID, documentID, "ko", false)
	require.NoError(t, err)
	require.Equal(t, ko.TargetRevision, koState.TargetRevision, "another locale write must not invalidate this target token")

	var targetConflict *translation.TargetRevisionConflict
	err = postgres.DB.Transaction(func(tx *gorm.DB) error {
		_, applyErr := applyReleaseTargetLocaleMutation(ctx, tx, store, releaseTargetLocaleMutationInput{
			ReleaseID: releaseID, DocumentID: documentID, Locale: "fr",
			ExpectedDocumentRevision: seeded.DocumentRevision, ExpectedTargetRevision: &fr.TargetRevision,
			Batch: frSecond, Now: now.Add(5 * time.Second), Fence: internalReleaseContentFence(releaseID),
		})
		return applyErr
	})
	require.True(t, errors.As(err, &targetConflict))

	frState, err := loadReleaseTargetLocaleState(ctx, postgres.DB, store, releaseID, documentID, "fr", false)
	require.NoError(t, err)
	require.NoError(t, postgres.DB.Transaction(func(tx *gorm.DB) error {
		_, deleteErr := deleteReleaseTargetLocale(ctx, tx, store, releaseID, documentID, "fr", seeded.DocumentRevision, &frState.TargetRevision, nil, internalReleaseContentFence(releaseID))
		return deleteErr
	}))
	afterDelete, err := store.LoadSnapshot(ctx, postgres.DB, documentID, "en")
	require.NoError(t, err)
	require.Equal(t, seeded.DocumentRevision, afterDelete.Document.Revision)
	_, exists, err := loadOptionalReleaseLocaleMetadataRow(ctx, postgres.DB, releaseID, "fr", false)
	require.NoError(t, err)
	require.False(t, exists)
}

func releaseTargetIntegrationDocument(first, second uuid.UUID) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, SourceLocale: "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{
			{Block: &contentv1.RichTextBlock{Id: first.String(), Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}}, Placement: &contentv1.ContentBlockPlacement{Index: 0}},
			{Block: &contentv1.RichTextBlock{Id: second.String(), Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}}, Placement: &contentv1.ContentBlockPlacement{Index: 1}},
		}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{
			releaseTargetIntegrationParagraph(first, "First"), releaseTargetIntegrationParagraph(second, "Second"),
		}}},
	}
}

func releaseTargetIntegrationParagraph(blockID uuid.UUID, text string) *contentv1.RichTextBlockLocale {
	return &contentv1.RichTextBlockLocale{BlockId: blockID.String(), Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{Props: &contentv1.ParagraphLocaleProps{}, Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}}}}}}}
}

func releaseTargetIntegrationBatch(t *testing.T, documentID, revision, blockID uuid.UUID, locale, text string) contentblock.Batch {
	t.Helper()
	batch, err := contentblock.BatchFromRichTextSystemProto(documentID, &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, ExpectedRevision: revision.String(),
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{Locale: locale, Mutations: []*contentv1.RichTextBlockLocaleMutation{{Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{Block: releaseTargetIntegrationParagraph(blockID, text)}}}}}},
	})
	require.NoError(t, err)
	return batch
}
