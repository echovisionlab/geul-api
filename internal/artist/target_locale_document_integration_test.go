//go:build integration

package artist_test

import (
	"context"
	"testing"
	"time"

	artist "github.com/echovisionlab/geul-api/internal/artist"
	artistpostgres "github.com/echovisionlab/geul-api/internal/artist/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
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
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	store := testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)
	identityID := uuid.NewString()
	contributor := uuid.MustParse(seedArtistIdentity(t, db, identityID, "Artist target admin"))
	requireInternalResourceAdmin(t, stack.SpiceDBClient, identityID)
	service := newTestService(t, integrationDeps{DB: db, SpiceDB: stack.SpiceDBClient, ContentBlocks: store})
	artistID := seedInternalArtist(t, db, store)
	attachInternalResourcePolicy(t, stack.SpiceDBClient, artistID)
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, db.Exec("UPDATE artist SET source_locale = 'en' WHERE id = ?::uuid", artistID).Error)
	title := "Artist source"
	seedArtistSourceTitle(t, db, artistID, "en", title, now)
	sharedRevision := attachArtistContentDocument(t, db, store, artistID, "en", "source paragraph")
	documentID := artistDocumentIDForIntegration(t, db, artistID)
	ctx := auth.WithUser(artistDocumentContributorContext(t), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(contributor.String()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})

	koUpdatedAt := now.Add(time.Second)
	jaUpdatedAt := now.Add(2 * time.Second)
	require.NoError(t, db.Exec(`
		INSERT INTO artist_translation (entity_id, locale, title, created_at, updated_at)
		VALUES (?::uuid, 'ko', '한국어', ?, ?), (?::uuid, 'ja', '日本語', ?, ?)
	`, artistID, koUpdatedAt, koUpdatedAt, artistID, jaUpdatedAt, jaUpdatedAt).Error)
	koRevision := artistTargetRevisionForIntegration(t, db, store, artistID, "ko")
	jaRevision := artistTargetRevisionForIntegration(t, db, store, artistID, "ja")
	require.NotEmpty(t, koRevision)
	require.NotEmpty(t, jaRevision)

	source, err := store.LoadSnapshot(t.Context(), db, documentID, "en")
	require.NoError(t, err)
	require.Len(t, source.Blocks, 1)
	batchProto := artistTargetParagraphBatch(
		sharedRevision, contributor.String(), "ko", source.Blocks[0].ID.String(), "번역 문단",
	)
	batch, err := contentblock.BatchFromRichTextProto(documentID, batchProto)
	require.NoError(t, err)
	result, err := service.ExecuteAIDocumentMutation(ctx, artistID, "ko", artist.AIDocumentExecutionApply,
		func(artist.AIDocumentState) (artist.AIDocumentMutation, error) {
			return artist.AIDocumentMutation{
				ArtistID: artistID, Locale: "ko", ExpectedRevision: sharedRevision,
				ExpectedTargetRevision: &koRevision, ExpectedSource: "en", ExpectedPresence: true,
				ContributorMemberID: contributor, Batch: &batch,
			}, nil
		})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, sharedRevision, result.Revision)
	require.NotNil(t, result.TargetRevision)
	require.NotEqual(t, koRevision, *result.TargetRevision)
	nextKOToken := *result.TargetRevision

	require.Equal(t, jaRevision, artistTargetRevisionForIntegration(t, db, store, artistID, "ja"))
	var storedRevision string
	require.NoError(t, db.Raw("SELECT revision::text FROM content_document WHERE id = ?", documentID).Scan(&storedRevision).Error)
	require.Equal(t, sharedRevision, storedRevision)

	replacementTitle := "교체된 한국어 제목"
	replacement := &translation.Candidate{
		Title:                     &replacementTitle,
		ContentDocumentRevision:   sharedRevision,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
		ContentBlockLocaleDeletes: []string{source.Blocks[0].ID.String()},
	}
	replacementJob := artist.TranslationJob{EntityType: "artist", EntityID: artistID, TargetLocale: "ko"}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return artist.ApplyTypedTranslationInterchangeCandidate(
			t.Context(), artistpostgres.Bind(tx, artistpostgres.Deps{ContentBlocks: store}), replacementJob, replacement,
			translation.EntryWrite{Now: now.Add(4 * time.Second)}, &nextKOToken,
		)
	}))
	providerSnapshot, err := store.LoadSnapshot(t.Context(), db, documentID, "en")
	require.NoError(t, err)
	require.Equal(t, sharedRevision, providerSnapshot.Document.Revision.String())
	providerRevision := artistTargetRevisionForIntegration(t, db, store, artistID, "ko")
	require.NotEqual(t, nextKOToken, providerRevision)
	require.Equal(t, replacementTitle, artistLocaleTitleForIntegration(t, db, artistID, "ko"))
	require.False(t, targetlocale.SnapshotHasLocale(providerSnapshot, "ko"))
	nextKOToken = providerRevision

	result, err = service.ExecuteAIDocumentMutation(ctx, artistID, "ko", artist.AIDocumentExecutionApply,
		func(artist.AIDocumentState) (artist.AIDocumentMutation, error) {
			return artist.AIDocumentMutation{
				ArtistID: artistID, Locale: "ko", ExpectedRevision: sharedRevision,
				ExpectedTargetRevision: &nextKOToken, ExpectedSource: "en", ExpectedPresence: true,
				ContributorMemberID: contributor, DeleteTranslation: true,
			}, nil
		})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, sharedRevision, result.Revision)
	var koCount int64
	require.NoError(t, db.Table("artist_translation").Where("entity_id = ?::uuid AND locale = 'ko'", artistID).Count(&koCount).Error)
	require.Zero(t, koCount)
	loaded, err := store.LoadSnapshot(context.Background(), db, documentID, "en")
	require.NoError(t, err)
	require.False(t, targetlocale.SnapshotHasLocale(loaded, "ko"))

	providerLocale := "fr"
	providerTitle := "Titre fourni"
	requester := uuid.NewString()
	providerJob := artist.TranslationJob{
		EntityType: "artist", EntityID: artistID, TargetLocale: providerLocale, RequestedByMemberID: requester,
	}
	providerCandidate := &translation.Candidate{
		Title:                     &providerTitle,
		ContentDocumentRevision:   sharedRevision,
		ContentBlockLocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: providerLocale},
	}
	audit := &artistTranslationAuditAppender{}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return artist.ApplyTypedTranslationCandidate(
			t.Context(), artistpostgres.Bind(tx, artistpostgres.Deps{ContentBlocks: store}), providerJob, providerCandidate,
			translation.EntryWrite{Now: now.Add(5 * time.Second)}, audit,
		)
	}))
	require.Len(t, audit.records, 1)
	require.Equal(t, providerTitle, artistLocaleTitleForIntegration(t, db, artistID, providerLocale))
	providerTarget, err := store.LoadSnapshot(t.Context(), db, documentID, "en")
	require.NoError(t, err)
	require.Equal(t, sharedRevision, providerTarget.Document.Revision.String())
}

