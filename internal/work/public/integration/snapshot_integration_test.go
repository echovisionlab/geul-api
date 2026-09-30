//go:build integration

package integration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/testutil"
	workpublic "github.com/echovisionlab/geul-api/internal/work/public"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func TestWorkPublicReadKeepsPublishedStatusAndBodyAcrossUnpublishIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	stack, err := sharedPublicIntegrationStack()
	require.NoError(t, err)
	identityID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Name: "Public Snapshot Work Admin"})
	memberID := seedPublicAdminMemberIdentityLink(t, db, identityID, "Public Snapshot Work Admin")
	adminCtx := publicLegalAdminCtx(memberID, identityID)
	management := newPublicWorkManageService(t, db, identityID)
	blockID := uuid.New()
	created, err := management.CreateWork(adminCtx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title: "Published snapshot work", Type: managev1.WorkType_WORK_TYPE_PORTFOLIO,
		Year: 2026, Month: 9, IsPresent: boolPtr(true),
		Document: publicSnapshotWorkDocument(blockID, "published body"),
	}))
	require.NoError(t, err)
	_, err = management.PublishWork(adminCtx, connect.NewRequest(&managev1.PublishWorkRequest{Id: created.Msg.Id}))
	require.NoError(t, err)

	publicDB, err := gorm.Open(postgres.Open(stack.Postgres.DSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := publicDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	require.NoError(t, publicDB.Callback().Query().After("gorm:query").Register("audit_pause_work_snapshot", func(tx *gorm.DB) {
		if tx.Statement.Table != "work" || !strings.Contains(strings.ToLower(tx.Statement.SQL.String()), "from \"work\"") {
			return
		}
		once.Do(func() {
			close(entered)
			<-release
		})
	}))
	store := newPublicWorkContentBlockStore(t)
	publicService := workpublic.NewWorkService(
		publicDB,
		publicIntegrationSpiceDB,
		extractedWorkPublicMediaHydrator{},
		newPublicWorkRuntimeForTest(publicDB, "https://cdn.example.com"),
		workadapter.NewMemberSummaries(publicDB, "https://cdn.example.com"),
		referencecatalogadapter.PublicMapPlaces{},
		workpublic.WithWorkContentBlockStore(store),
	)
	getDone := make(chan workReadResult, 1)
	go func() {
		response, getErr := publicService.Get(context.Background(), connect.NewRequest(&openv1.GetWorkRequest{Slug: created.Msg.Id}))
		getDone <- workReadResult{response: response, err: getErr}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("public Work read did not reach its root row query")
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	writeDone := make(chan error, 1)
	writeStarted := make(chan struct{})
	go func() {
		close(writeStarted)
		_, writeErr := management.UnpublishWork(adminCtx, connect.NewRequest(&managev1.UnpublishWorkRequest{Id: created.Msg.Id}))
		if writeErr == nil {
			writeErr = replacePublicWorkSnapshotDocument(context.Background(), db, store, created.Msg.Id,
				blockID, "fresh draft body")
		}
		writeDone <- writeErr
	}()
	<-writeStarted
	requireWorkUpdateWaitsForSnapshot(t, db, writeDone)
	unblock()

	read := <-getDone
	require.NoError(t, read.err)
	require.Equal(t, openv1.WorkStatus_WORK_STATUS_PUBLISHED, read.response.Msg.Work.Status)
	require.Equal(t, "published body", read.response.Msg.Work.Document.GetLocaleOverlay().GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText())
	require.NoError(t, <-writeDone)
}

type workReadResult struct {
	response *connect.Response[openv1.GetWorkResponse]
	err      error
}

func publicSnapshotWorkDocument(blockID uuid.UUID, text string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		SourceLocale:            "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID.String(), Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			}},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{
			Locale: "en",
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID.String(),
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
						Text: &contentv1.RichTextStyledText{Text: text},
					}}},
				}},
			}},
		}},
	}
}

func replacePublicWorkSnapshotDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	workID string,
	blockID uuid.UUID,
	text string,
) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root struct {
			ContentDocumentID string `gorm:"column:content_document_id"`
		}
		if err := tx.Table("work").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("content_document_id").Where("id = ?::uuid", workID).Take(&root).Error; err != nil {
			return err
		}
		documentID, err := uuid.Parse(root.ContentDocumentID)
		if err != nil {
			return err
		}
		snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, "en")
		if err != nil {
			return err
		}
		replacement, err := contentblock.ReplaceFromRichTextProto(
			documentID, snapshot.Document.Revision, publicSnapshotWorkDocument(blockID, text),
		)
		if err != nil {
			return err
		}
		_, err = store.ReplaceSnapshot(ctx, tx, replacement, func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: "en"}, nil
		})
		return err
	})
}

func requireWorkUpdateWaitsForSnapshot(t *testing.T, db *gorm.DB, writeDone <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-writeDone:
			require.NoError(t, err)
			t.Fatal("Work unpublish and draft edit completed while the public snapshot was paused")
		default:
		}
		var waiting bool
		err := db.Raw(`
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database()
				  AND wait_event_type = 'Lock'
				  AND query ILIKE '%FOR UPDATE%'
				  AND query ILIKE '%work%'
			)
		`).Scan(&waiting).Error
		require.NoError(t, err)
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Work unpublish did not wait for the public root SHARE lock")
}
