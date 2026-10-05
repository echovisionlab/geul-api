package artist

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type nullableDocumentStringUpdate struct {
	present bool
	value   *string
}

func parseNullableDocumentStringUpdate(
	field string,
	input *intrav1.NullableStringMutation,
) (nullableDocumentStringUpdate, error) {
	if input == nil {
		return nullableDocumentStringUpdate{}, nil
	}
	switch operation := input.Operation.(type) {
	case *intrav1.NullableStringMutation_Set:
		value := strings.TrimSpace(operation.Set)
		if value == "" {
			return nullableDocumentStringUpdate{}, errs.InvalidArgument(field, "set value must not be empty; use clear instead")
		}
		return nullableDocumentStringUpdate{present: true, value: &value}, nil
	case *intrav1.NullableStringMutation_Clear:
		if !operation.Clear {
			return nullableDocumentStringUpdate{}, errs.InvalidArgument(field, "clear must be true")
		}
		return nullableDocumentStringUpdate{present: true}, nil
	default:
		return nullableDocumentStringUpdate{}, errs.InvalidArgument(field, "set or clear is required")
	}
}

func nullableDocumentStringChanged(current *string, update nullableDocumentStringUpdate) bool {
	return update.present && !sameOptionalString(current, update.value)
}

func assignNullableDocumentString(
	fields structured.Fields,
	column string,
	current *string,
	update nullableDocumentStringUpdate,
) bool {
	if !nullableDocumentStringChanged(current, update) {
		return false
	}
	if update.value == nil {
		fields[column] = nil
	} else {
		fields[column] = *update.value
	}
	return true
}

type artistDocumentMetadataPlan struct {
	realName       nullableDocumentStringUpdate
	countryCode    nullableDocumentStringUpdate
	website        nullableDocumentStringUpdate
	slug           nullableDocumentStringUpdate
	parentArtistID nullableDocumentStringUpdate
	socialLinks    map[string]string
	setSocialLinks bool
	labelIDs       []string
	setLabelIDs    bool
}

func buildArtistDocumentMetadataPlan(
	input *intrav1.ArtistDocumentMetadataUpdate,
) (artistDocumentMetadataPlan, error) {
	if input == nil {
		return artistDocumentMetadataPlan{}, errs.Required("update")
	}
	var plan artistDocumentMetadataPlan
	var err error
	if plan.realName, err = parseNullableDocumentStringUpdate("real_name", input.RealName); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.countryCode, err = parseNullableDocumentStringUpdate("country_code", input.CountryCode); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.website, err = parseNullableDocumentStringUpdate("website", input.Website); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.slug, err = parseNullableDocumentStringUpdate("slug", input.Slug); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.parentArtistID, err = parseNullableDocumentStringUpdate("parent_artist_id", input.ParentArtistId); err != nil {
		return artistDocumentMetadataPlan{}, err
	}
	if plan.slug.value != nil {
		if err := validateSlugWithoutSlash(*plan.slug.value); err != nil {
			return artistDocumentMetadataPlan{}, err
		}
	}
	if plan.parentArtistID.value != nil {
		parsed, err := uuid.Parse(*plan.parentArtistID.value)
		if err != nil {
			return artistDocumentMetadataPlan{}, errs.InvalidArgument("parent_artist_id", "must be a valid Artist ID")
		}
		canonical := parsed.String()
		plan.parentArtistID.value = &canonical
	}
	if input.SocialLinks != nil {
		plan.socialLinks = maps.Clone(input.SocialLinks.Values)
		if plan.socialLinks == nil {
			plan.socialLinks = map[string]string{}
		}
		plan.setSocialLinks = true
	}
	if input.LabelIds != nil {
		plan.labelIDs, err = normalizeArtistLabelIDs(input.LabelIds.Values)
		if err != nil {
			return artistDocumentMetadataPlan{}, err
		}
		plan.setLabelIDs = true
	}
	if !plan.realName.present && !plan.countryCode.present && !plan.website.present &&
		!plan.slug.present && !plan.parentArtistID.present && !plan.setSocialLinks && !plan.setLabelIDs {
		return artistDocumentMetadataPlan{}, errs.InvalidArgument("update", "at least one metadata value is required")
	}
	return plan, nil
}

func normalizeArtistLabelIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		parsed, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, errs.InvalidArgument("label_ids", "must contain valid Label IDs")
		}
		id := parsed.String()
		if _, exists := seen[id]; exists {
			return nil, errs.InvalidArgument("label_ids", "must contain unique Label IDs")
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

func validateArtistLabelIDsExist(
	ctx context.Context,
	tx *gorm.DB,
	labelIDs []string,
) error {
	if len(labelIDs) == 0 {
		return nil
	}
	var count int64
	if err := tx.WithContext(ctx).
		Table("label").
		Where("id IN ?", labelIDs).
		Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(labelIDs)) {
		return errs.InvalidArgument("label_ids", "contains a Label that does not exist")
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

func (s *InternalArtistService) UpdateArtistDocumentMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdateArtistDocumentMetadataRequest],
) (*connect.Response[intrav1.UpdateArtistDocumentMetadataResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Artist content Block store is not configured")
	}
	locale, err := requireArtistSourceRoomLocale(ctx, s.db, req.Msg.ArtistId, req.Msg.Locale)
	if err != nil {
		return nil, err
	}
	plan, err := buildArtistDocumentMetadataPlan(req.Msg.Update)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := uuid.Parse(req.Msg.ExpectedRevision)
	if err != nil {
		return nil, errs.InvalidArgument("expected_revision", "must be a UUID")
	}
	documentID, err := loadCreativeContentDocumentID(ctx, s.db, artistContentEntity, req.Msg.ArtistId)
	if err != nil {
		return nil, err
	}
	previousParent, err := loadCreativeDocumentParent(ctx, s.db, artistContentEntity, req.Msg.ArtistId)
	if err != nil {
		return nil, err
	}
	requiresParentWrite := plan.parentArtistID.present &&
		!sameOptionalString(previousParent, plan.parentArtistID.value)

	var advanced contentblock.AdvanceResult
	mutate := func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := s.translation.RequireDocumentContributors(ctx, tx, req.Msg.ContributorMemberIds); err != nil {
			return err
		}
		if err := requireArtistCollaborationContributors(ctx, tx, s.checkpoints, req.Msg.ArtistId, req.Msg.ContributorMemberIds); err != nil {
			return err
		}
		var advanceErr error
		advanced, advanceErr = s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
			artistSourceRoomFence(req.Msg.ArtistId, locale),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				var current model.Artist
				if err := tx.WithContext(ctx).
					Select("id", "real_name", "country_code", "website", "social_links", "slug", "parent_artist_id").
					Where("id = ?", req.Msg.ArtistId).
					Take(&current).Error; err != nil {
					return contentblock.MetadataEffect{}, err
				}
				parentChanged := nullableDocumentStringChanged(current.ParentArtistID, plan.parentArtistID)
				if plan.parentArtistID.present && (!sameOptionalString(current.ParentArtistID, previousParent) || parentChanged != requiresParentWrite) {
					return contentblock.MetadataEffect{}, errs.FailedPrecondition("Artist parent changed concurrently; reload before saving")
				}
				if parentChanged {
					if write == nil {
						return contentblock.MetadataEffect{}, errs.FailedPrecondition("Artist parent changed concurrently; reload before saving")
					}
					parentID := ""
					if plan.parentArtistID.value != nil {
						parentID = *plan.parentArtistID.value
					}
					if err := validateArtistParentTransition(ctx, tx, req.Msg.ArtistId, parentID); err != nil {
						return contentblock.MetadataEffect{}, err
					}
				}
				fields := structured.Fields{}
				changed := assignNullableDocumentString(fields, "real_name", current.RealName, plan.realName)
				changed = assignNullableDocumentString(fields, "country_code", current.CountryCode, plan.countryCode) || changed
				changed = assignNullableDocumentString(fields, "website", current.Website, plan.website) || changed
				slugChanged := assignNullableDocumentString(fields, "slug", current.Slug, plan.slug)
				changed = slugChanged || changed
				changed = assignNullableDocumentString(fields, "parent_artist_id", current.ParentArtistID, plan.parentArtistID) || changed
				if plan.setSocialLinks && !maps.Equal(current.SocialLinks, plan.socialLinks) {
					fields["social_links"] = plan.socialLinks
					changed = true
				}
				if slugChanged && plan.slug.value != nil {
					if err := ensureSlugAvailable(ctx, tx, &model.Artist{}, "artist", *plan.slug.value, req.Msg.ArtistId); err != nil {
						return contentblock.MetadataEffect{}, err
					}
					if err := requireArtistRouteAvailableWithDB(ctx, s.runtime, tx, plan.slug.value); err != nil {
						return contentblock.MetadataEffect{}, err
					}
				}
				currentLabelIDs, err := loadArtistLabelIDsForUpdate(ctx, tx, req.Msg.ArtistId)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				labelsChanged := plan.setLabelIDs && !slices.Equal(currentLabelIDs, plan.labelIDs)
				if labelsChanged {
					if err := validateArtistLabelIDsExist(ctx, tx, plan.labelIDs); err != nil {
						return contentblock.MetadataEffect{}, err
					}
					if err := replaceArtistLabelIDs(ctx, tx, req.Msg.ArtistId, plan.labelIDs); err != nil {
						return contentblock.MetadataEffect{}, err
					}
					changed = true
				}
				if !changed {
					return contentblock.MetadataEffect{}, nil
				}
				if len(fields) > 0 {
					fields["updated_at"] = time.Now().UTC()
					if err := tx.WithContext(ctx).Model(&model.Artist{}).
						Where("id = ?", req.Msg.ArtistId).
						Updates(fields).Error; err != nil {
						return contentblock.MetadataEffect{}, err
					}
				}
				if parentChanged {
					apply, compensate, err := replaceArtistParentRelationships(req.Msg.ArtistId, current.ParentArtistID, plan.parentArtistID.value)
					if err != nil {
						return contentblock.MetadataEffect{}, err
					}
					if err := write(apply, compensate); err != nil {
						return contentblock.MetadataEffect{}, err
					}
				}
				return contentblock.MetadataEffect{Changed: true}, nil
			},
		)
		if advanceErr != nil {
			return normalizeCreativeContentBlockError(artistContentEntity, advanceErr)
		}
		return nil
	}
	if requiresParentWrite {
		_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, mutate)
	} else {
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return mutate(tx, nil) })
	}
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&intrav1.UpdateArtistDocumentMetadataResponse{
		DocumentRevision: advanced.DocumentRevision.String(),
		Changed:          advanced.Changed, SourceChanged: advanced.TranslationSourceChanged,
		Locale: locale,
	}), nil
}
func loadCreativeDocumentParent(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
) (*string, error) {
	column := "parent_" + entityType + "_id"
	var row struct {
		ParentID *string `gorm:"column:parent_id"`
	}
	result := db.WithContext(ctx).
		Table(entityType).
		Select(column+" AS parent_id").
		Where("id = ?", entityID).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, errs.NotFound(entityType, entityID)
	}
	if result.Error != nil {
		return nil, errs.Internal(result.Error)
	}
	return row.ParentID, nil
}
