//go:build integration

package public_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	postruntime "github.com/echovisionlab/geul-api/internal/adapters/post/runtime"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	postpublic "github.com/echovisionlab/geul-api/internal/post/public"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func TestPostPublicReadKeepsPublishedStatusAndBodyAcrossUnpublishIntegration(t *testing.T) {
	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	db := stack.Postgres.DB

	identityID := uuid.NewString()
	testutil.SeedPostIntegrationIdentity(t, db, identityID, "Public Snapshot Admin")
	spiceDB := stack.SpiceDBClient
	testutil.GrantPostIntegrationRole(t, spiceDB, identityID, policyv1.Role.Admin())
	adminCtx := testutil.PostIntegrationContext(identityID)
	store := testutil.NewPostContentBlockStore(t)
	management := postintegration.NewPostDomainService(
		t, db, "", spiceDB, testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(identityID, "en")), store,
	)
	blockID := uuid.New()
	created, err := management.CreatePost(adminCtx, connect.NewRequest(&managev1.CreatePostRequest{
		Title:    "Published snapshot post",
		Document: publicSnapshotPostDocument(blockID, "published body"),
	}))
	require.NoError(t, err)
	_, err = management.PublishPost(adminCtx, connect.NewRequest(&managev1.PublishPostRequest{Id: created.Msg.Id}))
	require.NoError(t, err)

	publicDB, err := gorm.Open(postgres.Open(stack.Postgres.DSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := publicDB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	require.NoError(t, publicDB.Callback().Query().After("gorm:query").Register("audit_pause_post_snapshot", func(tx *gorm.DB) {
		if tx.Statement.Table != "post" || !strings.Contains(strings.ToLower(tx.Statement.SQL.String()), "from \"post\"") {
			return
		}
		once.Do(func() {
			close(entered)
			<-release
		})
	}))
	publicService := postpublic.NewPostService(
		publicDB, "", spiceDB, referenceSearchFileService{}, postadapter.NewLocalization(),
		referencecatalogadapter.PublicMapPlaces{}, postadapter.NewMemberSummaries(publicDB, ""),
		postruntime.ShareLinks{}, postpublic.WithPostContentBlockStore(store),
	)
	getDone := make(chan postReadResult, 1)
	go func() {
		response, getErr := publicService.Get(context.Background(), connect.NewRequest(&openv1.GetPostRequest{Slug: created.Msg.Id}))
		getDone <- postReadResult{response: response, err: getErr}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("public Post read did not reach its root row query")
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	writeDone := make(chan error, 1)
	writeStarted := make(chan struct{})
	go func() {
		close(writeStarted)
		_, writeErr := management.UnpublishPost(adminCtx, connect.NewRequest(&managev1.UnpublishPostRequest{Id: created.Msg.Id}))
		if writeErr == nil {
			writeErr = replacePublicPostSnapshotDocument(context.Background(), db, store, created.Msg.Id,
				blockID, "fresh draft body")
		}
		writeDone <- writeErr
	}()
	<-writeStarted
	requirePublicPostUpdateWaitsForSnapshot(t, db, writeDone)
	unblock()

	read := <-getDone
	require.NoError(t, read.err)
	require.Equal(t, openv1.PostStatus_POST_STATUS_PUBLISHED, read.response.Msg.Post.Status)
	require.Equal(t, "published body", read.response.Msg.Post.Document.GetLocaleOverlay().GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText())
	require.NoError(t, <-writeDone)
}

type postReadResult struct {
	response *connect.Response[openv1.GetPostResponse]
	err      error
}

func publicSnapshotPostDocument(blockID uuid.UUID, text string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
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

func replacePublicPostSnapshotDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	postID string,
	blockID uuid.UUID,
	text string,
) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root struct {
			ContentDocumentID string `gorm:"column:content_document_id"`
		}
		if err := tx.Table("post").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("content_document_id").Where("id = ?::uuid", postID).Take(&root).Error; err != nil {
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
			documentID, snapshot.Document.Revision, publicSnapshotPostDocument(blockID, text),
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

func requirePublicPostUpdateWaitsForSnapshot(t *testing.T, db *gorm.DB, writeDone <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-writeDone:
			require.NoError(t, err)
			t.Fatal("Post unpublish and draft edit completed while the public snapshot was paused")
		default:
		}
		var waiting bool
		err := db.Raw(`
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database()
				  AND wait_event_type = 'Lock'
				  AND query ILIKE '%FOR UPDATE%'
				  AND query ILIKE '%post%'
			)
		`).Scan(&waiting).Error
		require.NoError(t, err)
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Post unpublish did not wait for the public root SHARE lock")
}
