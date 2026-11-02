package filemediapublicpostgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	optional "github.com/echovisionlab/geul-api/internal/optional"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestHydrateAuthorizedContentBlockMediaKeepsPolicyPerExactBlockRelation(t *testing.T) {
	db := newContentBlockMediaUnitDB(t)
	documentID := uuid.NewString()
	postID := uuid.NewString()
	fileID := uuid.NewString()
	firstBlockID := uuid.NewString()
	secondBlockID := uuid.NewString()
	thirdBlockID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id) VALUES (?)`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, status, content_document_id) VALUES (?, 'POST_STATUS_PUBLISHED', ?)`, postID, documentID).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO content_block (id, document_id, kind) VALUES (?, ?, 'file'), (?, ?, 'file'), (?, ?, 'file')`,
		firstBlockID, documentID, secondBlockID, documentID, thirdBlockID, documentID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_audience) VALUES
			(?, 'file', 'active', ?, 'public'),
			(?, 'file', 'active', ?, 'authenticated'),
			(?, 'file', 'active', ?, 'disabled')`,
		firstBlockID, fileID, secondBlockID, fileID, thirdBlockID, fileID,
	).Error)
	fileName := "original.png"
	require.NoError(t, db.Exec(
		`INSERT INTO file (id, file_name, extension, mime_type, file_size)
		 VALUES (?, ?, 'png', 'image/png', 1024)`,
		fileID, fileName,
	).Error)

	service := newTestService(testDeps{DB: db, SpiceDB: &auth.SpiceDBClient{}, CDNDomain: "https://cdn.example.com", MediaDomain: "https://media.example.com", MediaSecret: "unit-secret", DownloadTTL: time.Minute})
	items, err := contentblock.LoadMediaReferences(
		context.Background(),
		db,
		uuid.MustParse(documentID),
	)
	require.NoError(t, err)
	hydrationContext := mediaasset.WithContentDownloadOwnerAuthorization(context.Background(), mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "post", ResourceID: postID, Status: "POST_STATUS_PUBLISHED", DocumentID: documentID,
		Mode: mediaasset.ContentDownloadOwnerAccessPublic,
	})
	items, err = service.HydrateAuthorizedContentBlockMedia(hydrationContext, items)
	require.NoError(t, err)
	require.Len(t, items, 3)

	byBlock := make(map[string]*fileref.BlockMediaItem, len(items))
	for _, item := range items {
		byBlock[optional.Value(optional.Value(item).Selector).BlockID] = item
	}
	for _, blockID := range []string{firstBlockID, secondBlockID, thirdBlockID} {
		item := byBlock[blockID]
		require.NotNil(t, item)
		require.Equal(t, fileID, item.Attachment.ActiveFileID())
		require.Equal(t, fileID, item.Delivery.FileID)
		require.Equal(t, fileName, optional.Value(item.Delivery.FileName))
		require.NotNil(t, item.Delivery.Inline)
	}
	require.NotNil(t, byBlock[firstBlockID].Delivery.Download)
	require.Equal(t, fileref.Download, byBlock[firstBlockID].DownloadAction)
	require.Nil(t, byBlock[secondBlockID].Delivery.Download)
	require.Equal(t, fileref.SignIn, byBlock[secondBlockID].DownloadAction)
	require.Nil(t, byBlock[thirdBlockID].Delivery.Download)
	require.Equal(t, fileref.NoDownload, byBlock[thirdBlockID].DownloadAction)

	inactiveContext := auth.WithUser(hydrationContext, &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()), Authenticated: true,
	})
	inactiveItems, err := contentblock.LoadMediaReferences(context.Background(), db, uuid.MustParse(documentID))
	require.NoError(t, err)
	inactiveItems, err = service.HydrateAuthorizedContentBlockMedia(inactiveContext, inactiveItems)
	require.NoError(t, err)
	inactiveByBlock := make(map[string]*fileref.BlockMediaItem, len(inactiveItems))
	for _, item := range inactiveItems {
		inactiveByBlock[optional.Value(optional.Value(item).Selector).BlockID] = item
	}
	require.Equal(t, fileref.Download, inactiveByBlock[firstBlockID].DownloadAction)
	require.NotNil(t, inactiveByBlock[firstBlockID].Delivery.Download)
	require.Equal(t, fileref.NoDownload, inactiveByBlock[secondBlockID].DownloadAction)
	require.Nil(t, inactiveByBlock[secondBlockID].Delivery.Download)
}

