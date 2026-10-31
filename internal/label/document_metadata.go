package label

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func buildLabelDocumentMetadataPlan(input *intrav1.LabelDocumentMetadataUpdate) (entityparticipant.MetadataColumns, error) {
	if input == nil {
		return entityparticipant.MetadataColumns{}, domainerr.Required("update")
	}
	var plan entityparticipant.MetadataColumns
	var err error
	if plan.Slug, err = entityparticipant.ParseNullableStringUpdate("slug", input.Slug); err != nil {
		return entityparticipant.MetadataColumns{}, err
	}
	if plan.CountryCode, err = entityparticipant.ParseNullableStringUpdate("country_code", input.CountryCode); err != nil {
		return entityparticipant.MetadataColumns{}, err
	}
	if plan.Website, err = entityparticipant.ParseNullableStringUpdate("website", input.Website); err != nil {
		return entityparticipant.MetadataColumns{}, err
	}
	if plan.Parent, err = entityparticipant.ParseNullableStringUpdate("parent_label_id", input.ParentLabelId); err != nil {
		return entityparticipant.MetadataColumns{}, err
	}
	if err := labelEntity.Complete(&plan, input.SocialLinks != nil, input.SocialLinks.GetValues()); err != nil {
		return entityparticipant.MetadataColumns{}, err
	}
	if !plan.Present() {
		return entityparticipant.MetadataColumns{}, domainerr.InvalidField("update", "at least one metadata value is required")
	}
	return plan, nil
}

// UpdateLabelDocumentMetadataCommand saves the Label's document metadata
// from its collaboration room.
type UpdateLabelDocumentMetadataCommand struct {
	LabelID              string
	ExpectedRevision     string
	Update               *intrav1.LabelDocumentMetadataUpdate
	ContributorMemberIDs []string
}

// UpdateLabelDocumentMetadata saves the slug, country, website, social
// links and parent Label, moving the SpiceDB parent with it.
func (s *Service) UpdateLabelDocumentMetadata(ctx context.Context, command UpdateLabelDocumentMetadataCommand) (_ contentblock.AdvanceResult, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", command.LabelID))

	plan, err := buildLabelDocumentMetadataPlan(command.Update)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	expectedRevision, err := uuid.Parse(command.ExpectedRevision)
	if err != nil {
		return contentblock.AdvanceResult{}, domainerr.InvalidField("expected_revision", "must be a UUID")
	}
	documentID, err := loadLabelContentDocumentID(ctx, s.db, command.LabelID)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	parent, err := labelEntity.PlanParentUpdate(ctx, s.db, command.LabelID, plan.Parent)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	return s.participants.SaveMetadata(ctx, entityparticipant.MetadataSave{
		EntityID: command.LabelID, Documents: s.contentBlocks, DocumentID: documentID, ExpectedRevision: expectedRevision,
		Parent: parent, Access: s.contributorAccess(command.ContributorMemberIDs),
		Fence: internalLabelCollaborationContentFence(s.checkpoints, command.LabelID, command.ContributorMemberIDs),
		Apply: func(ctx context.Context, tx *gorm.DB, writesParent bool) (entityparticipant.MetadataApplied, error) {
			return applyLabelDocumentMetadata(ctx, tx, command.LabelID, plan, parent, writesParent)
		},
		NormalizeError: normalizeLabelContentBlockError,
	})
}

// applyLabelDocumentMetadata writes the changed metadata columns of the
// Label row under the content fence.
func applyLabelDocumentMetadata(ctx context.Context, tx *gorm.DB, labelID string, plan entityparticipant.MetadataColumns, parent entityparticipant.ParentUpdate, writesParent bool) (entityparticipant.MetadataApplied, error) {
	var current model.Label
	if err := tx.WithContext(ctx).Select("id", "slug", "country_code", "website", "social_links", "parent_label_id").Where("id = ?", labelID).Take(&current).Error; err != nil {
		return entityparticipant.MetadataApplied{}, err
	}
	applied := entityparticipant.MetadataApplied{CurrentParent: current.ParentLabelID}
	fields := structured.Fields{}
	changed, err := labelEntity.AssignColumns(ctx, tx, labelID, plan, parent, writesParent, entityparticipant.MetadataRow{
		Slug: current.Slug, CountryCode: current.CountryCode, Website: current.Website,
		Parent: current.ParentLabelID, SocialLinks: current.SocialLinks,
	}, fields, func(ctx context.Context, tx *gorm.DB, slug string) error {
		if err := ensureSlugAvailable(ctx, tx, &model.Label{}, "label", slug, labelID); err != nil {
			return err
		}
		return requireLabelRouteAvailableWithDB(ctx, tx, &slug)
	})
	if err != nil || !changed {
		return applied, err
	}
	fields["updated_at"] = time.Now().UTC()
	if err := tx.WithContext(ctx).Model(&model.Label{}).Where("id = ?", labelID).Updates(fields).Error; err != nil {
		return applied, err
	}
	applied.Changed = true
	return applied, nil
}
