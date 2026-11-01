package postpostgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/post"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *store) LockAttachableFiles(ctx context.Context, fileIDs []string) error {
	return mediaasset.LockAttachableFilesForUpdate(ctx, s.tx, fileIDs)
}

func (s *store) FindFile(ctx context.Context, fileID string) error {
	var file model.File
	return notFound(s.tx.WithContext(ctx).First(&file, "id = ?", fileID).Error, post.ErrFileNotFound)
}

func (s *store) ReadyOgAssets(ctx context.Context, assetIDs []string) (map[string]*commonv1.AssetRef, error) {
	ready := make(map[string]*commonv1.AssetRef, len(assetIDs))
	var assets []model.PublicAsset
	if err := s.tx.WithContext(ctx).
		Where("id IN ? AND status = ?", assetIDs, model.PublicAssetStatusReady).
		Find(&assets).Error; err != nil {
		return nil, err
	}
	lifecycle := mediaasset.NewLifecycle(s.tx, s.deps.CDNDomain)
	for _, asset := range assets {
		if asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		ref, err := lifecycle.AssetRef(asset)
		if err != nil {
			return nil, err
		}
		ready[asset.ID] = ref
	}
	return ready, nil
}

func (s *store) BlockMedia(ctx context.Context, postID string, documentID uuid.UUID, principal *auth.UserInfo) ([]*contentv1.ContentBlockMediaItem, error) {
	items, err := s.deps.MediaLoader.LoadContentBlockMediaReferences(ctx, s.tx, documentID)
	if err != nil {
		return nil, err
	}
	return s.deps.Media.HydrateAuthorizedPostBlockMediaWithDB(auth.WithUser(ctx, principal), s.tx, postID, documentID, principal, items)
}

func (s *store) RequestOG(ctx context.Context, postID, locale string, force bool, reason string) (*string, error) {
	plan, err := s.deps.OG.RequestCurrentWithDB(
		ctx, s.tx, managev1.OgEntityType_OG_ENTITY_TYPE_POST, postID, locale, force, reason,
	)
	if err != nil || plan == nil {
		return nil, err
	}
	runID := plan.RunID
	return &runID, nil
}

func (s *store) CancelOG(ctx context.Context, postID string) error {
	if err := ogpostgres.CancelEntity(ctx, s.tx, managev1.OgEntityType_OG_ENTITY_TYPE_POST, postID); err != nil {
		return err
	}
	return mediaasset.NewLifecycle(s.tx, s.deps.CDNDomain).ReleasePublicAssetBindings(ctx, "post", postID, "og")
}

func (s *store) DeleteShareLinks(ctx context.Context, postID string) error {
	return s.deps.ShareLinkCleanup.DeleteEntityShareLinks(ctx, s.tx, postID)
}
