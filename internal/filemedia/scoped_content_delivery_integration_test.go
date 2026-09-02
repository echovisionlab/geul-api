//go:build integration

package filemedia

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResolveAuthorizedPostFeaturedImageRejectsChangedOrPendingExactSlotIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*gorm.DB, scopedFeaturedFixture) error
	}{
		{
			name: "slot replaced before authorization",
			mutate: func(db *gorm.DB, fixture scopedFeaturedFixture) error {
				return db.Table("post").Where("id = ?", fixture.ownerID).
					Update("featured_image_file_id", fixture.replacementFileID).Error
			},
		},
		{
			name: "slot detached before authorization",
			mutate: func(db *gorm.DB, fixture scopedFeaturedFixture) error {
				return db.Table("post").Where("id = ?", fixture.ownerID).
					Update("featured_image_file_id", nil).Error
			},
		},
		{
			name: "File deletion pending before authorization",
			mutate: func(db *gorm.DB, fixture scopedFeaturedFixture) error {
				return db.Table("file").Where("id = ?", fixture.fileID).
					Update("delete_requested_at", time.Now().UTC()).Error
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newConcurrentServiceIntegrationDB(t)
			fixture := seedScopedPostFeaturedFixture(t, db)
			require.NoError(t, testCase.mutate(db, fixture))

			delivery, err := fixture.service.ResolveAuthorizedPostFeaturedImage(
				fixture.ctx, fixture.ownerID, fixture.fileID,
			)

			require.NoError(t, err)
			require.Nil(t, delivery)
		})
	}
}

func TestHydrateAuthorizedPageBlockMediaRejectsChangedOrPendingExactAttachmentIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*gorm.DB, scopedBlockFixture) error
	}{
		{
			name: "attachment replaced before authorization",
			mutate: func(db *gorm.DB, fixture scopedBlockFixture) error {
				return db.Exec(
					`UPDATE content_block_attachment SET file_id = ?::uuid WHERE block_id = ?::uuid AND reference_path = 'file'`,
					fixture.replacementFileID, fixture.blockID,
				).Error
			},
		},
		{
			name: "attachment detached before authorization",
			mutate: func(db *gorm.DB, fixture scopedBlockFixture) error {
				return db.Exec(
					`DELETE FROM content_block_attachment WHERE block_id = ?::uuid AND reference_path = 'file'`,
					fixture.blockID,
				).Error
			},
		},
		{
			name: "File deletion pending before authorization",
			mutate: func(db *gorm.DB, fixture scopedBlockFixture) error {
				return db.Exec(
					`UPDATE file SET delete_requested_at = CURRENT_TIMESTAMP WHERE id = ?::uuid`,
					fixture.fileID,
				).Error
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newConcurrentServiceIntegrationDB(t)
			fixture := seedScopedPageBlockFixture(t, db)
			require.NoError(t, testCase.mutate(db, fixture))

			var hydrated []*contentv1.ContentBlockMediaItem
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				if err := lockScopedPageRootForTest(t.Context(), tx, fixture.pageID); err != nil {
					return err
				}
				var err error
				hydrated, err = fixture.service.HydrateAuthorizedPageBlockMediaWithDB(
					t.Context(), tx, fixture.pageID, uuid.MustParse(fixture.documentID), fixture.principal, fixture.items(),
				)
				return err
			}))
			require.Len(t, hydrated, 1)
			require.Nil(t, hydrated[0].GetDelivery())
			require.Equal(t, contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_UNAVAILABLE, hydrated[0].GetDownloadAvailability())
			require.Equal(t, contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_NONE, hydrated[0].GetDownloadAction())
		})
	}
}

