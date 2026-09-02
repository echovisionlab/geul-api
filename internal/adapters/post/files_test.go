package post

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

func TestFilesDelegatesPostOwnedFileOperations(t *testing.T) {
	wantErr := errors.New("file operation failed")
	fake := &recordingManageFiles{err: wantErr}
	display := &recordingDisplayFiles{err: wantErr}
	adapter := NewFiles(fake, display)

	require.ErrorIs(t, adapter.DeleteFileByID(t.Context(), "file-1"), wantErr)
	_, err := adapter.ResolveAuthorizedPostFeaturedImage(t.Context(), "post-1", "file-2")
	require.ErrorIs(t, err, wantErr)
	_, err = adapter.ResolvePublicDisplayMedia(t.Context(), []string{"file-2", "file-3"})
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, "file-1", fake.deletedID)
	require.Equal(t, "post-1", fake.postID)
	require.Equal(t, "file-2", fake.expectedFileID)
	require.Equal(t, []string{"file-2", "file-3"}, display.resolvedIDs)
}

func TestPublicFilesLoadsExactAuthorizedReferencesBeforeHydration(t *testing.T) {
	db := newPostFilesAdapterDB(t)
	documentID, blockID, fileID := uuid.New(), uuid.New(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id) VALUES (?, ?)`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
		VALUES (?, 'primary', 'active', ?), (?, 'ignored', 'pending', ?)
	`, blockID, fileID, blockID, uuid.NewString()).Error)

	fake := &recordingPublicFiles{}
	items, err := NewPublicFiles(db, fake).ResolveAuthorizedContentBlockMedia(t.Context(), documentID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, blockID.String(), items[0].GetSelector().GetBlockId())
	require.Equal(t, "primary", items[0].GetSelector().GetReferencePath())
	require.Equal(t, fileID, items[0].GetAttachment().GetActiveFileId())
	require.Equal(t, items, fake.hydrated)
}

func TestPublicFilesDelegatesPublicDisplayMedia(t *testing.T) {
	want := map[string]*commonv1.MediaDelivery{"file-1": {FileId: "file-1"}}
	fake := &recordingPublicFiles{deliveries: want}

	got, err := NewPublicFiles(newPostFilesAdapterDB(t), fake).
		ResolvePublicDisplayMedia(t.Context(), []string{"file-1"})
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, []string{"file-1"}, fake.resolvedIDs)
}

type recordingManageFiles struct {
	deletedID      string
	postID         string
	expectedFileID string
	err            error
}

func (f *recordingManageFiles) DeleteFileByID(_ context.Context, fileID string) error {
	f.deletedID = fileID
	return f.err
}

func (f *recordingManageFiles) ResolveAuthorizedPostFeaturedImage(
	_ context.Context,
	postID string,
	expectedFileID string,
) (*commonv1.MediaDelivery, error) {
	f.postID = postID
	f.expectedFileID = expectedFileID
	return nil, f.err
}

type recordingDisplayFiles struct {
	resolvedIDs []string
	err         error
}

func (f *recordingDisplayFiles) ResolvePublicDisplayMedia(
	_ context.Context,
	fileIDs []string,
) (map[string]*commonv1.MediaDelivery, error) {
	f.resolvedIDs = append([]string(nil), fileIDs...)
	return nil, f.err
}

type recordingPublicFiles struct {
	resolvedIDs []string
	deliveries  map[string]*commonv1.MediaDelivery
	hydrated    []*contentv1.ContentBlockMediaItem
}

func (f *recordingPublicFiles) ResolvePublicDisplayMedia(
	_ context.Context,
	fileIDs []string,
) (map[string]*commonv1.MediaDelivery, error) {
	f.resolvedIDs = append([]string(nil), fileIDs...)
	return f.deliveries, nil
}

func (f *recordingPublicFiles) HydrateAuthorizedContentBlockMedia(
	_ context.Context,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	f.hydrated = items
	return items, nil
}

func newPostFilesAdapterDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE content_block (id TEXT PRIMARY KEY, document_id TEXT NOT NULL);
		CREATE TABLE content_block_attachment (
			block_id TEXT NOT NULL,
			reference_path TEXT NOT NULL,
			selector_kind TEXT NOT NULL,
			file_id TEXT NOT NULL
		);
	`).Error)
	return db
}
