package artist

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// ArtistImageProjection is the ordered Artist-to-File relation plus its ready
// public image asset. The first entry is the sole primary image.
type ArtistImageProjection struct {
	ArtistID  string
	FileID    string
	SortOrder int32
	Asset     *commonv1.AssetRef
}

func loadArtistImages(
	ctx context.Context,
	runtime Runtime,
	db *gorm.DB,
	artistID string,
) ([]ArtistImageProjection, error) {
	images, err := runtime.LoadArtistImages(ctx, db, []string{artistID})
	if err != nil {
		return nil, err
	}
	return images[artistID], nil
}

func artistImageRevision(images []ArtistImageProjection) string {
	hash := sha256.New()
	var size [4]byte
	for _, image := range images {
		binary.BigEndian.PutUint32(size[:], uint32(len(image.FileID)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(image.FileID))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func applyManageArtistImages(artist *managev1.Artist, images []ArtistImageProjection) {
	if artist == nil {
		return
	}
	artist.Images = make([]*managev1.ArtistImage, 0, len(images))
	for index, image := range images {
		primary := index == 0
		artist.Images = append(artist.Images, &managev1.ArtistImage{
			FileId:    image.FileID,
			Asset:     image.Asset,
			SortOrder: int32(index),
			Primary:   primary,
		})
		if primary {
			artist.ImageAsset = image.Asset
		}
	}
}

func (s *ArtistService) SetArtistImages(
	ctx context.Context,
	req *connect.Request[managev1.SetArtistImagesRequest],
) (*connect.Response[managev1.SetArtistImagesResponse], error) {
	fileIDs, err := validateOrderedArtistFileIDs(req.Msg.FileIds)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Msg.ExpectedRevision) == "" {
		return nil, errs.Required("expected_revision")
	}

	images, ogRunID, changed, err := s.replaceArtistImages(ctx, req.Msg.ArtistId, fileIDs, &req.Msg.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	responseImages := make([]*managev1.ArtistImage, 0, len(images))
	artist := &managev1.Artist{}
	applyManageArtistImages(artist, images)
	responseImages = append(responseImages, artist.Images...)
	if changed {
		publishContentUpdatedEvent(ctx, s.asyncPublisher, buildManageMediaMutationContentUpdatedEvent(managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST, req.Msg.ArtistId, "media.images"))
	}
	return connect.NewResponse(&managev1.SetArtistImagesResponse{
		Images:            responseImages,
		Revision:          artistImageRevision(images),
		OgGenerationRunId: ogRunID,
	}), nil
}

func validateOrderedArtistFileIDs(fileIDs []string) ([]string, error) {
	normalized := make([]string, 0, len(fileIDs))
	seen := make(map[string]struct{}, len(fileIDs))
	for _, value := range fileIDs {
		fileID := strings.TrimSpace(value)
		if fileID == "" {
			return nil, errs.InvalidArgument("file_ids", "must not contain an empty File ID")
		}
		if _, exists := seen[fileID]; exists {
			return nil, errs.InvalidArgument("file_ids", "must not contain duplicates")
		}
		seen[fileID] = struct{}{}
		normalized = append(normalized, fileID)
	}
	return normalized, nil
}

func (s *ArtistService) replaceArtistImages(
	ctx context.Context,
	artistID string,
	fileIDs []string,
	expectedRevision *string,
) ([]ArtistImageProjection, *string, bool, error) {
	var ogRunID *string
	changed := false
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockEditableArtistImages(ctx, tx, s.spiceDB, artistID); err != nil {
			return err
		}
		current, err := loadArtistImages(ctx, s.runtime, tx, artistID)
		if err != nil {
			return err
		}
		if expectedRevision != nil && artistImageRevision(current) != *expectedRevision {
			return errs.FailedPrecondition("Artist images changed; reload before saving")
		}
		if sameArtistImageFileIDs(current, fileIDs) {
			return nil
		}
		changed = true
		primaryChanged := artistPrimaryImageID(current) != firstString(fileIDs)
		if err := s.runtime.ReplaceArtistImageBindings(ctx, tx, artistID, fileIDs); err != nil {
			return err
		}
		if err := s.appendArtistGalleryAudit(ctx, tx, artistID, fileIDs); err != nil {
			return err
		}
		if !primaryChanged {
			return nil
		}
		runID, requestErr := s.runtime.RequestCurrentWithDB(
			ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST,
			artistID, "", false, "artist_primary_image_updated",
		)
		if requestErr != nil {
			return requestErr
		}
		if runID != "" {
			ogRunID = &runID
		}
		return nil
	}); err != nil {
		return nil, nil, false, err
	}
	images, err := loadArtistImages(ctx, s.runtime, s.db, artistID)
	if err != nil {
		return nil, nil, false, errs.Internal(err)
	}
	sort.SliceStable(images, func(i, j int) bool {
		return images[i].SortOrder < images[j].SortOrder
	})
	return images, ogRunID, changed, nil
}

func sameArtistImageFileIDs(images []ArtistImageProjection, fileIDs []string) bool {
	if len(images) != len(fileIDs) {
		return false
	}
	for index := range images {
		if images[index].FileID != fileIDs[index] {
			return false
		}
	}
	return true
}

func lockEditableArtistImages(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, artistID string) error {
	var artist model.Artist
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&artist, "id = ?", artistID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFound("artist", artistID)
		}
		return err
	}
	return requireLockedArtistPermission(ctx, tx, spiceDB, artistID, policyv1.Artist.Manage)
}

func artistPrimaryImageID(images []ArtistImageProjection) string {
	if len(images) == 0 {
		return ""
	}
	return images[0].FileID
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