func TestHydrateAuthorizedPageBlockMediaSignsBeforeAttachmentOrFileMutationIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(context.Context, *sql.Tx, scopedBlockFixture) error
	}{
		{
			name: "attachment replacement waits",
			mutate: func(ctx context.Context, tx *sql.Tx, fixture scopedBlockFixture) error {
				_, err := tx.ExecContext(ctx,
					`UPDATE content_block_attachment SET file_id = $1 WHERE block_id = $2 AND reference_path = 'file'`,
					fixture.replacementFileID, fixture.blockID,
				)
				return err
			},
		},
		{
			name: "attachment detach waits",
			mutate: func(ctx context.Context, tx *sql.Tx, fixture scopedBlockFixture) error {
				_, err := tx.ExecContext(ctx,
					`DELETE FROM content_block_attachment WHERE block_id = $1 AND reference_path = 'file'`,
					fixture.blockID,
				)
				return err
			},
		},
		{
			name: "File deletion intent waits",
			mutate: func(ctx context.Context, tx *sql.Tx, fixture scopedBlockFixture) error {
				_, err := tx.ExecContext(ctx,
					`UPDATE file SET delete_requested_at = CURRENT_TIMESTAMP WHERE id = $1`, fixture.fileID,
				)
				return err
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newConcurrentServiceIntegrationDB(t)
			fixture := seedScopedPageBlockFixture(t, db)
			signed := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseSignedFence := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseSignedFence()
			fixture.service.testAfterScopedBlockMediaSigned = func(string, string, []string) {
				close(signed)
				<-release
			}
			type resolution struct {
				items []*contentv1.ContentBlockMediaItem
				err   error
			}
			resolved := make(chan resolution, 1)
			go func() {
				var hydrated []*contentv1.ContentBlockMediaItem
				err := db.Transaction(func(tx *gorm.DB) error {
					if err := lockScopedPageRootForTest(t.Context(), tx, fixture.pageID); err != nil {
						return err
					}
					var hydrateErr error
					hydrated, hydrateErr = fixture.service.HydrateAuthorizedPageBlockMediaWithDB(
						t.Context(), tx, fixture.pageID, uuid.MustParse(fixture.documentID), fixture.principal, fixture.items(),
					)
					return hydrateErr
				})
				resolved <- resolution{items: hydrated, err: err}
			}()
			select {
			case <-signed:
			case <-time.After(5 * time.Second):
				t.Fatal("Page Block delivery did not reach the signed fence")
			}

			backendPID, attempted, mutationDone := startScopedBlockMutation(t, db, fixture, testCase.mutate)
			<-attempted
			requirePostgresBackendWaitingForLock(t, db, backendPID)
			select {
			case err := <-mutationDone:
				require.NoError(t, err)
				t.Fatal("attachment or File mutation committed before signed delivery transaction completed")
			default:
			}

			releaseSignedFence()
			result := <-resolved
			require.NoError(t, result.err)
			require.Len(t, result.items, 1)
			require.Equal(t, fixture.fileID, result.items[0].GetDelivery().GetFileId())
			require.NotNil(t, result.items[0].GetDelivery().GetInline())
			require.Nil(t, result.items[0].GetDelivery().GetDownload())
			require.Equal(t, contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_UNAVAILABLE, result.items[0].GetDownloadAvailability())
			select {
			case err := <-mutationDone:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("attachment or File mutation did not commit after delivery transaction completed")
			}
		})
	}
}

func TestHydrateAuthorizedPageBlockMediaKeepsOriginalDownloadPerFileEntitlementIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	fixture := seedScopedPageBlockFixture(t, db)
	ownedBlockID := uuid.NewString()
	require.NoError(t, db.Exec(
		`UPDATE file SET uploaded_by_member_id = ?::uuid WHERE id = ?::uuid`,
		fixture.principal.MemberID.String(), fixture.replacementFileID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO content_block (id, document_id, parent_block_id, container_slot, position, kind, shared_data) VALUES (?::uuid, ?::uuid, NULL, 'root', 1, 'file', '{}'::jsonb)`,
		ownedBlockID, fixture.documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id) VALUES (?::uuid, 'file', 'active', ?::uuid)`,
		ownedBlockID, fixture.replacementFileID,
	).Error)
	items := []*contentv1.ContentBlockMediaItem{
		fixture.item(fixture.blockID, fixture.fileID),
		fixture.item(ownedBlockID, fixture.replacementFileID),
	}

	var hydrated []*contentv1.ContentBlockMediaItem
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := lockScopedPageRootForTest(t.Context(), tx, fixture.pageID); err != nil {
			return err
		}
		var err error
		hydrated, err = fixture.service.HydrateAuthorizedPageBlockMediaWithDB(
			t.Context(), tx, fixture.pageID, uuid.MustParse(fixture.documentID), fixture.principal, items,
		)
		return err
	}))
	require.Len(t, hydrated, 2)
	require.NotNil(t, hydrated[0].GetDelivery().GetInline())
	require.Nil(t, hydrated[0].GetDelivery().GetDownload())
	require.Equal(t, contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_UNAVAILABLE, hydrated[0].GetDownloadAvailability())
	require.NotNil(t, hydrated[1].GetDelivery().GetInline())
	require.NotNil(t, hydrated[1].GetDelivery().GetDownload())
	require.Equal(t, contentv1.ContentBlockDownloadAvailability_CONTENT_BLOCK_DOWNLOAD_AVAILABILITY_AVAILABLE, hydrated[1].GetDownloadAvailability())
	require.Equal(t, contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD, hydrated[1].GetDownloadAction())
}

