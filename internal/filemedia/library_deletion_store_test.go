package filemedia_test

import (
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLibraryDeletionRejectsReferencesAndReplaysPendingCommands(t *testing.T) {
	f := newLibraryFixture(t)
	free, blocked, pending := filemediatest.CatalogFile("free", nil), filemediatest.CatalogFile("blocked", nil), filemediatest.CatalogFile("pending", nil)
	for _, r := range []domain.CatalogRow{free, blocked, pending} {
		f.store.Files[r.ID] = filemediatest.CatalogFileRecord{Row: r, PendingDeletion: r.ID == pending.ID}
	}
	f.store.References[blocked.ID] = []string{"post.featured_image_file_id"}
	f.store.Usages = []domain.FileUsageRow{{FileID: blocked.ID, Domain: "post", EntityID: uuid.NewString(), ReferencePath: "featured_image"}}
	got, err := f.files.DeleteFiles(f.ctx, filemedia.DeleteFilesCommand{FileIDs: []string{free.ID, blocked.ID, pending.ID}})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{free.ID, pending.ID}, got.AcceptedFileIDs)
	require.Len(t, got.RejectedFiles, 1)
	require.Equal(t, blocked.ID, got.RejectedFiles[0].FileID)
	require.True(t, f.store.Files[free.ID].PendingDeletion)
	require.False(t, f.store.Files[blocked.ID].PendingDeletion)
	require.Equal(t, []string{free.ID}, f.store.FaviconDeletions)
	require.Len(t, f.audit.Records(), 1)
	require.Len(t, f.store.DeletionOutbox, 2)
	_, err = f.files.DeleteFiles(f.ctx, filemedia.DeleteFilesCommand{FileIDs: []string{free.ID, pending.ID}})
	require.NoError(t, err)
	require.Len(t, f.audit.Records(), 1)
	require.Len(t, f.store.DeletionOutbox, 4)
}
func TestLibraryDeletionFailuresRollBackIntentAndOutbox(t *testing.T) {
	for _, mode := range []string{"permission", "inactive", "audit", "queue"} {
		t.Run(mode, func(t *testing.T) {
			f := newLibraryFixture(t)
			row := filemediatest.CatalogFile("rollback", nil)
			f.store.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row}
			switch mode {
			case "permission":
				f.checker.Deny(policyv1.File.ManageLibrary())
			case "inactive":
				f.fence.Deactivate(f.user.IdentityID.String())
			case "audit":
				f.audit.Err = errors.New("audit unavailable")
			case "queue":
				f.store.EnqueueError = errors.New("queue unavailable")
			}
			_, err := f.files.DeleteFiles(f.ctx, filemedia.DeleteFilesCommand{FileIDs: []string{row.ID}})
			require.Error(t, err)
			require.False(t, f.store.Files[row.ID].PendingDeletion)
			require.Empty(t, f.store.DeletionOutbox)
			require.Empty(t, f.store.FaviconDeletions)
		})
	}
}
func TestLibraryFolderDeletionRejectsUsageThenDeletesSubtree(t *testing.T) {
	f := newLibraryFixture(t)
	root, err := f.files.CreateFileFolder(f.ctx, filemedia.CreateFileFolderCommand{Name: "root"})
	require.NoError(t, err)
	child, err := f.files.CreateFileFolder(f.ctx, filemedia.CreateFileFolderCommand{ParentID: root.ID, Name: "child"})
	require.NoError(t, err)
	row, pending := filemediatest.CatalogFile("file", &child.ID), filemediatest.CatalogFile("pending", &child.ID)
	f.store.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row}
	f.store.Files[pending.ID] = filemediatest.CatalogFileRecord{Row: pending, PendingDeletion: true}
	f.store.Usages = []domain.FileUsageRow{{FileID: row.ID, Domain: "post", EntityID: uuid.NewString()}}
	_, err = f.files.DeleteFileFolder(f.ctx, filemedia.DeleteFileFolderCommand{FolderID: root.ID})
	require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
	require.Len(t, f.store.Folders, 2)
	require.False(t, f.store.Files[row.ID].PendingDeletion)
	require.Empty(t, f.store.DeletionOutbox)
	f.store.Usages = nil
	ids, err := f.files.DeleteFileFolder(f.ctx, filemedia.DeleteFileFolderCommand{FolderID: root.ID})
	require.NoError(t, err)
	require.Equal(t, []string{row.ID}, ids)
	require.Empty(t, f.store.Folders)
	require.Nil(t, f.store.Files[row.ID].Row.ParentID)
	require.Nil(t, f.store.Files[pending.ID].Row.ParentID)
	require.Len(t, f.store.DeletionOutbox, 1)
	require.Len(t, f.audit.Records(), 5)
}
func TestStandaloneDeletionKeepsReferenceProtection(t *testing.T) {
	f := newLibraryFixture(t)
	row := filemediatest.CatalogFile("protected", nil)
	f.store.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row}
	f.store.References[row.ID] = []string{"post.featured_image_file_id"}
	err := filemedia.RequestFileDeletion(f.ctx, f.units, row.ID)
	require.ErrorIs(t, err, filemedia.ErrFileStillReferenced)
	require.False(t, f.store.Files[row.ID].PendingDeletion)
	require.Empty(t, f.store.DeletionOutbox)
	require.NoError(t, filemedia.RequestFileDeletion(f.ctx, f.units, uuid.NewString()))
	require.Empty(t, f.store.DeletionOutbox)
}

