//go:build integration

package artist

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type artistTranslationAuditAppender struct {
	records []sharedtelemetry.AuditRecord
}

func (a *artistTranslationAuditAppender) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	a.records = append(a.records, record)
	return nil
}

var _ domainaudit.Appender = (*artistTranslationAuditAppender)(nil)

func TestArtistTargetLocaleBatchUsesExactCASWithoutAdvancingSharedRevisionIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	store := testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)
	artistID := seedInternalArtist(t, db, store)
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, db.Exec("UPDATE artist SET source_locale = 'en' WHERE id = ?::uuid", artistID).Error)
	title := "Artist source"
	require.NoError(t, saveArtistSourceLocaleDocumentState(
		t.Context(), db, artistID, "en", translationLocaleDocumentSaveInput{
			SourceLocale: "en", Title: &title, Now: now,
		},
	))
	sharedRevision := attachArtistContentDocument(t, db, store, artistID, "en", "source paragraph")
	documentID, err := loadCreativeContentDocumentID(t.Context(), db, artistContentEntity, artistID)
	require.NoError(t, err)

	koUpdatedAt := now.Add(time.Second)
	jaUpdatedAt := now.Add(2 * time.Second)
	require.NoError(t, db.Exec(`
		INSERT INTO artist_translation (entity_id, locale, title, created_at, updated_at)
		VALUES (?::uuid, 'ko', '한국어', ?, ?), (?::uuid, 'ja', '日本語', ?, ?)
	`, artistID, koUpdatedAt, koUpdatedAt, artistID, jaUpdatedAt, jaUpdatedAt).Error)
	koState, err := loadArtistTargetLocaleState(t.Context(), db, store, artistID, documentID, "ko", false)
	require.NoError(t, err)
	jaState, err := loadArtistTargetLocaleState(t.Context(), db, store, artistID, documentID, "ja", false)
	require.NoError(t, err)
	require.NotEmpty(t, koState.TargetRevision)
	require.NotEmpty(t, jaState.TargetRevision)

	contributor := uuid.New()
	testutil.InsertAuthorizedDocumentContributor(t, db, stack.SpiceDBClient, contributor.String())
	require.Len(t, koState.Snapshot.Blocks, 1)
	batchProto := artistTargetParagraphBatch(
		sharedRevision, contributor.String(), "ko", koState.Snapshot.Blocks[0].ID.String(), "번역 문단",
	)
	batch, err := contentblock.BatchFromRichTextProto(documentID, batchProto)
	require.NoError(t, err)
	var result contentblock.Result
	var nextKOToken string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		result, nextKOToken, err = applyArtistTargetLocaleBatch(
			t.Context(), tx, store, artistID, documentID, "ko", batch,
			&koState.TargetRevision, false, now.Add(3*time.Second),
			internalCreativeContentFence(artistContentEntity, artistID),
		)
		return err
	}))
	require.True(t, result.Changed)
	require.Equal(t, sharedRevision, result.DocumentRevision.String())
	require.NotEqual(t, koState.TargetRevision, nextKOToken)

	nextJA, err := loadArtistTargetLocaleState(t.Context(), db, store, artistID, documentID, "ja", false)
	require.NoError(t, err)
	require.Equal(t, jaState.TargetRevision, nextJA.TargetRevision)
	var storedRevision string
	require.NoError(t, db.Raw("SELECT revision::text FROM content_document WHERE id = ?", documentID).Scan(&storedRevision).Error)
	require.Equal(t, sharedRevision, storedRevision)

	replacementTitle := "교체된 한국어 제목"
	replacement := &translation.Candidate{
		Title:                     &replacementTitle,
		ContentDocumentRevision:   sharedRevision,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
		ContentBlockLocaleDeletes: []string{koState.Snapshot.Blocks[0].ID.String()},
	}
	replacementJob := &model.TranslationJob{
		EntityType: artistContentEntity, EntityID: artistID, SourceLocale: "en", TargetLocale: "ko",
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ApplyTypedTranslationInterchangeCandidateWithDB(
			t.Context(), tx, store, replacementJob, replacement,
			translation.EntryWrite{Now: now.Add(4 * time.Second)}, &nextKOToken,
		)
	}))
	providerState, err := loadArtistTargetLocaleState(t.Context(), db, store, artistID, documentID, "ko", false)
	require.NoError(t, err)
	require.Equal(t, sharedRevision, providerState.Snapshot.Document.Revision.String())
	require.NotEqual(t, nextKOToken, providerState.TargetRevision)
	require.NotNil(t, providerState.TargetMetadata)
	require.Equal(t, replacementTitle, *providerState.TargetMetadata.Title)
	require.False(t, artistSnapshotContainsLocale(providerState.Snapshot, "ko"))
	nextKOToken = providerState.TargetRevision

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		result, err = deleteArtistTargetLocale(
			t.Context(), tx, store, artistID, documentID, "ko", result.DocumentRevision,
			&nextKOToken, []uuid.UUID{contributor},
			internalCreativeContentFence(artistContentEntity, artistID),
		)
		return err
	}))
	require.True(t, result.Changed)
	require.Equal(t, sharedRevision, result.DocumentRevision.String())
	var koCount int64
	require.NoError(t, db.Table("artist_translation").Where("entity_id = ?::uuid AND locale = 'ko'", artistID).Count(&koCount).Error)
	require.Zero(t, koCount)
	loaded, err := store.LoadSnapshot(context.Background(), db, documentID, "en")
	require.NoError(t, err)
	require.False(t, artistSnapshotContainsLocale(loaded, "ko"))

	providerLocale := "fr"
	providerTitle := "Titre fourni"
	requester := uuid.NewString()
	providerJob := &model.TranslationJob{
		EntityType: artistContentEntity, EntityID: artistID, SourceLocale: "en", TargetLocale: providerLocale,
		RequestedByMemberID: requester,
	}
	providerCandidate := &translation.Candidate{
		Title:                     &providerTitle,
		ContentDocumentRevision:   sharedRevision,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: providerLocale},
	}
	audit := &artistTranslationAuditAppender{}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ApplyTypedTranslationCandidateWithDB(
			t.Context(), tx, store, providerJob, providerCandidate,
			translation.EntryWrite{Now: now.Add(5 * time.Second)}, audit,
		)
	}))
	require.Len(t, audit.records, 1)
	providerTarget, err := loadArtistTargetLocaleState(
		t.Context(), db, store, artistID, documentID, providerLocale, false,
	)
	require.NoError(t, err)
	require.NotNil(t, providerTarget.TargetMetadata)
	require.Equal(t, providerTitle, *providerTarget.TargetMetadata.Title)
	require.Equal(t, sharedRevision, providerTarget.Snapshot.Document.Revision.String())
}

func artistTargetParagraphBatch(
	expectedRevision string,
	contributorMemberID string,
	locale string,
	blockID string,
	value string,
) *contentv1.RichTextBlockMutationBatch {
	content := []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
		Text: &contentv1.RichTextStyledText{Text: value},
	}}}
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		ExpectedRevision:        expectedRevision,
		ContributorMemberIds:    []string{contributorMemberID},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: locale,
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{
				Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
					Block: &contentv1.RichTextBlockLocale{
						BlockId: blockID,
						Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
							Props: &contentv1.ParagraphLocaleProps{}, Content: content,
						}},
					},
				}},
			}},
		}},
	}
}