func TestResolveAuthorizedPostFeaturedImageSignsBeforeSlotOrFileMutationIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(context.Context, *sql.Tx, scopedFeaturedFixture) error
	}{
		{
			name: "slot replacement waits",
			mutate: func(ctx context.Context, tx *sql.Tx, fixture scopedFeaturedFixture) error {
				_, err := tx.ExecContext(ctx, `UPDATE post SET featured_image_file_id = $1 WHERE id = $2`, fixture.replacementFileID, fixture.ownerID)
				return err
			},
		},
		{
			name: "File deletion intent waits",
			mutate: func(ctx context.Context, tx *sql.Tx, fixture scopedFeaturedFixture) error {
				_, err := tx.ExecContext(ctx, `UPDATE file SET delete_requested_at = CURRENT_TIMESTAMP WHERE id = $1`, fixture.fileID)
				return err
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newConcurrentServiceIntegrationDB(t)
			fixture := seedScopedPostFeaturedFixture(t, db)
			signed := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseSignedFence := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseSignedFence()
			type signedFence struct {
				owner   string
				ownerID string
				fileID  string
			}
			signedFenceReached := make(chan signedFence, 1)
			fixture.service.testAfterScopedFeaturedSigned = func(owner, ownerID, fileID string) {
				signedFenceReached <- signedFence{owner: owner, ownerID: ownerID, fileID: fileID}
				close(signed)
				<-release
			}
			type resolution struct {
				deliveryFileID string
				err            error
			}
			resolved := make(chan resolution, 1)
			go func() {
				delivery, err := fixture.service.ResolveAuthorizedPostFeaturedImage(
					fixture.ctx, fixture.ownerID, fixture.fileID,
				)
				fileID := ""
				if delivery != nil {
					fileID = delivery.GetFileId()
				}
				resolved <- resolution{deliveryFileID: fileID, err: err}
			}()
			select {
			case <-signed:
			case <-time.After(5 * time.Second):
				t.Fatal("featured image delivery did not reach the signed fence")
			}
			fence := <-signedFenceReached
			require.Equal(t, "post", fence.owner)
			require.Equal(t, fixture.ownerID, fence.ownerID)
			require.Equal(t, fixture.fileID, fence.fileID)

			backendPID, attempted, mutationDone := startScopedContentMutation(t, db, fixture, testCase.mutate)
			<-attempted
			requirePostgresBackendWaitingForLock(t, db, backendPID)
			select {
			case err := <-mutationDone:
				require.NoError(t, err)
				t.Fatal("slot or File mutation committed before signed delivery transaction completed")
			default:
			}

			releaseSignedFence()
			result := <-resolved
			require.NoError(t, result.err)
			require.Equal(t, fixture.fileID, result.deliveryFileID)
			select {
			case err := <-mutationDone:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("slot or File mutation did not commit after delivery transaction completed")
			}
		})
	}
}

type scopedFeaturedFixture struct {
	db                *gorm.DB
	service           *FileService
	ctx               context.Context
	ownerID           string
	fileID            string
	replacementFileID string
}

