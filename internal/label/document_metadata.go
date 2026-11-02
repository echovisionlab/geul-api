package label

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/google/uuid"
)

// DocumentMetadataUpdate is a collaboration room's Label metadata update.
// A nil mutation leaves its field alone; SocialLinksPresent replaces the
// social links with SocialLinks.
type DocumentMetadataUpdate struct {
	Slug               *entityparticipant.StringMutation
	CountryCode        *entityparticipant.StringMutation
	Website            *entityparticipant.StringMutation
	ParentLabelID      *entityparticipant.StringMutation
	SocialLinksPresent bool
	SocialLinks        map[string]string
}

func buildLabelDocumentMetadataPlan(input *DocumentMetadataUpdate) (entityparticipant.MetadataColumns, error) {
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
	if plan.Parent, err = entityparticipant.ParseNullableStringUpdate("parent_label_id", input.ParentLabelID); err != nil {
		return entityparticipant.MetadataColumns{}, err
	}
	if err := labelEntity.Complete(&plan, input.SocialLinksPresent, input.SocialLinks); err != nil {
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
	Update               *DocumentMetadataUpdate
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
	documentID, err := s.readLabelContentDocumentID(ctx, command.LabelID)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	var parent entityparticipant.ParentUpdate
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		parent, err = labelEntity.PlanParentUpdate(ctx, store, command.LabelID, plan.Parent)
		return err
	}); err != nil {
		return contentblock.AdvanceResult{}, err
	}
	fence := internalLabelCollaborationContentFence(command.LabelID, command.ContributorMemberIDs)
	return s.participants.SaveMetadata(ctx, entityparticipant.MetadataSave[Store]{
		EntityID: command.LabelID, Parent: parent, Access: contributorAccess(command.ContributorMemberIDs),
		Save: func(ctx context.Context, store Store, writesParent bool) (contentblock.AdvanceResult, *string, error) {
			var currentParent *string
			advanced, err := store.AdvanceContentRevision(
				ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision}, fence,
				func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
					changed, parentID, err := applyLabelDocumentMetadata(ctx, store, command.LabelID, plan, parent, writesParent)
					currentParent = parentID
					return contentblock.MetadataEffect{Changed: changed}, err
				},
			)
			if err != nil {
				return contentblock.AdvanceResult{}, currentParent, normalizeLabelContentBlockError(err)
			}
			return advanced, currentParent, nil
		},
	})
}

// applyLabelDocumentMetadata writes the changed metadata columns of the
// Label row under the content fence. It reports whether a column changed and
// the parent it read under the lock.
func applyLabelDocumentMetadata(ctx context.Context, store Store, labelID string, plan entityparticipant.MetadataColumns, parent entityparticipant.ParentUpdate, writesParent bool) (bool, *string, error) {
	current, err := store.DocumentMetadata(ctx, labelID)
	if err != nil {
		return false, nil, err
	}
	var change entityparticipant.MetadataChange
	changed, err := labelEntity.AssignColumns(ctx, store, labelID, plan, parent, writesParent, entityparticipant.MetadataRow{
		Slug: current.Slug, CountryCode: current.CountryCode, Website: current.Website,
		Parent: current.ParentLabelID, SocialLinks: current.SocialLinks,
	}, &change, func(ctx context.Context, slug string) error {
		taken, err := store.SlugTaken(ctx, slug, labelID)
		if err != nil {
			return err
		}
		if taken {
			return domainerr.Duplicate("label", "slug", slug)
		}
		return store.LockResourceRoute(ctx, slug)
	})
	if err != nil || !changed {
		return false, current.ParentLabelID, err
	}
	if err := store.UpdateDocumentMetadata(ctx, labelID, change, time.Now().UTC()); err != nil {
		return false, current.ParentLabelID, err
	}
	return true, current.ParentLabelID, nil
}
