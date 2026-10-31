package artist

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// artistDocumentMetadataPlan is a parsed Artist document-metadata update:
// the columns Artist shares with Label, its real name and its Labels.
type artistDocumentMetadataPlan struct {
	columns     entityparticipant.MetadataColumns
	realName    entityparticipant.NullableStringUpdate
	labelIDs    []string
	setLabelIDs bool
}

func buildArtistDocumentMetadataPlan(
	input *intrav1.ArtistDocumentMetadataUpdate,
) (artistDocumentMetadataPlan, error) {
	if input == nil {
		return artistDocumentMetadataPlan{}, domainerr.Required("update")
	}
	var plan artistDocumentMetadataPlan
	var err error
	if plan.realName, err = entityparticipant.ParseNullableStringUpdate("real_name", input.RealName); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.columns.CountryCode, err = entityparticipant.ParseNullableStringUpdate("country_code", input.CountryCode); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.columns.Website, err = entityparticipant.ParseNullableStringUpdate("website", input.Website); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.columns.Slug, err = entityparticipant.ParseNullableStringUpdate("slug", input.Slug); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.columns.Parent, err = entityparticipant.ParseNullableStringUpdate("parent_artist_id", input.ParentArtistId); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if err := artistEntity.Complete(&plan.columns, input.SocialLinks != nil, input.SocialLinks.GetValues()); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if input.LabelIds != nil {
		plan.labelIDs, err = normalizeArtistLabelIDs(input.LabelIds.Values)
		if err != nil {
			return artistDocumentMetadataPlan{}, err
		}
		plan.setLabelIDs = true
	}
	if !plan.realName.Present && !plan.columns.Present() && !plan.setLabelIDs {
		return artistDocumentMetadataPlan{}, domainerr.InvalidField("update", "at least one metadata value is required")
	}
	return plan, nil
}

func normalizeArtistLabelIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		parsed, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, domainerr.InvalidField("label_ids", "must contain valid Label IDs")
		}
		id := parsed.String()
		if _, exists := seen[id]; exists {
			return nil, domainerr.InvalidField("label_ids", "must contain unique Label IDs")
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func loadArtistLabelIDsForUpdate(
	ctx context.Context,
	tx *gorm.DB,
	artistID string,
) ([]string, error) {
	var labelIDs []string
	if err := tx.WithContext(ctx).
		Table("artist_label").
		Select("label_id::text").
		Where("artist_id = ?", artistID).
		Order("label_id ASC").
		Scan(&labelIDs).Error; err != nil {
		return nil, err
	}
	return labelIDs, nil
}

// validateArtistLabelIDsExist requires every Label of labelIDs to exist.
func (s *Service) validateArtistLabelIDsExist(ctx context.Context, tx *gorm.DB, labelIDs []string) error {
	if len(labelIDs) == 0 {
		return nil
	}
	existing, err := s.labels.LabelTitles(ctx, tx, labelIDs)
	if err != nil {
		return err
	}
	if len(existing) != len(labelIDs) {
		return domainerr.InvalidField("label_ids", "contains a Label that does not exist")
	}
	return nil
}

func replaceArtistLabelIDs(
	ctx context.Context,
	tx *gorm.DB,
	artistID string,
	labelIDs []string,
) error {
	if err := tx.WithContext(ctx).
		Where("artist_id = ?", artistID).
		Delete(&model.ArtistLabel{}).Error; err != nil {
		return err
	}
	for index, labelID := range labelIDs {
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO artist_label (artist_id, label_id, sort_order)
			VALUES (?::uuid, ?::uuid, ?)
		`, artistID, labelID, index).Error; err != nil {
			return err
		}
	}
	return nil
}

// UpdateArtistDocumentMetadataCommand saves the Artist's document metadata
// from its source-locale collaboration room.
type UpdateArtistDocumentMetadataCommand struct {
	ArtistID             string
	Locale               string
	ExpectedRevision     string
	Update               *intrav1.ArtistDocumentMetadataUpdate
	ContributorMemberIDs []string
}

// MetadataUpdate is a saved document-metadata update in the source locale.
type MetadataUpdate struct {
	Locale  string
	Advance contentblock.AdvanceResult
}

// UpdateArtistDocumentMetadata saves the real name, country, website,
// social links, slug, parent Artist and Labels, moving the SpiceDB parent
// with it. Only the source-locale room may save metadata.
func (s *Service) UpdateArtistDocumentMetadata(ctx context.Context, command UpdateArtistDocumentMetadataCommand) (MetadataUpdate, error) {
	locale, err := requireArtistSourceRoomLocale(ctx, s.db, command.ArtistID, command.Locale)
	if err != nil {
		return MetadataUpdate{}, err
	}
	plan, err := buildArtistDocumentMetadataPlan(command.Update)
	if err != nil {
		return MetadataUpdate{}, err
	}
	expectedRevision, err := uuid.Parse(command.ExpectedRevision)
	if err != nil {
		return MetadataUpdate{}, domainerr.InvalidField("expected_revision", "must be a UUID")
	}
	documentID, err := loadCreativeContentDocumentID(ctx, s.db, command.ArtistID)
	if err != nil {
		return MetadataUpdate{}, err
	}
	parent, err := artistEntity.PlanParentUpdate(ctx, s.db, command.ArtistID, plan.columns.Parent)
	if err != nil {
		return MetadataUpdate{}, err
	}
	advanced, err := s.participants.SaveMetadata(ctx, entityparticipant.MetadataSave{
		EntityID: command.ArtistID, Documents: s.contentBlocks, DocumentID: documentID, ExpectedRevision: expectedRevision,
		Parent: parent, Access: s.contributorAccess(command.ArtistID, command.ContributorMemberIDs),
		Fence: artistSourceRoomFence(command.ArtistID, locale),
		Apply: func(ctx context.Context, tx *gorm.DB, writesParent bool) (entityparticipant.MetadataApplied, error) {
			return s.applyArtistDocumentMetadata(ctx, tx, command.ArtistID, plan, parent, writesParent)
		},
		NormalizeError: func(err error) error { return normalizeCreativeContentBlockError(artistContentEntity, err) },
	})
	return MetadataUpdate{Locale: locale, Advance: advanced}, err
}

// applyArtistDocumentMetadata writes the changed metadata columns of the
// Artist row and replaces its Labels under the content fence.
func (s *Service) applyArtistDocumentMetadata(ctx context.Context, tx *gorm.DB, artistID string, plan artistDocumentMetadataPlan, parent entityparticipant.ParentUpdate, writesParent bool) (entityparticipant.MetadataApplied, error) {
	var current model.Artist
	if err := tx.WithContext(ctx).
		Select("id", "real_name", "country_code", "website", "social_links", "slug", "parent_artist_id").
		Where("id = ?", artistID).
		Take(&current).Error; err != nil {
		return entityparticipant.MetadataApplied{}, err
	}
	applied := entityparticipant.MetadataApplied{CurrentParent: current.ParentArtistID}
	fields := structured.Fields{}
	realNameChanged := plan.realName.Assign(fields, "real_name", current.RealName)
	changed, err := artistEntity.AssignColumns(ctx, tx, artistID, plan.columns, parent, writesParent, entityparticipant.MetadataRow{
		Slug: current.Slug, CountryCode: current.CountryCode, Website: current.Website,
		Parent: current.ParentArtistID, SocialLinks: current.SocialLinks,
	}, fields, func(ctx context.Context, tx *gorm.DB, slug string) error {
		if err := ensureSlugAvailable(ctx, tx, &model.Artist{}, "artist", slug, artistID); err != nil {
			return err
		}
		return requireArtistRouteAvailableWithDB(ctx, s.runtime, tx, &slug)
	})
	if err != nil {
		return applied, err
	}
	labelsChanged, err := s.replaceChangedArtistLabels(ctx, tx, artistID, plan)
	if err != nil || (!changed && !realNameChanged && !labelsChanged) {
		return applied, err
	}
	if len(fields) > 0 {
		fields["updated_at"] = time.Now().UTC()
		if err := tx.WithContext(ctx).Model(&model.Artist{}).Where("id = ?", artistID).Updates(fields).Error; err != nil {
			return applied, err
		}
	}
	applied.Changed = true
	return applied, nil
}

// replaceChangedArtistLabels replaces the Artist's Labels when the plan
// sets different ones.
func (s *Service) replaceChangedArtistLabels(ctx context.Context, tx *gorm.DB, artistID string, plan artistDocumentMetadataPlan) (bool, error) {
	currentLabelIDs, err := loadArtistLabelIDsForUpdate(ctx, tx, artistID)
	if err != nil {
		return false, err
	}
	if !plan.setLabelIDs || slices.Equal(currentLabelIDs, plan.labelIDs) {
		return false, nil
	}
	if err := s.validateArtistLabelIDsExist(ctx, tx, plan.labelIDs); err != nil {
		return false, err
	}
	return true, replaceArtistLabelIDs(ctx, tx, artistID, plan.labelIDs)
}