func TestHydrateAuthorizedContentBlockMediaDeliversImmersiveSceneAttachmentsWithoutFileDownloadPolicy(t *testing.T) {
	db := newContentBlockMediaUnitDB(t)
	documentID, pageID := uuid.NewString(), uuid.NewString()
	blockID, meshFileID := uuid.NewString(), uuid.NewString()
	referencePaths := []string{"mesh", "optimization_source", "optimized_mesh"}
	require.NoError(t, db.Exec(`INSERT INTO content_document (id) VALUES (?)`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO page (id, status, content_document_id) VALUES (?, 'PAGE_STATUS_PUBLISHED', ?)`, pageID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id, kind) VALUES (?, ?, 'immersive_scene')`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, extension, mime_type, file_size) VALUES (?, 'scene.glb', 'glb', 'model/gltf-binary', 35848)`, meshFileID).Error)
	for _, referencePath := range referencePaths {
		require.NoError(t, db.Exec(
			`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_audience) VALUES (?, ?, 'active', ?, 'disabled')`,
			blockID, referencePath, meshFileID,
		).Error)
	}

	service := newTestService(testDeps{DB: db, SpiceDB: &auth.SpiceDBClient{}, CDNDomain: "https://cdn.example.com", MediaDomain: "https://media.example.com", MediaSecret: "unit-secret", DownloadTTL: time.Minute})
	items, err := contentblock.LoadMediaReferences(context.Background(), db, uuid.MustParse(documentID))
	require.NoError(t, err)
	items, err = service.HydrateAuthorizedContentBlockMedia(
		mediaasset.WithContentDownloadOwnerAuthorization(context.Background(), mediaasset.ContentDownloadOwnerAuthorization{
			ResourceType: "page", ResourceID: pageID, Status: "PAGE_STATUS_PUBLISHED", DocumentID: documentID,
			Mode: mediaasset.ContentDownloadOwnerAccessPublic,
		}),
		items,
	)
	require.NoError(t, err)
	require.Len(t, items, len(referencePaths))
	for _, item := range items {
		require.Equal(t, meshFileID, optional.Value(item).Delivery.FileID)
		require.NotNil(t, optional.Value(item).Delivery.Inline)
		require.Nil(t, optional.Value(item).Delivery.Download)
		require.Equal(t, fileref.DownloadUnavailable, optional.Value(item).DownloadAvailability)
		require.Equal(t, fileref.NoDownload, optional.Value(item).DownloadAction)
	}
}

func TestHydrateAuthorizedContentBlockMediaDoesNotIssuePrivateRefsAfterOwnerOrShareRevoke(t *testing.T) {
	db := newContentBlockMediaUnitDB(t)
	documentID, postID, blockID, fileID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id) VALUES (?)`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, status, content_document_id) VALUES (?, 'POST_STATUS_PUBLISHED', ?)`, postID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id, kind) VALUES (?, ?, 'file')`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_audience) VALUES (?, 'file', 'active', ?, 'public')`, blockID, fileID).Error)
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, extension, mime_type, file_size) VALUES (?, 'private.png', 'png', 'image/png', 1024)`, fileID).Error)

	service := newTestService(testDeps{DB: db, SpiceDB: &auth.SpiceDBClient{}, CDNDomain: "https://cdn.example.com", MediaDomain: "https://media.example.com", MediaSecret: "unit-secret", DownloadTTL: time.Minute})
	loadItems := func() []*fileref.BlockMediaItem {
		items, err := contentblock.LoadMediaReferences(context.Background(), db, uuid.MustParse(documentID))
		require.NoError(t, err)
		return items
	}

	publishedWitness := mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "post", ResourceID: postID, Status: "POST_STATUS_PUBLISHED", DocumentID: documentID,
		Mode: mediaasset.ContentDownloadOwnerAccessPublic,
	}
	require.NoError(t, db.Exec(`UPDATE post SET status = 'POST_STATUS_DRAFT' WHERE id = ?`, postID).Error)
	items, err := service.HydrateAuthorizedContentBlockMedia(
		mediaasset.WithContentDownloadOwnerAuthorization(context.Background(), publishedWitness), loadItems(),
	)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Nil(t, items[0].Delivery)
	require.Equal(t, fileref.NoDownload, items[0].DownloadAction)

	shareID := uuid.NewString()
	expiresAt := time.Now().Add(time.Hour).UTC()
	require.NoError(t, db.Exec(`INSERT INTO share_link (id, token, entity_type, entity_id, expires_at, created_at) VALUES (?, 'opaque-token', 'SHARE_LINK_ENTITY_TYPE_POST', ?, ?, ?)`, shareID, postID, expiresAt, time.Now().UTC()).Error)
	shareWitness := mediaasset.ContentDownloadOwnerAuthorization{
		ResourceType: "post", ResourceID: postID, Status: "POST_STATUS_DRAFT", DocumentID: documentID,
		Mode: mediaasset.ContentDownloadOwnerAccessShare,
		ShareLink: &mediaasset.ContentDownloadShareLinkWitness{
			ID: shareID, EntityType: "SHARE_LINK_ENTITY_TYPE_POST", EntityID: postID, ExpiresAt: &expiresAt,
		},
	}
	require.NoError(t, db.Exec(`DELETE FROM share_link WHERE id = ?`, shareID).Error)
	items, err = service.HydrateAuthorizedContentBlockMedia(
		mediaasset.WithContentDownloadOwnerAuthorization(context.Background(), shareWitness), loadItems(),
	)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Nil(t, items[0].Delivery)
	require.Equal(t, fileref.NoDownload, items[0].DownloadAction)
}

func newContentBlockMediaUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`ATTACH DATABASE ':memory:' AS kratos`).Error)
	// SQLite returns the Page policy as bytes, matching the PostgreSQL JSONB driver.
	require.NoError(t, db.Exec(`
		CREATE TABLE content_document (id TEXT PRIMARY KEY);
		CREATE TABLE post (id TEXT PRIMARY KEY, status TEXT NOT NULL, content_document_id TEXT NOT NULL);
		CREATE TABLE page (id TEXT PRIMARY KEY, status TEXT NOT NULL, content_document_id TEXT NOT NULL, access_policy BLOB NOT NULL DEFAULT X'7B7D');
		CREATE TABLE work (id TEXT PRIMARY KEY, status TEXT NOT NULL, content_document_id TEXT NOT NULL);
		CREATE TABLE program_event (id TEXT PRIMARY KEY, status TEXT NOT NULL, content_document_id TEXT NOT NULL);
		CREATE TABLE share_link (
			id TEXT PRIMARY KEY,
			token TEXT NOT NULL,
			entity_type TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			label TEXT,
			password_hash TEXT,
			expires_at DATETIME,
			created_at DATETIME NOT NULL
		);
		CREATE TABLE member (
			id TEXT PRIMARY KEY,
			account_identity_id TEXT,
			onboarded INTEGER NOT NULL DEFAULT 0,
			deleted_at DATETIME,
			created_at DATETIME NOT NULL
		);
		CREATE TABLE kratos.identities (
			id TEXT PRIMARY KEY,
			external_id TEXT,
			state TEXT NOT NULL,
			metadata_admin TEXT NOT NULL
		);
		CREATE TABLE content_block (id TEXT PRIMARY KEY, document_id TEXT NOT NULL, kind TEXT NOT NULL);
		CREATE TABLE content_block_attachment (
			block_id TEXT NOT NULL,
			reference_path TEXT NOT NULL,
			selector_kind TEXT NOT NULL,
			file_id TEXT,
			missing_kind TEXT,
			download_audience TEXT NOT NULL DEFAULT 'disabled',
			PRIMARY KEY (block_id, reference_path)
		);
		CREATE TABLE content_block_attachment_download_audience_segment (
			block_id TEXT NOT NULL,
			reference_path TEXT NOT NULL,
			audience_segment_id TEXT NOT NULL,
			PRIMARY KEY (block_id, reference_path, audience_segment_id)
		);
		CREATE TABLE file ( client_media_bundle_id TEXT,
			id TEXT PRIMARY KEY,
			file_name TEXT,
			extension TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			file_size INTEGER NOT NULL,
 duration_seconds INTEGER,
			delete_requested_at DATETIME
		);
		CREATE TABLE file_derivative (
			file_id TEXT NOT NULL,
			type TEXT NOT NULL,
			asset_id TEXT,
			media_generation_id TEXT
		);
		CREATE TABLE public_asset (
			id TEXT PRIMARY KEY,
			source_file_id TEXT,
			kind TEXT NOT NULL,
			extension TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			file_size INTEGER NOT NULL,
			sha256 BLOB NOT NULL,
			disposition TEXT NOT NULL,
			download_filename TEXT,
			status TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)
	`).Error)
	return db
}
