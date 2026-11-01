//go:build integration

package legalpublicpostgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/legal/public"
	legalpublicpostgres "github.com/echovisionlab/geul-api/internal/legal/public/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// TestPublicStoreReadsPublishedVersionsIntegration pins the public reads'
// SQL: which statuses and effective windows each read admits, their order,
// Share Link expiry and binding, and the content document a version names.
func TestPublicStoreReadsPublishedVersionsIntegration(t *testing.T) {
	t.Parallel()
	db := newPublicIntegrationDB(t)
	contents := newPublicLegalContentBlockStore(t)
	units, err := legalpublicpostgres.NewUnits(db, contents)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	at := func(offset time.Duration) *time.Time { value := now.Add(offset); return &value }
	privacy := func(version int, status managev1.PrivacyStatus, from, until *time.Time) model.Privacy {
		return seedPublicPrivacyPolicy(t, db, contents, version, "Privacy "+status.String(), "Body", status.String(), from, until)
	}
	older := privacy(2, managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED, at(-96*time.Hour), at(-72*time.Hour))
	archived := privacy(3, managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED, at(-48*time.Hour), at(-time.Hour))
	current := privacy(4, managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE, at(-time.Hour), nil)
	next := privacy(6, managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED, at(time.Hour), nil)
	draft := privacy(7, managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT, nil, nil)
	// A kind holds one active version; an expired one is a Terms version.
	seedPublicTermsPolicy(t, db, contents, 1, "Expired terms", "Body", managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(), at(-72*time.Hour), at(-48*time.Hour))
	hash, err := crypto.NewPasswordHasher(nil).Hash("secret")
	require.NoError(t, err)
	shareType := managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String()
	require.NoError(t, db.Create(&[]model.ShareLink{
		{ID: uuid.NewString(), Token: "open", EntityType: shareType, EntityID: next.ID, ExpiresAt: at(time.Hour), PasswordHash: &hash, CreatedAt: now},
		{ID: uuid.NewString(), Token: "expired", EntityType: shareType, EntityID: next.ID, ExpiresAt: at(-time.Minute), CreatedAt: now},
	}).Error)

	require.NoError(t, units.Read(t.Context(), func(store public.Store) error {
		ctx := context.Background()
		published, found, err := store.PublishedVersion(ctx, legaldomain.Privacy, archived.ID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, public.Version{
			ID: archived.ID, Version: 3, Title: archived.Title, Status: legaldomain.PrivacyArchived,
			EffectiveFrom: published.EffectiveFrom, EffectiveUntil: published.EffectiveUntil,
		}, published)
		require.True(t, archived.EffectiveFrom.Equal(*published.EffectiveFrom))
		for _, hidden := range []string{next.ID, draft.ID, uuid.NewString()} {
			_, found, err = store.PublishedVersion(ctx, legaldomain.Privacy, hidden)
			require.NoError(t, err)
			require.False(t, found)
		}
		_, found, err = store.PublishedVersion(ctx, legaldomain.Terms, archived.ID)
		require.NoError(t, err)
		require.False(t, found, "a Privacy version is no Terms version")

		inStatus, found, err := store.VersionInStatus(ctx, legaldomain.Privacy, next.ID, legaldomain.PrivacyScheduled)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, next.ID, inStatus.ID)
		_, found, err = store.VersionInStatus(ctx, legaldomain.Privacy, next.ID, legaldomain.PrivacyActive)
		require.NoError(t, err)
		require.False(t, found)

		effective, found, err := store.EffectiveVersion(ctx, legaldomain.Privacy, now)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, current.ID, effective.ID)
		_, found, err = store.EffectiveVersion(ctx, legaldomain.Terms, now)
		require.NoError(t, err)
		require.False(t, found, "an active version past its effective_until is not current")
		scheduled, found, err := store.NextScheduledVersion(ctx, legaldomain.Privacy, now)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, next.ID, scheduled.ID)
		_, found, err = store.NextScheduledVersion(ctx, legaldomain.Privacy, now.Add(3*time.Hour))
		require.NoError(t, err)
		require.False(t, found, "a scheduled version due before now is no longer next")

		total, err := store.CountArchived(ctx, legaldomain.Privacy)
		require.NoError(t, err)
		require.EqualValues(t, 2, total)
		page, err := store.ListArchived(ctx, legaldomain.Privacy, 1, 0)
		require.NoError(t, err)
		require.Equal(t, archived.ID, page[0].ID, "the latest effective first")
		page, err = store.ListArchived(ctx, legaldomain.Privacy, 10, 1)
		require.NoError(t, err)
		require.Len(t, page, 1)
		require.Equal(t, older.ID, page[0].ID)

		link, found, err := store.ShareLink(ctx, "open", shareType, next.ID, now)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, hash, *link.PasswordHash)
		for _, missing := range []struct{ token, entityType, entityID string }{
			{"expired", shareType, next.ID},
			{"open", shareType, archived.ID},
			{"open", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS.String(), next.ID},
		} {
			_, found, err = store.ShareLink(ctx, missing.token, missing.entityType, missing.entityID, now)
			require.NoError(t, err)
			require.False(t, found)
		}

		lifecycle, found, err := store.Lifecycle(ctx, legaldomain.Privacy, next.ID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, legaldomain.PrivacyScheduled, lifecycle.Status)
		require.True(t, next.EffectiveFrom.Equal(*lifecycle.EffectiveFrom))
		_, found, err = store.Lifecycle(ctx, legaldomain.Privacy, uuid.NewString())
		require.NoError(t, err)
		require.False(t, found)

		_, err = store.DocumentRoot(ctx, legaldomain.Privacy, uuid.NewString())
		require.ErrorIs(t, err, legaldomain.ErrPolicyNotFound)
		return nil
	}))
	require.NoError(t, units.ReadLocked(t.Context(), func(store public.Store) error {
		root, err := store.DocumentRoot(t.Context(), legaldomain.Privacy, current.ID)
		require.NoError(t, err)
		require.Equal(t, "en", root.SourceLocale)
		require.Equal(t, *current.ContentDocumentID, root.ContentDocumentID.String())
		snapshot, err := store.ContentSnapshot(t.Context(), *root.ContentDocumentID, root.SourceLocale)
		require.NoError(t, err)
		require.Len(t, snapshot.Blocks, 1)
		_, err = store.ContentSnapshot(t.Context(), uuid.New(), "en")
		require.ErrorIs(t, err, contentblock.ErrDocumentNotFound)
		return nil
	}))
}

func newPublicIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
}

type publicLegalFileReuseAuthorizer struct{}

func (publicLegalFileReuseAuthorizer) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

func newPublicLegalContentBlockStore(t *testing.T) *contentblock.Store {
	t.Helper()
	store, err := contentblock.NewGeneratedStore(publicLegalFileReuseAuthorizer{})
	require.NoError(t, err)
	return store
}

func seedPublicPrivacyPolicy(
	t *testing.T,
	db *gorm.DB,
	store *contentblock.Store,
	version int,
	title string,
	body string,
	status string,
	effectiveFrom *time.Time,
	effectiveUntil *time.Time,
) model.Privacy {
	t.Helper()
	row := model.Privacy{
		ID: uuid.NewString(), Version: version, Title: title, Content: "", Status: status,
		EffectiveFrom: effectiveFrom, EffectiveUntil: effectiveUntil,
	}
	seedPublicLegalContentDocument(t, db, store, "privacy", row.ID, body, &row.ContentDocumentID, &row)
	return row
}

func seedPublicTermsPolicy(
	t *testing.T,
	db *gorm.DB,
	store *contentblock.Store,
	version int,
	title string,
	body string,
	status string,
	effectiveFrom *time.Time,
	effectiveUntil *time.Time,
) model.Terms {
	t.Helper()
	row := model.Terms{
		ID: uuid.NewString(), Version: version, Title: title, Content: "", Status: status,
		EffectiveFrom: effectiveFrom, EffectiveUntil: effectiveUntil,
	}
	seedPublicLegalContentDocument(t, db, store, "terms", row.ID, body, &row.ContentDocumentID, &row)
	return row
}

func seedPublicLegalContentDocument(
	t *testing.T,
	db *gorm.DB,
	store *contentblock.Store,
	kind string,
	entityID string,
	body string,
	documentIDTarget **string,
	root any,
) {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		created, err := store.CreateDocument(t.Context(), tx, contentblock.CreateInput{
			Profile: "policy", SourceLocale: "en",
		})
		if err != nil {
			return err
		}
		documentID := created.Document.ID.String()
		*documentIDTarget = &documentID
		if err := tx.Create(root).Error; err != nil {
			return err
		}
		replacement, err := contentblock.ReplaceFromRichTextProto(
			created.Document.ID,
			created.Document.Revision,
			publicLegalRichTextDocument(body),
		)
		if err != nil {
			return err
		}
		_, err = store.ReplaceSnapshot(
			t.Context(),
			tx,
			replacement,
			func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
				return contentblock.DomainContext{SourceLocale: "en"}, nil
			},
		)
		if err != nil {
			return err
		}
		return tx.Table(kind+"_history").Where("id = ?", entityID).
			Update("source_locale", "en").Error
	}))
}

func publicLegalRichTextDocument(text string) *contentv1.RichTextDocument {
	blockID := uuid.NewString()
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY, SourceLocale: "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			}},
			Placement: &contentv1.ContentBlockPlacement{Index: 0},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID,
			Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props: &contentv1.ParagraphLocaleProps{},
				Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
					Text: &contentv1.RichTextStyledText{Text: text},
				}}},
			}},
		}}}},
	}
}
