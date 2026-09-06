package release

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (s *TrackService) validateTrackCreditArtists(
	ctx context.Context,
	credits []*managev1.TrackCreditInput,
) error {
	for index, credit := range credits {
		if credit.ArtistId == nil || *credit.ArtistId == "" {
			continue
		}
		var artistExists bool
		if err := s.db.WithContext(ctx).
			Model(&model.Artist{}).
			Select("1").
			Where("id = ?", *credit.ArtistId).
			Find(&artistExists).Error; err != nil {
			return errs.Internal(err)
		}
		if !artistExists {
			field := fmt.Sprintf("credit[%d].artist_id", index)
			return errs.InvalidArgument(field, fmt.Sprintf("not found: %s", *credit.ArtistId))
		}
	}
	return nil
}

func (s *TrackService) replaceTrackCredits(
	ctx context.Context,
	trackID string,
	inputs []*managev1.TrackCreditInput,
) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var track model.Track
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&track, "id = ?", trackID).Error; err != nil {
			return err
		}
		if err := requireActiveTrackAction(ctx, tx, s.spiceDB, trackID, trackActionEdit); err != nil {
			return err
		}
		var existing []model.TrackCredit
		if err := tx.Where("track_id = ?", trackID).Order("sort_order ASC, id ASC").Find(&existing).Error; err != nil {
			return err
		}
		existingByID := map[string]model.TrackCredit{}
		for _, credit := range existing {
			existingByID[credit.ID] = credit
		}
		seen := map[string]bool{}
		if err := authorizationtarget.LockReferences(ctx, tx, trackCreditMemberReferences(inputs)); err != nil {
			return err
		}
		for order, input := range inputs {
			if input == nil {
				return errs.InvalidArgument("credits", "must not contain null")
			}
			id := input.GetId()
			if id != "" && !isValidUUID(id) {
				return errs.InvalidArgument("credit.id", "must be a UUID")
			}
			if id != "" && seen[id] {
				return errs.InvalidArgument("credit.id", "must be unique")
			}
			seen[id] = true
			input.SortOrder = int32(order)
			if current, ok := existingByID[id]; ok {
				next, err := newTrackCredit(trackID, input)
				if err != nil {
					return err
				}
				next.ID = current.ID
				if sameTrackCredit(current, next) {
					continue
				}
				if err := tx.Model(&model.TrackCredit{}).Where("id = ?", current.ID).Updates(&next).Error; err != nil {
					return err
				}
				if err := s.appendReleaseTrackAudit(ctx, tx, track.ReleaseID, current.ID, sharedtelemetry.AuditItemOperationUpdated); err != nil {
					return err
				}
				continue
			}
			if id != "" {
				return errs.InvalidArgument("credit.id", "does not belong to track")
			}
			credit, err := newTrackCredit(trackID, input)
			if err != nil {
				return err
			}
			if err := tx.Clauses(clause.Returning{}).Create(&credit).Error; err != nil {
				return err
			}
			if err := s.appendReleaseTrackAudit(ctx, tx, track.ReleaseID, credit.ID, sharedtelemetry.AuditItemOperationCreated); err != nil {
				return err
			}
		}
		for _, credit := range existing {
			if !seen[credit.ID] {
				if err := tx.Delete(&model.TrackCredit{}, "id = ?", credit.ID).Error; err != nil {
					return err
				}
				if err := s.appendReleaseTrackAudit(ctx, tx, track.ReleaseID, credit.ID, sharedtelemetry.AuditItemOperationDeleted); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func sameTrackCredit(left, right model.TrackCredit) bool {
	return left.CreditRole == right.CreditRole && left.SortOrder == right.SortOrder && sameOptionalString(left.ArtistID, right.ArtistID) && sameOptionalString(left.MemberID, right.MemberID) && sameOptionalString(left.CreditedName, right.CreditedName)
}

func trackCreditMemberReferences(inputs []*managev1.TrackCreditInput) []authorizationtarget.Reference {
	references := make([]authorizationtarget.Reference, 0, len(inputs))
	for index, input := range inputs {
		if input.MemberId == nil || *input.MemberId == "" {
			continue
		}
		references = append(references, authorizationtarget.Reference{
			MemberID: *input.MemberId,
			Field:    fmt.Sprintf("credit[%d].member_id", index),
		})
	}
	return references
}

func newTrackCredit(trackID string, input *managev1.TrackCreditInput) (model.TrackCredit, error) {
	if !trackCreditHasAttribution(input) {
		return model.TrackCredit{}, fmt.Errorf("credit must have artist_id, user_id, or credited_name")
	}
	credit := model.TrackCredit{
		TrackID:    trackID,
		CreditRole: input.CreditRole,
		SortOrder:  int(input.SortOrder),
	}
	if input.ArtistId != nil && *input.ArtistId != "" {
		credit.ArtistID = input.ArtistId
	}
	if input.MemberId != nil && *input.MemberId != "" {
		credit.MemberID = input.MemberId
	}
	if input.CreditedName != nil && *input.CreditedName != "" {
		credit.CreditedName = input.CreditedName
	}
	return credit, nil
}

func trackCreditHasAttribution(input *managev1.TrackCreditInput) bool {
	return input.ArtistId != nil && *input.ArtistId != "" ||
		input.MemberId != nil && *input.MemberId != "" ||
		input.CreditedName != nil && *input.CreditedName != ""
}
