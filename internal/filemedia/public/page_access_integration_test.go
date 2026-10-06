//go:build integration

package public

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedPublicPageDownload(t *testing.T, db *gorm.DB) (string, string, string, *connect.Request[openv1.AuthorizeDownloadRequest]) {
	t.Helper()
	pageID, documentID, blockID, fileID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document(id,profile) VALUES (?::uuid,'page')`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO page(id,status,content_document_id) VALUES (?::uuid,'PAGE_STATUS_PUBLISHED',?::uuid)`, pageID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO file(id,file_name,extension,mime_type,file_size,sha256) VALUES (?::uuid,'private-page-file','wav','audio/wav',4096,?)`, fileID, make([]byte, 32)).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block(id,document_id,container_slot,position,kind,shared_data) VALUES (?::uuid,?::uuid,'root',0,'file','{}'::jsonb)`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block_attachment(block_id,reference_path,selector_kind,file_id,download_audience) VALUES (?::uuid,'file','active',?::uuid,'public')`, blockID, fileID).Error)
	request := connect.NewRequest(&openv1.AuthorizeDownloadRequest{EntityType: openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_PAGE, EntityId: pageID, RelationTarget: &openv1.AuthorizeDownloadRequest_ContentBlock{ContentBlock: &contentv1.ContentBlockMediaSelector{BlockId: blockID, ReferencePath: "file"}}})
	return pageID, documentID, fileID, request
}

func TestPageAudienceOverridesPublicFileAudienceIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	pageID, documentID, _, request := seedPublicPageDownload(t, db)
	svc := NewFileService(db, publicIntegrationSpiceDBClient(t), "https://cdn.example.test", "https://media.example.test", publicDownloadUnitSecret, mediaauth.DownloadTTL)
	initial, err := svc.AuthorizeDownload(t.Context(), request)
	require.NoError(t, err)
	require.NotEmpty(t, initial.Msg.GetDownload().GetUrl())
	ownerContext := mediaasset.WithContentDownloadOwnerAuthorization(t.Context(), mediaasset.ContentDownloadOwnerAuthorization{ResourceType: "page", ResourceID: pageID, DocumentID: documentID, Status: "PAGE_STATUS_PUBLISHED", Mode: mediaasset.ContentDownloadOwnerAccessPublic})
	items, err := filemedia.LoadContentBlockMediaReferences(t.Context(), db, uuid.MustParse(documentID))
	require.NoError(t, err)
	require.NoError(t, db.Exec(`UPDATE page SET access_policy='{"mode":"PAGE_ACCESS_MODE_AUTHENTICATED"}'::jsonb WHERE id=?::uuid`, pageID).Error)
	denied, err := svc.AuthorizeDownload(t.Context(), request)
	require.NoError(t, err)
	require.Nil(t, denied.Msg.GetDownload())
	require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, denied.Msg.GetAccess().GetAction())
	hydrated, err := svc.HydrateAuthorizedContentBlockMedia(ownerContext, items)
	require.NoError(t, err)
	for _, item := range hydrated {
		require.Nil(t, item.GetDelivery(), "old public owner witness must not issue inline or download media after audience changes")
	}
	require.NoError(t, db.Exec(`UPDATE page SET access_policy='{}'::jsonb WHERE id=?::uuid`, pageID).Error)
	reopened, err := svc.AuthorizeDownload(t.Context(), request)
	require.NoError(t, err)
	require.NotEmpty(t, reopened.Msg.GetDownload().GetUrl())
}

func TestPageAudienceChangeBetweenSigningPhasesIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	pageID, _, _, request := seedPublicPageDownload(t, db)
	loaded, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	serviceDB := db.Session(&gorm.Session{Logger: &relationSourceBlockingLogger{Interface: db.Logger, loaded: loaded, release: release, trigger: 1}})
	svc := NewFileService(serviceDB, publicIntegrationSpiceDBClient(t), "https://cdn.example.test", "https://media.example.test", publicDownloadUnitSecret, mediaauth.DownloadTTL)
	type result struct {
		response *connect.Response[openv1.AuthorizeDownloadResponse]
		err      error
	}
	finished := make(chan result, 1)
	go func() {
		response, err := svc.AuthorizeDownload(context.Background(), request)
		finished <- result{response, err}
	}()
	select {
	case <-loaded:
	case <-time.After(5 * time.Second):
		t.Fatal("signing did not reach phase-one SQL snapshot")
	}
	changed := make(chan error, 1)
	go func() {
		changed <- db.Exec(`UPDATE page SET access_policy='{"mode":"PAGE_ACCESS_MODE_AUTHENTICATED"}'::jsonb WHERE id=?::uuid`, pageID).Error
	}()
	select {
	case err := <-changed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Page audience update could not commit between signing phases")
	}
	close(release)
	select {
	case signed := <-finished:
		require.NoError(t, signed.err)
		require.NotNil(t, signed.response)
		require.Nil(t, signed.response.Msg.GetDownload())
		require.Equal(t, openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE, signed.response.Msg.GetAccess().GetAction())
	case <-time.After(5 * time.Second):
		t.Fatal("signing did not complete after phase-one release")
	}
}
