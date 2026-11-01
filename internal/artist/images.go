package artist

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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

// SetArtistImagesCommand replaces the Artist's ordered images when they
// still match ExpectedRevision; the first is the primary image.
type SetArtistImagesCommand struct {
	ArtistID         string
	FileIDs          []string
	ExpectedRevision string
}

// ImageUpdate is the Artist's images after a change and the OG generation
// run it started.
type ImageUpdate struct {
	Images            []ArtistImageProjection
	OgGenerationRunID *string
}

// SetArtistImages replaces the Artist's ordered images.
func (s *Service) SetArtistImages(ctx context.Context, command SetArtistImagesCommand) (ImageUpdate, error) {
	fileIDs, err := validateOrderedArtistFileIDs(command.FileIDs)
	if err != nil {
		return ImageUpdate{}, err
	}
	if strings.TrimSpace(command.ExpectedRevision) == "" {
		return ImageUpdate{}, domainerr.Required("expected_revision")
	}
	return s.replaceArtistImages(ctx, command.ArtistID, fileIDs, &command.ExpectedRevision)
}

// SetArtistImageCommand makes FileID the Artist's only image.
type SetArtistImageCommand struct {
	ArtistID string
	FileID   string
}

// SetArtistImage replaces the Artist's images with one primary image.
func (s *Service) SetArtistImage(ctx context.Context, command SetArtistImageCommand) (ImageUpdate, error) {
	fileIDs, err := validateOrderedArtistFileIDs([]string{command.FileID})
	if err != nil {
		return ImageUpdate{}, err
	}
	return s.replaceArtistImages(ctx, command.ArtistID, fileIDs, nil)
}

// DeleteArtistImageCommand removes every image of an Artist.
type DeleteArtistImageCommand struct {
	ArtistID string
}

// DeleteArtistImage removes the Artist's images without deleting their
// files.
func (s *Service) DeleteArtistImage(ctx context.Context, command DeleteArtistImageCommand) (ImageUpdate, error) {
	return s.replaceArtistImages(ctx, command.ArtistID, nil, nil)
}

func validateOrderedArtistFileIDs(fileIDs []string) ([]string, error) {
	normalized := make([]string, 0, len(fileIDs))
	seen := make(map[string]struct{}, len(fileIDs))
	for _, value := range fileIDs {
		fileID := strings.TrimSpace(value)
		if fileID == "" {
			return nil, domainerr.InvalidField("file_ids", "must not contain an empty File ID")
		}
		if _, exists := seen[fileID]; exists {
			return nil, domainerr.InvalidField("file_ids", "must not contain duplicates")
		}
		seen[fileID] = struct{}{}
		normalized = append(normalized, fileID)
	}
	return normalized, nil
}

// replaceArtistImages locks the Artist row before the fresh Manage check and
// binds fileIDs in order; a changed primary image refreshes the OG image.
func (s *Service) replaceArtistImages(
	ctx context.Context,
	artistID string,
	fileIDs []string,
	expectedRevision *string,
) (ImageUpdate, error) {
	ogRunID, err := mutation.Run(ctx, s.mutations, mutation.Spec[*artistImageChange]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var artist model.Artist
			if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id").First(&artist, "id = ?", artistID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domainerr.ResourceNotFound("artist", artistID)
				}
				return err
			}
			return nil
		},
		Access: artistEntity.LegacyLockedAccess(s.spiceDB, artistID, policyv1.Artist.Manage),
		Change: func(ctx context.Context, tx *gorm.DB) (*artistImageChange, error) {
			return s.bindArtistImages(ctx, tx, artistID, fileIDs, expectedRevision)
		},
		Audit: func(change *artistImageChange) mutation.Audit {
			if change == nil {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditArtistUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewArtistGalleryAuditRecord(m, artistID, fileIDs)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, change *artistImageChange) error {
			if change == nil {
				return nil
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.publisher, buildManageMediaMutationContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST, artistID, "media.images",
			))
		},
	})
	if err != nil {
		return ImageUpdate{}, err
	}
	images, err := loadArtistImages(ctx, s.runtime, s.db, artistID)
	if err != nil {
		return ImageUpdate{}, err
	}
	sort.SliceStable(images, func(i, j int) bool {
		return images[i].SortOrder < images[j].SortOrder
	})
	update := ImageUpdate{Images: images}
	if ogRunID != nil {
		update.OgGenerationRunID = ogRunID.ogRunID
	}
	return update, nil
}

// artistImageChange is a committed image replacement; nil when the images
// were already fileIDs.
type artistImageChange struct {
	ogRunID *string
}

func (s *Service) bindArtistImages(ctx context.Context, tx *gorm.DB, artistID string, fileIDs []string, expectedRevision *string) (*artistImageChange, error) {
	current, err := loadArtistImages(ctx, s.runtime, tx, artistID)
	if err != nil {
		return nil, err
	}
	if expectedRevision != nil && artistImageRevision(current) != *expectedRevision {
		return nil, domainerr.Precondition("Artist images changed; reload before saving")
	}
	if sameArtistImageFileIDs(current, fileIDs) {
		return nil, nil
	}
	primaryChanged := artistPrimaryImageID(current) != firstString(fileIDs)
	if err := s.runtime.ReplaceArtistImageBindings(ctx, tx, artistID, fileIDs); err != nil {
		return nil, err
	}
	change := &artistImageChange{}
	if primaryChanged {
		runID, err := s.runtime.RequestCurrentWithDB(ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST, artistID, "", false, "artist_primary_image_updated")
		if err != nil {
			return nil, err
		}
		if runID != "" {
			change.ogRunID = &runID
		}
	}
	return change, nil
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
