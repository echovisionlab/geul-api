package workpostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/identitystate"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func (s *store) ReadyAssets(ctx context.Context, assetIDs []string) (map[string]*mediaasset.Reference, error) {
	return s.ports.Runtime.ResolveReadyAssetRefs(ctx, s.tx, assetIDs)
}

func (s *store) ReadyFileAsset(ctx context.Context, fileID, kind string) (*mediaasset.Reference, error) {
	return s.ports.Runtime.ReadyPublicAssetRefForSourceFile(ctx, s.tx, fileID, kind)
}

func (s *store) LockAttachableFile(ctx context.Context, fileID string) error {
	return s.ports.Runtime.LockAttachableFilesForUpdate(ctx, s.tx, []string{fileID})
}

func (s *store) FileID(ctx context.Context, fileID string) (string, error) {
	var file model.File
	if err := s.tx.WithContext(ctx).First(&file, "id = ?", fileID).Error; err != nil {
		return "", notFound(err, work.ErrFileNotFound)
	}
	return file.ID, nil
}

func (s *store) BindFeaturedImage(ctx context.Context, workID, fileID string) (*mediaasset.Reference, error) {
	return s.ports.Runtime.BindReadyAssetForSourceFile(ctx, s.tx, fileID, "work", workID, "featured_image", "image")
}

func (s *store) ReleaseFeaturedImage(ctx context.Context, workID string) error {
	return s.ports.Runtime.ReleasePublicAssetBindings(ctx, s.tx, "work", workID, "featured_image")
}

func (s *store) UnavailableAttachmentKinds(ctx context.Context, document *contentv1.LocalizedRichTextDocument) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	return s.ports.Runtime.LoadUnavailableVersionAttachmentKinds(ctx, s.tx, document)
}

func (s *store) HydrateBlockMedia(ctx context.Context, workID string, documentID uuid.UUID, principal *auth.UserInfo, items []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error) {
	return s.ports.Media.HydrateAuthorizedWorkBlockMediaWithDB(ctx, s.tx, workID, documentID, principal, items)
}

func (s *store) RequestOG(ctx context.Context, workID, locale, reason string) (string, error) {
	return s.ports.Runtime.RequestCurrentWithDB(ctx, s.tx, managev1.OgEntityType_OG_ENTITY_TYPE_WORK, workID, locale, false, reason)
}

func (s *store) CancelOG(ctx context.Context, workID string) error {
	return s.ports.Runtime.CancelAndReleaseEntityWithDB(ctx, s.tx, managev1.OgEntityType_OG_ENTITY_TYPE_WORK, "work", workID)
}

func (s *store) RequireFreshCan(ctx context.Context, fence identitystate.Fence, checker authz.AuthorizationDecisionChecker, can policyv1.Can) error {
	return identitystate.RequireFreshCan(ctx, s.tx, fence, checker, can)
}

func (s *store) ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error) {
	return auth.ResolveAuthenticatedPrincipalBySessionID(ctx, s.tx, sessionID)
}

func (s *store) LockSession(ctx context.Context, sessionID string, principal *auth.UserInfo) error {
	return auth.LockAuthenticatedSessionForPrincipal(ctx, s.tx, sessionID, principal)
}

func (s *store) RequireCurrentContributors(ctx context.Context, workID string, contributors []string) error {
	return s.ports.Checkpoints.RequireCurrentContributors(
		ctx, s.tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_WORK, workID, contributors,
	)
}

func (s *store) LockContributors(ctx context.Context, memberIDs []string) ([]string, error) {
	var locked []struct {
		ID string `gorm:"column:id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Select("id::text").
		Where("id IN ?", memberIDs).
		Find(&locked).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(locked))
	for _, row := range locked {
		ids = append(ids, row.ID)
	}
	return ids, nil
}
