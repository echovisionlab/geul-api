package label

import (
	"context"
	"errors"
	"maps"
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

func parseNullableDocumentStringUpdate(field string, input *intrav1.NullableStringMutation) (nullableDocumentStringUpdate, error) {
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

func assignNullableDocumentString(fields structured.Fields, column string, current *string, update nullableDocumentStringUpdate) bool {
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

type labelDocumentMetadataPlan struct {
	slug           nullableDocumentStringUpdate
	countryCode    nullableDocumentStringUpdate
	website        nullableDocumentStringUpdate
	parentLabelID  nullableDocumentStringUpdate
	socialLinks    map[string]string
	setSocialLinks bool
}

func buildLabelDocumentMetadataPlan(input *intrav1.LabelDocumentMetadataUpdate) (labelDocumentMetadataPlan, error) {
	if input == nil {
		return labelDocumentMetadataPlan{}, errs.Required("update")
	}
	var plan labelDocumentMetadataPlan
	var err error
	if plan.slug, err = parseNullableDocumentStringUpdate("slug", input.Slug); err != nil {
		return labelDocumentMetadataPlan{}, err
	}
	if plan.countryCode, err = parseNullableDocumentStringUpdate("country_code", input.CountryCode); err != nil {
		return labelDocumentMetadataPlan{}, err
	}
	if plan.website, err = parseNullableDocumentStringUpdate("website", input.Website); err != nil {
		return labelDocumentMetadataPlan{}, err
	}
	if plan.parentLabelID, err = parseNullableDocumentStringUpdate("parent_label_id", input.ParentLabelId); err != nil {
		return labelDocumentMetadataPlan{}, err
	}
	if plan.slug.value != nil {
		if err := validateSlugWithoutSlash(*plan.slug.value); err != nil {
			return labelDocumentMetadataPlan{}, err
		}
	}
	if plan.parentLabelID.value != nil {
		parsed, err := uuid.Parse(*plan.parentLabelID.value)
		if err != nil {
			return labelDocumentMetadataPlan{}, errs.InvalidArgument("parent_label_id", "must be a valid Label ID")
		}
		canonical := parsed.String()
		plan.parentLabelID.value = &canonical
	}
	if input.SocialLinks != nil {
		plan.socialLinks = maps.Clone(input.SocialLinks.Values)
		if plan.socialLinks == nil {
			plan.socialLinks = map[string]string{}
		}
		plan.setSocialLinks = true
	}
	if !plan.slug.present && !plan.countryCode.present && !plan.website.present && !plan.parentLabelID.present && !plan.setSocialLinks {
		return labelDocumentMetadataPlan{}, errs.InvalidArgument("update", "at least one metadata value is required")
	}
	return plan, nil
}

func (s *InternalLabelService) UpdateLabelDocumentMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateLabelDocumentMetadataRequest]) (*connect.Response[intrav1.UpdateLabelDocumentMetadataResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Label content Block store is not configured")
	}
	plan, err := buildLabelDocumentMetadataPlan(req.Msg.Update)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := uuid.Parse(req.Msg.ExpectedRevision)
	if err != nil {
		return nil, errs.InvalidArgument("expected_revision", "must be a UUID")
	}
	documentID, err := loadLabelContentDocumentID(ctx, s.db, req.Msg.LabelId)
	if err != nil {
		return nil, err
	}
	previousParent, err := loadLabelDocumentParent(ctx, s.db, req.Msg.LabelId)
	if err != nil {
		return nil, err
	}
	requiresParentWrite := plan.parentLabelID.present && !sameOptionalString(previousParent, plan.parentLabelID.value)
	var advanced contentblock.AdvanceResult
	mutate := func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := s.translation.RequireDocumentContributors(ctx, tx, req.Msg.ContributorMemberIds); err != nil {
			return err
		}
		var advanceErr error
		advanced, advanceErr = s.contentBlocks.AdvanceRevision(ctx, tx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision}, internalLabelCollaborationContentFence(s.checkpoints, req.Msg.LabelId, req.Msg.ContributorMemberIds), func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			var current model.Label
			if err := tx.WithContext(ctx).Select("id", "slug", "country_code", "website", "social_links", "parent_label_id").Where("id = ?", req.Msg.LabelId).Take(&current).Error; err != nil {
				return contentblock.MetadataEffect{}, err
			}
			parentChanged := nullableDocumentStringChanged(current.ParentLabelID, plan.parentLabelID)
			if plan.parentLabelID.present && (!sameOptionalString(current.ParentLabelID, previousParent) || parentChanged != requiresParentWrite) {
				return contentblock.MetadataEffect{}, errs.FailedPrecondition("Label parent changed concurrently; reload before saving")
			}
			if parentChanged {
				if write == nil {
					return contentblock.MetadataEffect{}, errs.FailedPrecondition("Label parent changed concurrently; reload before saving")
				}
				parentID := ""
				if plan.parentLabelID.value != nil {
					parentID = *plan.parentLabelID.value
				}
				if err := validateLabelParentTransition(ctx, tx, req.Msg.LabelId, parentID); err != nil {
					return contentblock.MetadataEffect{}, err
				}
			}
			fields := structured.Fields{}
			slugChanged := assignNullableDocumentString(fields, "slug", current.Slug, plan.slug)
			changed := slugChanged
			changed = assignNullableDocumentString(fields, "country_code", current.CountryCode, plan.countryCode) || changed
			changed = assignNullableDocumentString(fields, "website", current.Website, plan.website) || changed
			changed = assignNullableDocumentString(fields, "parent_label_id", current.ParentLabelID, plan.parentLabelID) || changed
			if plan.setSocialLinks && !maps.Equal(current.SocialLinks, plan.socialLinks) {
				fields["social_links"] = plan.socialLinks
				changed = true
			}
			if !changed {
				return contentblock.MetadataEffect{}, nil
			}
			if slugChanged && plan.slug.value != nil {
				if err := ensureSlugAvailable(ctx, tx, &model.Label{}, "label", *plan.slug.value, req.Msg.LabelId); err != nil {
					return contentblock.MetadataEffect{}, err
				}
				if err := requireLabelRouteAvailableWithDB(ctx, tx, plan.slug.value); err != nil {
					return contentblock.MetadataEffect{}, err
				}
			}
			fields["updated_at"] = time.Now().UTC()
			if err := tx.WithContext(ctx).Model(&model.Label{}).Where("id = ?", req.Msg.LabelId).Updates(fields).Error; err != nil {
				return contentblock.MetadataEffect{}, err
			}
			if parentChanged {
				apply, compensate, err := replaceLabelParentRelationshipMutations(req.Msg.LabelId, current.ParentLabelID, plan.parentLabelID.value)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				if err := write(apply, compensate); err != nil {
					return contentblock.MetadataEffect{}, err
				}
			}
			return contentblock.MetadataEffect{Changed: true}, nil
		})
		if advanceErr != nil {
			return normalizeLabelContentBlockError(advanceErr)
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
	return connect.NewResponse(&intrav1.UpdateLabelDocumentMetadataResponse{DocumentRevision: advanced.DocumentRevision.String(), Changed: advanced.Changed, SourceChanged: advanced.TranslationSourceChanged}), nil
}

func loadLabelDocumentParent(ctx context.Context, db *gorm.DB, labelID string) (*string, error) {
	var row struct {
		ParentID *string `gorm:"column:parent_id"`
	}
	result := db.WithContext(ctx).Table("label").Select("parent_label_id AS parent_id").Where("id = ?", labelID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, errs.NotFound("label", labelID)
	}
	if result.Error != nil {
		return nil, errs.Internal(result.Error)
	}
	return row.ParentID, nil
}