func seedScopedPostFeaturedFixture(t *testing.T, db *gorm.DB) scopedFeaturedFixture {
	t.Helper()
	identityID := uuid.NewString()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Scoped featured Post author")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	documentID, postID := uuid.NewString(), uuid.NewString()
	fileID, replacementFileID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'post', ?::uuid)`, documentID, uuid.NewString()).Error)
	for _, id := range []string{fileID, replacementFileID} {
		require.NoError(t, db.Create(&model.File{
			ID: id, FileName: id, MimeType: "image/jpeg", FileSize: 128,
			Extension: "jpg", SHA256: make([]byte, 32), UploadedByMemberID: &memberID,
		}).Error)
	}
	require.NoError(t, db.Exec(
		`INSERT INTO post (id, status, content_document_id, featured_image_file_id) VALUES (?::uuid, ?, ?::uuid, ?::uuid)`,
		postID, managev1.PostStatus_POST_STATUS_DRAFT.String(), documentID, fileID,
	).Error)
	seedFileDeliveryContentPolicy(t, spiceDB, "post", postID)
	seedFileDeliveryPostAuthority(t, spiceDB, postID, identityID)
	service := &FileService{
		db: db, spiceDB: spiceDB, mediaDomain: "https://media.example.test", mediaSecret: "scoped-featured-secret",
	}
	WithPostAccess(newIntegrationPostAccess(db, spiceDB))(service)
	return scopedFeaturedFixture{
		db: db, service: service, ctx: ctx, ownerID: postID, fileID: fileID, replacementFileID: replacementFileID,
	}
}

type scopedBlockFixture struct {
	service           *FileService
	principal         *auth.UserInfo
	pageID            string
	documentID        string
	blockID           string
	fileID            string
	replacementFileID string
}

func (fixture scopedBlockFixture) items() []*contentv1.ContentBlockMediaItem {
	return []*contentv1.ContentBlockMediaItem{fixture.item(fixture.blockID, fixture.fileID)}
}

func (fixture scopedBlockFixture) item(blockID, fileID string) *contentv1.ContentBlockMediaItem {
	return &contentv1.ContentBlockMediaItem{
		Selector:   &contentv1.ContentBlockMediaSelector{BlockId: blockID, ReferencePath: "file"},
		Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: fileID}},
	}
}

func seedScopedPageBlockFixture(t *testing.T, db *gorm.DB) scopedBlockFixture {
	t.Helper()
	memberID := uuid.NewString()
	otherMemberID := uuid.NewString()
	for _, id := range []string{memberID, otherMemberID} {
		require.NoError(t, db.Exec(`INSERT INTO member (id, nickname) VALUES (?::uuid, ?)`, id, "member-"+id[:8]).Error)
	}
	pageID, documentID, blockID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	fileID, replacementFileID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile, revision) VALUES (?::uuid, 'page', ?::uuid)`, documentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`INSERT INTO page (id, content_document_id) VALUES (?::uuid, ?::uuid)`, pageID, documentID).Error)
	for _, id := range []string{fileID, replacementFileID} {
		require.NoError(t, db.Exec(
			`INSERT INTO file (id, file_name, extension, mime_type, file_size, sha256, uploaded_by_member_id) VALUES (?::uuid, ?, 'jpg', 'image/jpeg', 128, ?, ?::uuid)`,
			id, id, make([]byte, 32), otherMemberID,
		).Error)
	}
	require.NoError(t, db.Exec(
		`INSERT INTO content_block (id, document_id, parent_block_id, container_slot, position, kind, shared_data) VALUES (?::uuid, ?::uuid, NULL, 'root', 0, 'file', '{}'::jsonb)`,
		blockID, documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id) VALUES (?::uuid, 'file', 'active', ?::uuid)`,
		blockID, fileID,
	).Error)
	return scopedBlockFixture{
		service: &FileService{
			db: db, mediaDomain: "https://media.example.test", mediaSecret: "scoped-block-secret",
		},
		principal: &auth.UserInfo{
			IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(memberID),
			SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
		},
		pageID: pageID, documentID: documentID, blockID: blockID, fileID: fileID, replacementFileID: replacementFileID,
	}
}

func lockScopedPageRootForTest(ctx context.Context, tx *gorm.DB, pageID string) error {
	return tx.WithContext(ctx).Exec(`SELECT id FROM page WHERE id = ?::uuid FOR SHARE`, pageID).Error
}

func startScopedBlockMutation(
	t *testing.T,
	db *gorm.DB,
	fixture scopedBlockFixture,
	mutate func(context.Context, *sql.Tx, scopedBlockFixture) error,
) (int, <-chan struct{}, <-chan error) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	var backendPID int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
	attempted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		defer cancel()
		defer conn.Close()
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
		if err != nil {
			close(attempted)
			done <- err
			return
		}
		close(attempted)
		if err = mutate(ctx, tx, fixture); err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		done <- err
	}()
	return backendPID, attempted, done
}

func startScopedContentMutation(
	t *testing.T,
	db *gorm.DB,
	fixture scopedFeaturedFixture,
	mutate func(context.Context, *sql.Tx, scopedFeaturedFixture) error,
) (int, <-chan struct{}, <-chan error) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	var backendPID int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
	attempted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		defer cancel()
		defer conn.Close()
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
		if err != nil {
			close(attempted)
			done <- err
			return
		}
		close(attempted)
		if err = mutate(ctx, tx, fixture); err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		done <- err
	}()
	return backendPID, attempted, done
}
