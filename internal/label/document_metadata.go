package label

import (
	"context"
	"log/slog"
	"maps"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type labelDocumentMetadataPlan struct {
	slug           entityparticipant.NullableStringUpdate
	countryCode    entityparticipant.NullableStringUpdate
	website        entityparticipant.NullableStringUpdate
	parentLabelID  entityparticipant.NullableStringUpdate
	socialLinks    map[string]string
	setSocialLinks bool
}

func buildLabelDocumentMetadataPlan(input *intrav1.LabelDocumentMetadataUpdate) (labelDocumentMetadataPlan, error) {
	if input == nil {
		return labelDocumentMetadataPlan{}, errs.Required("update")
	}
	var plan labelDocumentMetadataPlan
	var err error
	if plan.slug, err = entityparticipant.ParseNullableStringUpdate("slug", input.Slug); err != nil {
		return labelDocumentMetadataPlan{}, err
	}
	if plan.countryCode, err = entityparticipant.ParseNullableStringUpdate("country_code", input.CountryCode); err != nil {
		return labelDocumentMetadataPlan{}, err
	}
	if plan.website, err = entityparticipant.ParseNullableStringUpdate("website", input.Website); err != nil {
		return labelDocumentMetadataPlan{}, err
	}
	if plan.parentLabelID, err = entityparticipant.ParseNullableStringUpdate("parent_label_id", input.ParentLabelId); err != nil {
		return labelDocumentMetadataPlan{}, err
	}
	if plan.slug.Value != nil {
		if err := pathslug.Validate(*plan.slug.Value); err != nil {
			return labelDocumentMetadataPlan{}, err
		}
	}
	if plan.parentLabelID.Value != nil {
		parsed, err := uuid.Parse(*plan.parentLabelID.Value)
		if err != nil {
			return labelDocumentMetadataPlan{}, errs.InvalidArgument("parent_label_id", "must be a valid Label ID")
		}
		canonical := parsed.String()
		plan.parentLabelID.Value = &canonical
	}
	if input.SocialLinks != nil {
		plan.socialLinks = maps.Clone(input.SocialLinks.Values)
		if plan.socialLinks == nil {
			plan.socialLinks = map[string]string{}
		}
		plan.setSocialLinks = true
	}
	if !plan.slug.Present && !plan.countryCode.Present && !plan.website.Present && !plan.parentLabelID.Present && !plan.setSocialLinks {
		return labelDocumentMetadataPlan{}, errs.InvalidArgument("update", "at least one metadata value is required")
	}
	return plan, nil
}

func (s *InternalLabelService) UpdateLabelDocumentMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateLabelDocumentMetadataRequest]) (_ *connect.Response[intrav1.UpdateLabelDocumentMetadataResponse], returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", req.Msg.GetLabelId()))

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
	parent, err := labelEntity.PlanParentUpdate(ctx, s.db, req.Msg.LabelId, plan.parentLabelID)
	if err != nil {
		return nil, err
	}
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
			parentChanged, err := labelEntity.CheckParentUpdate(ctx, tx, req.Msg.LabelId, parent, current.ParentLabelID, write)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
			fields := structured.Fields{}
			slugChanged := plan.slug.Assign(fields, "slug", current.Slug)
			changed := slugChanged
			changed = plan.countryCode.Assign(fields, "country_code", current.CountryCode) || changed
			changed = plan.website.Assign(fields, "website", current.Website) || changed
			changed = plan.parentLabelID.Assign(fields, "parent_label_id", current.ParentLabelID) || changed
			if plan.setSocialLinks && !maps.Equal(current.SocialLinks, plan.socialLinks) {
				fields["social_links"] = plan.socialLinks
				changed = true
			}
			if !changed {
				return contentblock.MetadataEffect{}, nil
			}
			if slugChanged && plan.slug.Value != nil {
				if err := ensureSlugAvailable(ctx, tx, &model.Label{}, "label", *plan.slug.Value, req.Msg.LabelId); err != nil {
					return contentblock.MetadataEffect{}, err
				}
				if err := requireLabelRouteAvailableWithDB(ctx, tx, plan.slug.Value); err != nil {
					return contentblock.MetadataEffect{}, err
				}
			}
			fields["updated_at"] = time.Now().UTC()
			if err := tx.WithContext(ctx).Model(&model.Label{}).Where("id = ?", req.Msg.LabelId).Updates(fields).Error; err != nil {
				return contentblock.MetadataEffect{}, err
			}
			if parentChanged {
				if err := labelEntity.WriteParentRelationships(req.Msg.LabelId, parent, current.ParentLabelID, write); err != nil {
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
	if err := entityparticipant.ExecuteMetadataMutation(ctx, s.db, s.spiceDB, parent, mutate); err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&intrav1.UpdateLabelDocumentMetadataResponse{DocumentRevision: advanced.DocumentRevision.String(), Changed: advanced.Changed, SourceChanged: advanced.TranslationSourceChanged}), nil
}