// artistTargetRevisionForIntegration is the target revision of the Artist's
// locale a collaboration or AI write must name.
func artistTargetRevisionForIntegration(t *testing.T, db *gorm.DB, store *contentblock.Store, artistID, locale string) string {
	t.Helper()
	target, err := artist.LoadTypedTranslationInterchangeTarget(
		t.Context(), artistpostgres.Bind(db, artistpostgres.Deps{ContentBlocks: store}), artistID, locale,
	)
	require.NoError(t, err)
	return target.Revision
}

func artistLocaleTitleForIntegration(t *testing.T, db *gorm.DB, artistID, locale string) string {
	t.Helper()
	var title *string
	require.NoError(t, db.Table("artist_translation").Select("title").
		Where("entity_id = ?::uuid AND locale = ?", artistID, locale).Scan(&title).Error)
	require.NotNil(t, title)
	return *title
}

func artistDocumentIDForIntegration(t *testing.T, db *gorm.DB, artistID string) uuid.UUID {
	t.Helper()
	var documentID string
	require.NoError(t, db.Table("artist").Select("content_document_id::text").Where("id = ?", artistID).Scan(&documentID).Error)
	return uuid.MustParse(documentID)
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

func seedInternalArtist(t *testing.T, db *gorm.DB, contentBlocks *contentblock.Store) string {
	t.Helper()

	artistID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		document, err := contentBlocks.CreateDocument(t.Context(), tx, contentblock.CreateInput{
			Profile: "compact", SourceLocale: "en",
		})
		if err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO artist (id, status, source_locale, content_document_id, created_at, updated_at)
			VALUES (?, 'ARTIST_STATUS_DRAFT', 'en', ?, ?, ?)
		`, artistID, document.Document.ID, now, now).Error
	}))
	return artistID
}

func requireInternalResourceAdmin(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Admin())
	require.NoError(t, err)
}

func attachInternalResourcePolicy(t *testing.T, spiceDB *auth.SpiceDBClient, resourceID string) {
	t.Helper()
	mutation, err := policyv1.Artist.TouchPolicy(resourceID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}
