package label

import (
	"context"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *LabelService) CheckLabelSlugAvailable(
	ctx context.Context,
	req *connect.Request[managev1.CheckLabelSlugAvailableRequest],
) (*connect.Response[managev1.CheckLabelSlugAvailableResponse], error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errs.AuthenticationRequired()
	}

	if req.Msg.ExcludeLabelId != nil {
		// Editing existing label - check edit permission (admin or manager)
		if err := requireLabelPermission(ctx, s.spiceDB, *req.Msg.ExcludeLabelId, policyv1.Label.Edit); err != nil {
			return nil, err
		}
	} else {
		// Creating new label - only admins can create labels
		if err := requireLabelCreate(ctx, s.spiceDB); err != nil {
			return nil, err
		}
	}
	if err := validateSlugWithoutSlash(req.Msg.Slug); err != nil {
		return nil, err
	}

	var count int64

	query := s.db.WithContext(ctx).Model(&model.Label{}).Where("slug = ?", req.Msg.Slug)

	if req.Msg.ExcludeLabelId != nil {
		query = query.Where("id != ?", *req.Msg.ExcludeLabelId)
	}

	if err := query.Count(&count).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if count == 0 {
		available, err := routeregistry.IsResourceRouteAvailable(ctx, s.db, "labels", req.Msg.Slug)
		if err != nil {
			return nil, err
		}
		if !available {
			count = 1
		}
	}

	return connect.NewResponse(&managev1.CheckLabelSlugAvailableResponse{
		Available: count == 0,
	}), nil
}

// =============================================================================
// Helper Methods
// =============================================================================

type labelImageAssets struct {
	Light *commonv1.AssetRef
	Dark  *commonv1.AssetRef
}

func (s *LabelService) loadReadyLabelLogoAssets(
	ctx context.Context,
	labels []model.Label,
) (map[string]*commonv1.AssetRef, error) {
	fileIDs := make([]string, 0, len(labels)*2)
	seen := make(map[string]struct{}, len(labels)*2)
	for _, label := range labels {
		for _, candidate := range []*string{label.LogoLightFileID, label.LogoDarkFileID} {
			if candidate == nil || strings.TrimSpace(*candidate) == "" {
				continue
			}
			if _, ok := seen[*candidate]; ok {
				continue
			}
			seen[*candidate] = struct{}{}
			fileIDs = append(fileIDs, *candidate)
		}
	}
	refs := make(map[string]*commonv1.AssetRef, len(fileIDs))
	if len(fileIDs) == 0 {
		return refs, nil
	}
	refs, err := s.runtime.ResolveReadySourceFileRefs(ctx, s.db, fileIDs, "logo")
	if err != nil {
		return nil, errs.Internal(err)
	}
	return refs, nil
}

func labelImageAssetsFromReadyMap(label *model.Label, refs map[string]*commonv1.AssetRef) *labelImageAssets {
	result := &labelImageAssets{}
	if label == nil {
		return result
	}
	if label.LogoLightFileID != nil {
		result.Light = refs[*label.LogoLightFileID]
	}
	if label.LogoDarkFileID != nil {
		result.Dark = refs[*label.LogoDarkFileID]
	}
	return result
}

func (s *LabelService) getLabelImageAssets(ctx context.Context, labelID string) *labelImageAssets {
	var label model.Label
	if err := s.db.WithContext(ctx).Select("id", "logo_light_file_id", "logo_dark_file_id").First(&label, "id = ?", labelID).Error; err != nil {
		slog.Warn("failed to load label logo assets", "label_id", labelID, "error", err)
		return &labelImageAssets{}
	}
	refs, err := s.loadReadyLabelLogoAssets(ctx, []model.Label{label})
	if err != nil {
		slog.Warn("failed to resolve label logo assets", "label_id", labelID, "error", err)
		return &labelImageAssets{}
	}
	return labelImageAssetsFromReadyMap(&label, refs)
}

