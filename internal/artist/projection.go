package artist

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var artistSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":         ArtistSourceTitleSQL("artist"),
		"slug":         "slug",
		"status":       "status",
		"country_code": "country_code",
		"created_at":   "created_at",
		"updated_at":   "updated_at",
		"published_at": "published_at",
	},
	DefaultSort: ArtistSourceTitleSQL("artist") + " ASC",
}

// checkSlugAvailable checks if an artist slug is available for use
// excludeID can be empty for new artists, or set to the current artist ID when updating
func (s *Service) checkSlugAvailable(ctx context.Context, slug string, excludeID string) error {
	if err := pathslug.Validate(slug); err != nil {
		return err
	}
	if slug == "" {
		return nil // Empty slug is always valid
	}

	var count int64
	query := s.db.WithContext(ctx).Model(&model.Artist{}).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}

	if err := query.Count(&count).Error; err != nil {
		return err
	}

	if count > 0 {
		return domainerr.Duplicate("artist", "slug", slug)
	}

	return nil
}

// CheckArtistSlugAvailableQuery asks whether Slug is free for a new Artist,
// or for the Artist ExcludeArtistID when it is set.
type CheckArtistSlugAvailableQuery struct {
	Slug            string
	ExcludeArtistID *string
}

// CheckArtistSlugAvailable reports whether no other Artist and no route use
// the slug. Checking for an existing Artist requires Edit on it; for a new
// one, the platform-wide create action.
func (s *Service) CheckArtistSlugAvailable(ctx context.Context, query CheckArtistSlugAvailableQuery) (bool, error) {
	existing := query.ExcludeArtistID != nil && *query.ExcludeArtistID != ""
	if existing {
		if err := artistEntity.RequirePermission(ctx, s.spiceDB, *query.ExcludeArtistID, policyv1.Artist.Edit); err != nil {
			return false, err
		}
	} else if err := artistEntity.RequireCreate(ctx, s.spiceDB); err != nil {
		return false, err
	}
	if err := pathslug.Validate(query.Slug); err != nil {
		return false, err
	}
	artists := s.db.WithContext(ctx).Model(&model.Artist{}).Where("slug = ?", query.Slug)
	if existing {
		artists = artists.Where("id != ?", *query.ExcludeArtistID)
	}
	var count int64
	if err := artists.Count(&count).Error; err != nil {
		return false, err
	}
	if count != 0 {
		return false, nil
	}
	return s.runtime.IsResourceRouteAvailable(ctx, s.db, "artists", query.Slug)
}

// getArtistLabelIDs returns the Artist's linked Labels in link order,
// without draft Labels.
func (s *Service) getArtistLabelIDs(ctx context.Context, artistID string) ([]string, error) {
	var linked []string
	if err := s.db.WithContext(ctx).
		Table("artist_label").
		Select("label_id::text").
		Where("artist_id = ?", artistID).
		Order("sort_order ASC NULLS LAST, label_id ASC").
		Scan(&linked).Error; err != nil {
		return nil, err
	}
	drafts, err := s.labels.DraftLabelIDs(ctx, s.db, linked)
	if err != nil {
		return nil, err
	}
	labelIDs := make([]string, 0, len(linked))
	for _, labelID := range linked {
		if !drafts[labelID] {
			labelIDs = append(labelIDs, labelID)
		}
	}
	return labelIDs, nil
}

// artistDocument is an Artist's source-locale name, content document and
// OG image.
type artistDocument struct {
	Name      string
	Document  *contentv1.RichTextDocument
	Revision  string
	OgAssetID *string
}

// overlayArtistSourceLocaleDocument rereads the Artist row into artist and
// returns its source-locale document from the same snapshot.
func (s *Service) overlayArtistSourceLocaleDocument(ctx context.Context, artist *model.Artist) (artistDocument, error) {
	artistID := artist.ID
	projection, err := loadCreativeManageContentProjection(
		ctx, s.db, s.contentBlocks, artistID,
		func(ctx context.Context, tx *gorm.DB) error {
			var current model.Artist
			if err := tx.WithContext(ctx).
				Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
				Where("id = ?", artistID).
				Take(&current).Error; err != nil {
				return err
			}
			*artist = current
			return nil
		},
	)
	if err != nil {
		return artistDocument{}, err
	}
	return artistDocument{
		Name: projection.SourceTitle, Document: projection.Document, Revision: projection.Revision,
		OgAssetID: projection.SourceOgAssetID,
	}, nil
}

func (s *Service) overlayArtistSourceLocaleDocuments(ctx context.Context, artists []model.Artist) ([]artistDocument, error) {
	documents := make([]artistDocument, len(artists))
	for i := range artists {
		var err error
		if documents[i], err = s.overlayArtistSourceLocaleDocument(ctx, &artists[i]); err != nil {
			return nil, err
		}
	}
	return documents, nil
}

func (s *Service) loadReadyArtistOgAssets(ctx context.Context, documents []artistDocument) (map[string]*commonv1.AssetRef, error) {
	candidates := make([]*string, 0, len(documents))
	for i := range documents {
		candidates = append(candidates, documents[i].OgAssetID)
	}
	return loadReadyManageOgAssetRefs(ctx, s.runtime, s.db, candidates...)
}

func (s *Service) artistWithReadyOg(ctx context.Context, artist *model.Artist, document artistDocument) (*managev1.Artist, error) {
	ogAsset, err := readyManageOgAssetRef(ctx, s.runtime, s.db, document.OgAssetID)
	if err != nil {
		return nil, err
	}
	images, err := loadArtistImages(ctx, s.runtime, s.db, artist.ID)
	if err != nil {
		return nil, err
	}
	protoArtist := toProtoArtist(artist, document, ogAsset)
	applyManageArtistImages(protoArtist, images)
	return protoArtist, nil
}

// toProtoArtist converts a model.Artist to protobuf Artist
func toProtoArtist(
	a *model.Artist,
	document artistDocument,
	ogAsset *commonv1.AssetRef,
) *managev1.Artist {
	artist := &managev1.Artist{
		Id:           a.ID,
		SourceLocale: a.SourceLocale,
		Name:         document.Name,
		Document:     document.Document,
		Status:       a.Status,
		CreatedAt:    timestamppb.New(a.CreatedAt),
		OgAsset:      ogAsset,
		Revision:     document.Revision,
	}

	if a.Slug != nil {
		artist.Slug = a.Slug
	}
	if a.RealName != nil {
		artist.RealName = a.RealName
	}
	if a.ParentArtistID != nil {
		artist.ParentArtistId = a.ParentArtistID
	}
	if a.CountryCode != nil {
		artist.CountryCode = a.CountryCode
	}
	if a.Website != nil {
		artist.Website = a.Website
	}
	if a.PublishedAt != nil {
		artist.PublishedAt = timestamppb.New(*a.PublishedAt)
	}
	if a.UpdatedAt != nil {
		artist.UpdatedAt = timestamppb.New(*a.UpdatedAt)
	}
	if a.SocialLinks != nil {
		artist.SocialLinks = a.SocialLinks
	}
	return artist
}
