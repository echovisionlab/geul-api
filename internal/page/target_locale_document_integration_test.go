//go:build integration

package page

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPageTargetLocaleMetadataUsesExactCASWithoutAdvancingSharedRevisionIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, identityID, "Page target locale CAS")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := workIntegrationAdminCtx(identityID)
	store := newPageIntegrationContentBlockStore(t, spiceDB)
	service := NewPageService(
		db, newPageRuntimeForTest(db, "https://cdn.example.com"), &recordingPageDeleteFileDeleter{},
		noopAsyncPublisher{}, &fakeIdentityManager{identity: postIntegrationIdentity(identityID, "en")}, spiceDB,
		WithPageContentBlockStore(store), WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	created, err := service.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{Title: "Page target locale CAS"}))
	require.NoError(t, err)
	documentID, err := loadPageContentDocumentID(ctx, db, created.Msg.Id)
	require.NoError(t, err)
	state, err := loadPageTargetLocaleState(ctx, db, store, created.Msg.Id, documentID, "ko", false)
	require.NoError(t, err)
	sharedRevision := state.Snapshot.Document.Revision
	now := time.Now().UTC().Truncate(time.Microsecond)

	createTarget := func(locale string, at time.Time) string {
		t.Helper()
		var token *string
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			_, token, err = applyPageTargetLocaleBatch(
				ctx, tx, store, created.Msg.Id, documentID, locale,
				contentblock.Batch{DocumentID: documentID, ExpectedRevision: sharedRevision},
				nil, pageTargetMetadataPatch{EnsureLocale: true}, true, false, at,
				lockedPageContentFence(created.Msg.Id),
			)
			return err
		}))
		require.NotNil(t, token)
		return *token
	}
	koToken := createTarget("ko", now)
	jaToken := createTarget("ja", now.Add(time.Second))
	var originalLocaleRow struct {
		IncarnationID string    `gorm:"column:incarnation_id"`
		UpdatedAt     time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, db.Raw(`SELECT incarnation_id::text, updated_at FROM page_translation
		WHERE entity_id = ?::uuid AND locale = 'ko'`, created.Msg.Id).Scan(&originalLocaleRow).Error)
	require.True(t, originalLocaleRow.UpdatedAt.Equal(now), "initial row must use the exact truncated creation timestamp")
	sessionID := insertPageIntegrationSession(t, db, identityID)
	internal := NewInternalPageService(
		db, noopAsyncPublisher{}, spiceDB, newPageRuntimeForTest(db, "https://cdn.example.com"),
		WithInternalPageDomainAuditWriter(apitelemetry.NewDurableWriter(db)),
		WithInternalPageContentBlockStore(store), WithInternalPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	loaded, err := internal.LoadPageBlockDocument(ctx, connect.NewRequest(&intrav1.LoadPageBlockDocumentRequest{
		PageId: created.Msg.Id, Principal: &intrav1.CollaborationPrincipal{SessionId: sessionID}, Locale: "ko",
	}))
	require.NoError(t, err)
	require.Equal(t, "ko", loaded.Msg.Locale)
	require.True(t, loaded.Msg.LocaleExists)
	require.Equal(t, "en", loaded.Msg.SourceMetadata.GetLocale())
	require.Equal(t, koToken, loaded.Msg.GetTargetRevision())
	require.Equal(t, "ko", loaded.Msg.Document.GetLocale())

	title := "한국어"
	updated, err := internal.UpdatePageLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdatePageLocaleMetadataRequest{
		PageId: created.Msg.Id, Locale: "ko", Title: &title,
		ExpectedRevision: sharedRevision.String(), ExpectedTargetRevision: &koToken,
		ContributorMemberIds: []string{integrationMemberID(identityID)},
	}))
	require.NoError(t, err)
	require.True(t, updated.Msg.Changed)
	require.Equal(t, sharedRevision.String(), updated.Msg.DocumentRevision)
	require.NotNil(t, updated.Msg.TargetRevision)
	require.NotEqual(t, koToken, updated.Msg.GetTargetRevision())
	jaState, err := loadPageTargetLocaleState(ctx, db, store, created.Msg.Id, documentID, "ja", false)
	require.NoError(t, err)
	require.Equal(t, jaToken, jaState.TargetRevision)

	var deleted contentblock.Result
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		deleted, _, err = applyPageTargetLocaleBatch(
			ctx, tx, store, created.Msg.Id, documentID, "ko",
			contentblock.Batch{DocumentID: documentID, ExpectedRevision: sharedRevision},
			updated.Msg.TargetRevision, pageTargetMetadataPatch{DeleteLocale: true}, false, false,
			now.Add(3*time.Second), lockedPageContentFence(created.Msg.Id),
		)
		return err
	}))
	require.True(t, deleted.Changed)
	require.Equal(t, sharedRevision, deleted.DocumentRevision)
	var count int64
	require.NoError(t, db.Table("page_translation").Where("entity_id = ?::uuid AND locale = 'ko'", created.Msg.Id).Count(&count).Error)
	require.Zero(t, count)
	var storedRevision string
	require.NoError(t, db.Raw("SELECT revision::text FROM content_document WHERE id = ?", documentID).Scan(&storedRevision).Error)
	require.Equal(t, sharedRevision.String(), storedRevision)

	recreatedKoToken := createTarget("ko", now)
	require.NotEqual(t, koToken, recreatedKoToken, "recreated locale row must have a distinct CAS token")
	var recreatedLocaleRow struct {
		IncarnationID string    `gorm:"column:incarnation_id"`
		UpdatedAt     time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, db.Raw(`SELECT incarnation_id::text, updated_at FROM page_translation
		WHERE entity_id = ?::uuid AND locale = 'ko'`, created.Msg.Id).Scan(&recreatedLocaleRow).Error)
	require.NotEqual(t, originalLocaleRow.IncarnationID, recreatedLocaleRow.IncarnationID)
	require.True(t, recreatedLocaleRow.UpdatedAt.Equal(now), "recreated row must use the same truncated creation timestamp")
	require.Equal(t, originalLocaleRow.UpdatedAt, recreatedLocaleRow.UpdatedAt,
		"same creation timestamp isolates the locale incarnation from timestamp CAS")

	staleTitle := "must not write through a deleted locale token"
	_, err = internal.UpdatePageLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdatePageLocaleMetadataRequest{
		PageId: created.Msg.Id, Locale: "ko", Title: &staleTitle,
		ExpectedRevision: sharedRevision.String(), ExpectedTargetRevision: &koToken,
		ContributorMemberIds: []string{integrationMemberID(identityID)},
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	require.Len(t, connectErr.Details(), 1)
	detail, detailErr := connectErr.Details()[0].Value()
	require.NoError(t, detailErr)
	conflict, ok := detail.(*intrav1.CollaborationConflictDetail)
	require.True(t, ok, "stale target token must have a CollaborationConflictDetail")
	require.Equal(t,
		intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED,
		conflict.GetReason(),
	)

	recreatedTitle := "recreated locale fresh write"
	freshUpdate, err := internal.UpdatePageLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdatePageLocaleMetadataRequest{
		PageId: created.Msg.Id, Locale: "ko", Title: &recreatedTitle,
		ExpectedRevision: sharedRevision.String(), ExpectedTargetRevision: &recreatedKoToken,
		ContributorMemberIds: []string{integrationMemberID(identityID)},
	}))
	require.NoError(t, err)
	require.True(t, freshUpdate.Msg.Changed)
	require.NotEqual(t, recreatedKoToken, freshUpdate.Msg.GetTargetRevision())
	require.Equal(t, sharedRevision.String(), freshUpdate.Msg.DocumentRevision)
	unchangedTarget, err := internal.UpdatePageLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdatePageLocaleMetadataRequest{
		PageId: created.Msg.Id, Locale: "ko", Title: &recreatedTitle,
		ExpectedRevision: sharedRevision.String(), ExpectedTargetRevision: freshUpdate.Msg.TargetRevision,
		ContributorMemberIds: []string{integrationMemberID(identityID)},
	}))
	require.NoError(t, err)
	require.False(t, unchangedTarget.Msg.Changed)
	require.Empty(t, unchangedTarget.Msg.ChangedLocales)
	require.Equal(t, freshUpdate.Msg.GetTargetRevision(), unchangedTarget.Msg.GetTargetRevision())

	sourceTitle := "Page source changed"
	sourceUpdate, err := internal.UpdatePageLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdatePageLocaleMetadataRequest{
		PageId: created.Msg.Id, Locale: "en", Title: &sourceTitle,
		ExpectedRevision: sharedRevision.String(), ContributorMemberIds: []string{integrationMemberID(identityID)},
	}))
	require.NoError(t, err)
	require.True(t, sourceUpdate.Msg.Changed)
	require.NotEqual(t, sharedRevision.String(), sourceUpdate.Msg.DocumentRevision)
	jaAfterSource, err := loadPageTargetLocaleState(ctx, db, store, created.Msg.Id, documentID, "ja", false)
	require.NoError(t, err)
	require.NotEqual(t, jaToken, jaAfterSource.TargetRevision)
}
