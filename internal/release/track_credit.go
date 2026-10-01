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
		if credit == nil {
			return errs.InvalidArgument("credits", "must not contain null")
		}
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
	observed *managev1.TrackCreditsSnapshot,
) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var track model.Track
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&track, "id = ?", trackID).Error; err != nil {
			return err
		}
		if err := requireActiveTrackAction(ctx, tx, s.spiceDB, trackID, trackActionEdit); err != nil {
			return err
		}
		if err := requireObservedRelationSnapshot(observed); err != nil {
			return err
		}
		var existing []model.TrackCredit
		if err := tx.Where("track_id = ?", trackID).Order("sort_order ASC, id ASC").Find(&existing).Error; err != nil {
			return err
		}
		return s.mergeAndPersistObservedTrackCredits(ctx, tx, track, existing, observed.Credits, inputs)
	})
}

func (s *TrackService) mergeAndPersistObservedTrackCredits(
	ctx context.Context,
	tx *gorm.DB,
	track model.Track,
	current []model.TrackCredit,
	observed, desired []*managev1.TrackCreditInput,
) error {
	currentByID := make(map[string]model.TrackCredit, len(current))
	for _, row := range current {
		currentByID[row.ID] = row
	}

	seen := make(map[string]bool, len(desired))
	for order, input := range desired {
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
		if id != "" {
			seen[id] = true
			if _, belongsToTrack := currentByID[id]; !belongsToTrack {
				return errs.InvalidArgument("credit.id", "does not belong to track")
			}
		}
		input.SortOrder = int32(order)
		if _, err := newTrackCredit(track.ID, input); err != nil {
			return errs.InvalidArgument("credit", err.Error())
		}
	}
	if err := authorizationtarget.LockReferences(ctx, tx, trackCreditMemberReferences(desired)); err != nil {
		return err
	}

	// Existing rows retain their order unless membership changed. New rows append in the
	// request's desired order; a stale full snapshot never moves rows added by another tab.
	next := mergeTrackCredits(track.ID, current, observed, desired)

	nextByID := make(map[string]model.TrackCredit, len(next))
	for _, row := range next {
		if row.ID != "" {
			nextByID[row.ID] = row
		}
	}
	for _, row := range current {
		if _, keep := nextByID[row.ID]; !keep {
			if err := tx.Delete(&model.TrackCredit{}, "id = ?", row.ID).Error; err != nil {
				return err
			}
			if err := s.appendReleaseTrackAudit(ctx, tx, track.ReleaseID, row.ID, sharedtelemetry.AuditItemOperationDeleted); err != nil {
				return err
			}
		}
	}
	for _, row := range next {
		if previous, exists := currentByID[row.ID]; exists && row.ID != "" {
			if sameTrackCredit(previous, row) {
				continue
			}
			updates := trackCreditUpdateMap(previous, row)
			if err := tx.Model(&model.TrackCredit{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
				return err
			}
			if err := s.appendReleaseTrackAudit(ctx, tx, track.ReleaseID, row.ID, sharedtelemetry.AuditItemOperationUpdated); err != nil {
				return err
			}
			continue
		}
		if err := tx.Clauses(clause.Returning{}).Create(&row).Error; err != nil {
			return err
		}
		if err := s.appendReleaseTrackAudit(ctx, tx, track.ReleaseID, row.ID, sharedtelemetry.AuditItemOperationCreated); err != nil {
			return err
		}
	}
	return nil
}

func trackCreditUpdateMap(previous, next model.TrackCredit) map[string]any {
	updates := make(map[string]any)
	if !sameOptionalString(previous.ArtistID, next.ArtistID) {
		updates["artist_id"] = next.ArtistID
	}
	if !sameOptionalString(previous.MemberID, next.MemberID) {
		updates["member_id"] = next.MemberID
	}
	if !sameOptionalString(previous.CreditedName, next.CreditedName) {
		updates["credited_name"] = next.CreditedName
	}
	if !sameOptionalString(previous.CreditRole, next.CreditRole) {
		updates["credit_role"] = next.CreditRole
	}
	if previous.SortOrder != next.SortOrder {
		updates["sort_order"] = next.SortOrder
	}
	return updates
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
