package post

import (
	"context"
	"errors"
	"testing"

	optional "github.com/echovisionlab/geul-api/internal/optional"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestFilesDelegatesPostOwnedFileOperations(t *testing.T) {
	wantErr := errors.New("file operation failed")
	fake := &recordingManageFiles{err: wantErr}
	display := &recordingDisplayFiles{err: wantErr}
	adapter := NewFiles(fake, display)

	require.ErrorIs(t, adapter.DeleteFileByID(t.Context(), "file-1"), wantErr)
	_, err := adapter.ResolveAuthorizedPostFeaturedImage(t.Context(), "post-1", "file-2")
	require.ErrorIs(t, err, wantErr)
	_, err = adapter.ResolveDisplayMedia(t.Context(), []string{"file-2", "file-3"})
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
	require.Equal(t, blockID.String(), optional.Value(optional.Value(items[0]).Selector).BlockID)
	require.Equal(t, "primary", optional.Value(optional.Value(items[0]).Selector).ReferencePath)
	require.Equal(t, fileID, optional.Value(items[0]).Attachment.ActiveFileID())
	require.Equal(t, items, fake.hydrated)
}

func TestPublicFilesDelegatesPublicDisplayMedia(t *testing.T) {
	want := map[string]*fileref.Delivery{"file-1": {FileID: "file-1"}}
	fake := &recordingPublicFiles{deliveries: want}

	got, err := NewPublicFiles(newPostFilesAdapterDB(t), fake).
		ResolveDisplayMedia(t.Context(), []string{"file-1"})
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
) (*filemediadomain.Delivery, error) {
	f.postID = postID
	f.expectedFileID = expectedFileID
	return nil, f.err
}

type recordingDisplayFiles struct {
	resolvedIDs []string
	err         error
}

func (f *recordingDisplayFiles) ResolveDisplayMedia(
	_ context.Context,
	fileIDs []string,
) (map[string]*fileref.Delivery, error) {
	f.resolvedIDs = append([]string(nil), fileIDs...)
	return nil, f.err
}

type recordingPublicFiles struct {
	resolvedIDs []string
	deliveries  map[string]*fileref.Delivery
	hydrated    []*fileref.BlockMediaItem
}

func (f *recordingPublicFiles) ResolveDisplayMedia(
	_ context.Context,
	fileIDs []string,
) (map[string]*fileref.Delivery, error) {
	f.resolvedIDs = append([]string(nil), fileIDs...)
	return f.deliveries, nil
}

func (f *recordingPublicFiles) HydrateAuthorizedContentBlockMedia(
	_ context.Context,
	items []*fileref.BlockMediaItem,
) ([]*fileref.BlockMediaItem, error) {
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
