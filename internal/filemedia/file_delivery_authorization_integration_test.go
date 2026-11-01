//go:build integration

package filemedia

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestAuthorizeManageFileDeliveriesUsesCurrentPostAttachmentCurrentSchemaIntegration(t *testing.T) {
	t.Parallel()
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
	postID, fileID := seedCurrentSchemaPostFileDelivery(t, pg.DB)

	svc := &Service{db: pg.DB}
	usages, err := svc.currentManageFileDeliveryUsages(t.Context(), []string{fileID})
	require.NoError(t, err)
	require.Len(t, usages[fileID], 1)
	require.Equal(t, "content_attachment", usages[fileID][0].kind)
	require.Equal(t, "post", usages[fileID][0].resourceType)
	require.Equal(t, postID, usages[fileID][0].resourceID)

	var bindingCount int64
	require.NoError(t, pg.DB.Table("file_ingest_binding").Where("file_id = ?", fileID).Count(&bindingCount).Error)
	require.Zero(t, bindingCount)
}

func seedCurrentSchemaPostFileDelivery(t *testing.T, db *gorm.DB) (string, string) {
	t.Helper()
	postID, documentID, blockID, fileID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'post')`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, content_document_id) VALUES (?, ?)`, postID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO file (id, file_name, extension, mime_type, file_size)
		VALUES (?, 'post-attachment', 'mp4', 'video/mp4', 4096)
	`, fileID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block (id, document_id, container_slot, position, kind)
		VALUES (?, ?, 'content', 0, 'file')
	`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
		VALUES (?, 'primary', 'active', ?)
	`, blockID, fileID).Error)
	return postID, fileID
}
