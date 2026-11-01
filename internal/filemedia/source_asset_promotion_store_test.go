package filemedia_test

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func promotionFile(t *testing.T, s *filemediatest.LibraryStore, body []byte) domain.File {
	t.Helper()
	row := filemediatest.CatalogFile("source", nil)
	row.FileSize = new(int64(len(body)))
	digest := sha256.Sum256(body)
	s.Files[row.ID] = filemediatest.CatalogFileRecord{Row: row, SHA256: digest[:]}
	return domain.File{ID: row.ID, FileName: row.Name, MimeType: *row.MimeType, Extension: *row.Extension, FileSize: *row.FileSize, SHA256: digest[:]}
}
func TestSourcePromotionRetryRetainsAssetAndObjectIdentity(t *testing.T) {
	t.Parallel()
	s := filemediatest.NewLibraryStore()
	body := []byte("verified public source")
	file := promotionFile(t, s, body)
	client, failTransfer, deleted := filemedia.ExportPromotionStorage(t, file, body)
	failTransfer(true)
	service := filemedia.ExportPromotionService(mutationtest.New[filemedia.Store](s), client)
	_, err := filemedia.ExportPromoteSourceFile(service, t.Context(), file.ID, "image")
	require.ErrorContains(t, err, "stream source to public asset")
	failed, err := s.SourcePromotionAsset(t.Context(), file.ID, false)
	require.NoError(t, err)
	require.Equal(t, mediaasset.AssetFailed, failed.Status)
	require.Empty(t, deleted())
	failTransfer(false)
	ref, err := filemedia.ExportPromoteSourceFile(service, t.Context(), file.ID, "image")
	require.NoError(t, err)
	require.Equal(t, failed.ID, ref.GetAssetId())
	ready, err := s.PromotionAsset(t.Context(), failed.ID, false)
	require.NoError(t, err)
	require.Equal(t, mediaasset.AssetReady, ready.Status)
	require.Equal(t, failed.ObjectKey, ready.ObjectKey)
	require.Empty(t, deleted())
}
func TestSourcePromotionRejectsAnExistingDifferentContract(t *testing.T) {
	t.Parallel()
	s := filemediatest.NewLibraryStore()
	file := promotionFile(t, s, []byte("body"))
	now := time.Now().UTC()
	id := uuid.NewString()
	existing := mediaasset.Asset{ID: id, SourceFileID: &file.ID, Kind: "attachment", Extension: "webp", MimeType: "image/webp", Disposition: "attachment", DownloadFilename: new("download.webp"), ObjectKey: "asset/" + id + ".webp", Status: mediaasset.AssetReady, CreatedAt: now, UpdatedAt: now}
	s.PutMediaAsset(existing)
	_, _, _, err := filemedia.ExportPrepareSourcePromotion(t.Context(), s, "", file, "image")
	require.ErrorContains(t, err, "different public asset contract")
	got, err := s.PromotionAsset(t.Context(), id, false)
	require.NoError(t, err)
	require.Equal(t, existing, got)
}
func TestSourcePromotionAllocationReconcilesOnlyCommittedUsableStates(t *testing.T) {
	for _, status := range []mediaasset.AssetStatus{mediaasset.AssetAllocated, mediaasset.AssetReady, mediaasset.AssetFailed, mediaasset.AssetDeletePending} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			file := promotionFile(t, s, []byte("body"))
			id := uuid.NewString()
			s.PutMediaAsset(mediaasset.Asset{ID: id, SourceFileID: &file.ID, Kind: "image", Extension: file.Extension, MimeType: file.MimeType, Disposition: "inline", ObjectKey: "asset/" + id + ".webp", Status: status, CreatedAt: time.Now()})
			lost := errors.New("lost allocation acknowledgement")
			a, target, ready, err := filemedia.ExportRecoverSourcePromotionAllocation(t.Context(), mutationtest.New[filemedia.Store](s), file, "image", lost)
			if status == mediaasset.AssetAllocated || status == mediaasset.AssetReady {
				require.NoError(t, err)
				require.Equal(t, id, a.ID)
				require.Equal(t, "asset/"+id+".webp", target.ObjectKey)
				require.Equal(t, status == mediaasset.AssetReady, ready)
			} else {
				require.ErrorIs(t, err, lost)
				require.Nil(t, a)
			}
		})
	}
}
func TestSourcePromotionCompletionReconcilesExactMetadataAndLiveSource(t *testing.T) {
	for _, state := range []string{"committed", "conflicting digest", "wrong source", "pending source", "allocated", "failed"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			body := []byte("ready bytes")
			file := promotionFile(t, s, body)
			id := uuid.NewString()
			a := mediaasset.Asset{ID: id, SourceFileID: &file.ID, Status: mediaasset.AssetReady, FileSize: &file.FileSize, SHA256: file.SHA256}
			switch state {
			case "wrong source":
				a.SourceFileID = new(uuid.NewString())
			case "pending source":
				r := s.Files[file.ID]
				r.PendingDeletion = true
				s.Files[file.ID] = r
			case "allocated":
				a.Status = mediaasset.AssetAllocated
			case "failed":
				a.Status = mediaasset.AssetFailed
			}
			s.PutMediaAsset(a)
			digest := file.SHA256
			if state == "conflicting digest" {
				sum := sha256.Sum256([]byte("other"))
				digest = sum[:]
			}
			lost := errors.New("lost completion acknowledgement")
			resolved, err := filemedia.ExportRecoverSourcePromotionCompletion(t.Context(), mutationtest.New[filemedia.Store](s), file.ID, id, file.FileSize, digest, lost)
			switch state {
			case "committed":
				require.NoError(t, err)
				require.True(t, resolved)
			case "conflicting digest":
				require.ErrorContains(t, err, "conflicts with ready metadata")
			case "allocated", "failed":
				require.NoError(t, err)
				require.False(t, resolved)
			default:
				require.ErrorIs(t, err, lost)
			}
		})
	}
}
func TestSourcePromotionCompletionRejectsPendingDeletionBeforeChangingAsset(t *testing.T) {
	t.Parallel()
	s := filemediatest.NewLibraryStore()
	file := promotionFile(t, s, []byte("body"))
	r := s.Files[file.ID]
	r.PendingDeletion = true
	s.Files[file.ID] = r
	id := uuid.NewString()
	s.PutMediaAsset(mediaasset.Asset{ID: id, SourceFileID: &file.ID, Status: mediaasset.AssetAllocated})
	service := filemedia.ExportPromotionService(mutationtest.New[filemedia.Store](s), nil)
	err := filemedia.ExportCompleteSourcePromotion(service, t.Context(), file.ID, id, file.FileSize, file.SHA256)
	require.ErrorContains(t, err, "file is pending deletion")
	a, err := s.PromotionAsset(t.Context(), id, false)
	require.NoError(t, err)
	require.Equal(t, mediaasset.AssetAllocated, a.Status)
}
