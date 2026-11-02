package filemediatest

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

func (s *LibraryStore) PromotionFile(_ context.Context, id string, lock bool) (domain.File, error) {
	if s.Fail != nil {
		return domain.File{}, s.Fail
	}
	r, ok := s.Files[id]
	if !ok {
		if lock {
			return domain.File{}, domainerr.Precondition("one or more files no longer exist")
		}
		return domain.File{}, domain.ErrFileNotFound
	}
	f := deliveryFile(r)
	if lock {
		if f.DeleteRequestedAt != nil {
			return domain.File{}, domainerr.Precondition("file is pending deletion")
		}
		s.DeliveryLocks = append(s.DeliveryLocks, "promotion file")
		return domain.File{ID: id, MimeType: f.MimeType, FileSize: f.FileSize, Extension: f.Extension}, nil
	}
	return domain.File{ID: id, DeleteRequestedAt: f.DeleteRequestedAt}, nil
}
func (s *LibraryStore) SourcePromotionAsset(ctx context.Context, id string, _ bool) (mediaasset.Asset, error) {
	if s.Fail != nil {
		return mediaasset.Asset{}, s.Fail
	}
	var latest mediaasset.Asset
	found := false
	for assetID := range s.AssetIDs {
		a, err := s.MediaAssets.Asset(ctx, assetID)
		if errors.Is(err, mediaasset.ErrAssetNotFound) {
			continue
		}
		if err != nil {
			return mediaasset.Asset{}, err
		}
		if a.SourceFileID != nil && *a.SourceFileID == id && a.Status != mediaasset.AssetDeleted && (!found || a.CreatedAt.After(latest.CreatedAt)) {
			latest = a
			found = true
		}
	}
	if !found {
		return mediaasset.Asset{}, mediaasset.ErrAssetNotFound
	}
	return latest, nil
}
func (s *LibraryStore) PromotionAsset(ctx context.Context, id string, lock bool) (mediaasset.Asset, error) {
	if s.Fail != nil {
		return mediaasset.Asset{}, s.Fail
	}
	if lock {
		return s.MediaAssets.LockAsset(ctx, id)
	}
	return s.MediaAssets.Asset(ctx, id)
}
func (s *LibraryStore) ResumePromotionAsset(ctx context.Context, id string, clearDeletion bool, now time.Time) (bool, error) {
	a, err := s.PromotionAsset(ctx, id, false)
	if errors.Is(err, mediaasset.ErrAssetNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a.Status != mediaasset.AssetFailed {
		return false, nil
	}
	a.FileSize = nil
	a.SHA256 = nil
	a.Status = mediaasset.AssetAllocated
	a.ReadyAt = nil
	a.FailedAt = nil
	a.FailureReason = nil
	a.UpdatedAt = now
	if clearDeletion {
		a.DeleteRequestedAt = nil
		a.DeletedAt = nil
	}
	s.PutMediaAsset(a)
	s.Writes++
	return true, nil
}
func (s *LibraryStore) FailPromotionAsset(ctx context.Context, id, reason string, now time.Time) (bool, error) {
	a, err := s.PromotionAsset(ctx, id, false)
	if errors.Is(err, mediaasset.ErrAssetNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a.Status != mediaasset.AssetAllocated {
		return false, nil
	}
	a.Status = mediaasset.AssetFailed
	a.FailedAt = &now
	a.FailureReason = &reason
	a.UpdatedAt = now
	s.PutMediaAsset(a)
	s.Writes++
	return true, nil
}
func (s *LibraryStore) promotionLifecycle(cdn string) *mediaasset.Lifecycle {
	for id, r := range s.Files {
		f := deliveryFile(r)
		s.MediaAssets.PutFile(id, f.DeleteRequestedAt)
	}
	return mediaasset.NewLifecycle(mutationtest.New[mediaasset.Store](s.MediaAssets), cdn)
}
func (s *LibraryStore) AllocatePromotionAsset(ctx context.Context, cdn string, input mediaasset.Allocation) (*mediaasset.Asset, *mediaasset.WriteTarget, error) {
	if s.Fail != nil {
		return nil, nil, s.Fail
	}
	a, target, err := s.promotionLifecycle(cdn).AllocatePublicAsset(ctx, input)
	if err == nil {
		s.AssetIDs[a.ID] = true
	}
	return a, target, err
}
func (s *LibraryStore) CompletePromotionAsset(ctx context.Context, cdn string, input mediaasset.WrittenAsset) error {
	if s.Fail != nil {
		return s.Fail
	}
	_, err := s.promotionLifecycle(cdn).CompletePublicAsset(ctx, &input)
	return err
}
func (s *LibraryStore) ReadyPromotionReference(ctx context.Context, cdn, id string) (*mediaasset.Reference, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	return s.promotionLifecycle(cdn).ReadyAssetRef(ctx, id)
}

func (s *LibraryStore) ReadyPromotionReferenceForFile(ctx context.Context, cdn, file, kind string) (*mediaasset.Reference, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	return s.promotionLifecycle(cdn).ReadyAssetRefForSourceFile(ctx, file, kind)
}