func TestDeletionTransfersUnboundAssetsAndKeepsAllocatedRetention(t *testing.T) {
	for _, scope := range []string{"derivative", "source"} {
		t.Run(scope, func(t *testing.T) {
			f := newLibraryFixture(t)
			row := filemediatest.CatalogFile("assets", nil)
			f.store.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row}
			unbound, bound, allocated := uuid.NewString(), uuid.NewString(), uuid.NewString()
			for _, id := range []string{unbound, bound, allocated} {
				status := mediaasset.AssetReady
				if id == allocated {
					status = mediaasset.AssetAllocated
				}
				f.store.PutMediaAsset(mediaasset.Asset{ID: id, SourceFileID: &row.ID, Status: status})
				f.store.Derivatives[row.ID] = append(f.store.Derivatives[row.ID], domain.StoredDerivativeDelivery{FileID: row.ID, Type: "FILE_DERIVATIVE_TYPE_THUMBNAIL", AssetID: &id})
			}
			require.NoError(t, f.store.MediaAssets.InsertBinding(f.ctx, mediaasset.AssetBinding{AssetID: bound, OwnerType: "post", OwnerID: uuid.NewString(), BindingKey: "featured_image", SourceFileID: &row.ID}))
			require.NoError(t, f.units.Transaction(f.ctx, false, func(unit mutation.Tx[filemedia.Store]) error {
				if scope == "source" {
					return domain.RequestUnboundSourceAssetsDeletion(f.ctx, unit.Store(), row.ID)
				}
				return domain.RequestUnboundDerivativeAssetsDeletion(f.ctx, unit.Store(), row.ID)
			}))
			a, err := f.store.MediaAssets.Asset(f.ctx, unbound)
			require.NoError(t, err)
			require.Equal(t, mediaasset.AssetDeletePending, a.Status)
			require.NotNil(t, a.DeleteRequestedAt)
			a, err = f.store.MediaAssets.Asset(f.ctx, bound)
			require.NoError(t, err)
			require.Equal(t, mediaasset.AssetReady, a.Status)
			require.Nil(t, a.DeleteRequestedAt)
			a, err = f.store.MediaAssets.Asset(f.ctx, allocated)
			require.NoError(t, err)
			require.Equal(t, mediaasset.AssetAllocated, a.Status)
		})
	}
}

func TestDeletionImpactKeepsDomainOrderUnknownCountsAndFiveUsagePreview(t *testing.T) {
	t.Parallel()
	f := newLibraryFixture(t)
	file := filemediatest.CatalogFile("used", nil)
	f.store.Files[file.ID] = filemediatest.CatalogFileRecord{Row: file}
	for _, kind := range []string{"work", "page", "post", "mystery", "unknown", "page", "post"} {
		f.store.Usages = append(f.store.Usages, domain.FileUsageRow{FileID: file.ID, Domain: kind, EntityID: uuid.NewString(), ReferencePath: "featured_image"})
	}
	values, err := f.files.GetFileDeletionImpact(f.ctx, filemedia.GetFileDeletionImpactQuery{FileIDs: []string{file.ID}})
	require.NoError(t, err)
	require.Len(t, values, 1)
	result := values[0]
	require.Equal(t, file.ID, result.FileID)
	require.EqualValues(t, 7, result.TotalUsageCount)
	require.Equal(t, []*domain.UsageDomainCount{{Domain: domain.UsageUnspecified, Count: 2}, {Domain: domain.UsagePost, Count: 2}, {Domain: domain.UsagePage, Count: 2}, {Domain: domain.UsageWork, Count: 1}}, result.DomainCounts)
	require.True(t, result.HasMoreUsages)
	require.Len(t, result.FirstUsages, 5)
	kinds := make([]domain.UsageDomain, 0, 5)
	for _, usage := range result.FirstUsages {
		kinds = append(kinds, usage.Domain)
		require.EqualValues(t, 1, usage.Count)
	}
	require.Equal(t, []domain.UsageDomain{domain.UsageUnspecified, domain.UsagePage, domain.UsagePage, domain.UsagePost, domain.UsagePost}, kinds)
}