func (s *LabelService) overlayLabelSourceLocaleDocument(ctx context.Context, label *model.Label) error {
	if label == nil {
		return nil
	}
	labelID := label.ID
	projection, err := loadCreativeManageContentProjection(
		ctx, s.db, s.contentBlocks, labelContentEntity, labelID,
		func(ctx context.Context, tx *gorm.DB) error {
			var current model.Label
			if err := tx.WithContext(ctx).
				Clauses(clause.Locking{Strength: "SHARE"}).
				Where("id = ?", labelID).
				Take(&current).Error; err != nil {
				return err
			}
			*label = current
			return nil
		},
	)
	if err != nil {
		return err
	}
	label.Name = projection.SourceTitle
	label.ContentDocument = projection.Document
	label.ContentRevision = projection.Revision
	return nil
}

func (s *LabelService) overlayLabelSourceLocaleDocuments(ctx context.Context, labels []model.Label) error {
	for i := range labels {
		if err := s.overlayLabelSourceLocaleDocument(ctx, &labels[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *LabelService) loadReadyLabelOgAssets(
	ctx context.Context,
	labels []model.Label,
) (map[string]*commonv1.AssetRef, error) {
	candidates := make([]*string, 0, len(labels))
	for i := range labels {
		candidates = append(candidates, labelOgAssetIDForResponse(&labels[i]))
	}
	return loadReadyManageOgAssetRefs(ctx, s.runtime, s.db, candidates...)
}

func labelOgAssetIDForResponse(label *model.Label) *string {
	if label == nil || effectiveLabelLogoFileID(label.LogoLightFileID, label.LogoDarkFileID) == nil {
		return nil
	}
	return label.OgAssetID
}

func (s *LabelService) labelResponseWithReadyOg(
	ctx context.Context,
	label *model.Label,
	imageAssets *labelImageAssets,
) (*connect.Response[managev1.Label], error) {
	ogAsset, err := readyManageOgAssetRef(ctx, s.runtime, s.db, labelOgAssetIDForResponse(label))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(s.toProtoLabel(label, imageAssets, ogAsset)), nil
}

// toProtoLabel converts a model.Label to protobuf Label
func (s *LabelService) toProtoLabel(
	l *model.Label,
	imageAssets *labelImageAssets,
	ogAsset *commonv1.AssetRef,
) *managev1.Label {
	if labelOgAssetIDForResponse(l) == nil {
		ogAsset = nil
	}
	label := &managev1.Label{
		Id:           l.ID,
		Name:         l.Name,
		Document:     l.ContentDocument,
		Status:       l.Status,
		CreatedAt:    timestamppb.New(l.CreatedAt),
		UpdatedAt:    timestamppb.New(l.UpdatedAt),
		OgAsset:      ogAsset,
		Revision:     l.ContentRevision,
		SourceLocale: l.SourceLocale,
	}

	if l.Slug != nil {
		label.Slug = l.Slug
	}
	if l.CountryCode != nil {
		label.CountryCode = l.CountryCode
	}
	if l.Website != nil {
		label.Website = l.Website
	}
	if imageAssets != nil {
		label.ImageLightAsset = imageAssets.Light
		label.ImageDarkAsset = imageAssets.Dark
	}
	if l.ParentLabelID != nil {
		label.ParentLabelId = l.ParentLabelID
	}
	if l.SocialLinks != nil {
		label.SocialLinks = l.SocialLinks
	}
	if l.PublishedAt != nil {
		label.PublishedAt = timestamppb.New(*l.PublishedAt)
	}
	return label
}

// checkSlugAvailable checks if a slug is available for use
// excludeID can be empty for new labels, or set to the current label ID when updating
func (s *LabelService) checkSlugAvailable(ctx context.Context, slug string, excludeID string) error {
	if err := validateSlugWithoutSlash(slug); err != nil {
		return err
	}
	if slug == "" {
		return nil // Empty slug is always valid
	}

	var count int64
	query := s.db.WithContext(ctx).Model(&model.Label{}).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}

	if err := query.Count(&count).Error; err != nil {
		return errs.Internal(err)
	}

	if count > 0 {
		return errs.SlugAlreadyExists("label", slug)
	}

	return nil
}
