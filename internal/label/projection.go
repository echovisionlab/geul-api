package label

import (
	"context"
	"log/slog"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CheckLabelSlugAvailableQuery asks whether Slug is free for a new Label, or
// for the Label ExcludeLabelID when it is set.
type CheckLabelSlugAvailableQuery struct {
	Slug           string
	ExcludeLabelID *string
}

// CheckLabelSlugAvailable reports whether no other Label and no route use
// the slug. Checking for an existing Label requires Edit on it; for a new
// one, the platform-wide create action.
func (s *Service) CheckLabelSlugAvailable(ctx context.Context, query CheckLabelSlugAvailableQuery) (bool, error) {
	if auth.GetUser(ctx) == nil {
		return false, errAuthenticationRequired
	}
	if query.ExcludeLabelID != nil {
		if err := labelEntity.RequirePermission(ctx, s.spiceDB, *query.ExcludeLabelID, policyv1.Label.Edit); err != nil {
			return false, err
		}
	} else if err := labelEntity.RequireCreate(ctx, s.spiceDB); err != nil {
		return false, err
	}
	if err := pathslug.Validate(query.Slug); err != nil {
		return false, err
	}
	labels := s.db.WithContext(ctx).Model(&model.Label{}).Where("slug = ?", query.Slug)
	if query.ExcludeLabelID != nil {
		labels = labels.Where("id != ?", *query.ExcludeLabelID)
	}
	var count int64
	if err := labels.Count(&count).Error; err != nil {
		return false, err
	}
	if count != 0 {
		return false, nil
	}
	return routeregistry.IsResourceRouteAvailable(ctx, s.db, "labels", query.Slug)
}

type labelImageAssets struct {
	Light *commonv1.AssetRef
	Dark  *commonv1.AssetRef
}

func (s *Service) loadReadyLabelLogoAssets(
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
	return s.runtime.ResolveReadySourceFileRefs(ctx, s.db, fileIDs, "logo")
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

func (s *Service) getLabelImageAssets(ctx context.Context, labelID string) *labelImageAssets {
	var label model.Label
	if err := s.db.WithContext(ctx).Select("id", "logo_light_file_id", "logo_dark_file_id").First(&label, "id = ?", labelID).Error; err != nil {
		slog.WarnContext(ctx, "Label logo lookup failed; serving the Label without logos", "domain", "label", "operation", "load_label_logo_assets", "label_id", labelID, failurecause.Attr(err))
		return &labelImageAssets{}
	}
	refs, err := s.loadReadyLabelLogoAssets(ctx, []model.Label{label})
	if err != nil {
		slog.WarnContext(ctx, "Label logo asset resolution failed; serving the Label without logos", "domain", "label", "operation", "resolve_label_logo_assets", "label_id", labelID, failurecause.Attr(err))
		return &labelImageAssets{}
	}
	return labelImageAssetsFromReadyMap(&label, refs)
}

// labelDocument is a Label's source-locale name and content document.
type labelDocument struct {
	Name     string
	Document *contentv1.RichTextDocument
	Revision string
}

// overlayLabelSourceLocaleDocument rereads the Label row into label and
// returns its source-locale document from the same snapshot.
func (s *Service) overlayLabelSourceLocaleDocument(ctx context.Context, label *model.Label) (labelDocument, error) {
	labelID := label.ID
	projection, err := loadCreativeManageContentProjection(
		ctx, s.db, s.contentBlocks, labelID,
		func(ctx context.Context, tx *gorm.DB) error {
			var current model.Label
			if err := tx.WithContext(ctx).
				Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
				Where("id = ?", labelID).
				Take(&current).Error; err != nil {
				return err
			}
			*label = current
			return nil
		},
	)
	if err != nil {
		return labelDocument{}, err
	}
	return labelDocument{Name: projection.SourceTitle, Document: projection.Document, Revision: projection.Revision}, nil
}

func (s *Service) overlayLabelSourceLocaleDocuments(ctx context.Context, labels []model.Label) ([]labelDocument, error) {
	documents := make([]labelDocument, len(labels))
	for i := range labels {
		var err error
		if documents[i], err = s.overlayLabelSourceLocaleDocument(ctx, &labels[i]); err != nil {
			return nil, err
		}
	}
	return documents, nil
}

func (s *Service) loadReadyLabelOgAssets(
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

func (s *Service) labelWithReadyOg(
	ctx context.Context,
	label *model.Label,
	document labelDocument,
	imageAssets *labelImageAssets,
) (*managev1.Label, error) {
	ogAsset, err := readyManageOgAssetRef(ctx, s.runtime, s.db, labelOgAssetIDForResponse(label))
	if err != nil {
		return nil, err
	}
	return toProtoLabel(label, document, imageAssets, ogAsset), nil
}

// toProtoLabel converts a model.Label to protobuf Label
func toProtoLabel(
	l *model.Label,
	document labelDocument,
	imageAssets *labelImageAssets,
	ogAsset *commonv1.AssetRef,
) *managev1.Label {
	if labelOgAssetIDForResponse(l) == nil {
		ogAsset = nil
	}
	label := &managev1.Label{
		Id:           l.ID,
		Name:         document.Name,
		Document:     document.Document,
		Status:       string(l.Status),
		CreatedAt:    timestamppb.New(l.CreatedAt),
		UpdatedAt:    timestamppb.New(l.UpdatedAt),
		OgAsset:      ogAsset,
		Revision:     document.Revision,
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
func (s *Service) checkSlugAvailable(ctx context.Context, slug string, excludeID string) error {
	if err := pathslug.Validate(slug); err != nil {
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
		return err
	}

	if count > 0 {
		return domainerr.Duplicate("label", "slug", slug)
	}

	return nil
}
