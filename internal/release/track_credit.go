package release

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// trackCreditAudit is one credit a Track credit edit added, changed or
// removed.
type trackCreditAudit struct {
	creditID string
	op       sharedtelemetry.AuditItemOperation
}

// SetTrackCredits applies a Track credit set edit (Track editors) and
// returns the Track with its credits.
func (s *Service) SetTrackCredits(ctx context.Context, command SetTrackCreditsCommand) (*managev1.TrackWithCredits, error) {
	if command.TrackID == "" {
		return nil, domainerr.Required("track_id")
	}
	var track model.Track
	if err := s.db.WithContext(ctx).Where("id = ?", command.TrackID).First(&track).Error; err != nil {
		return nil, missingAs(err, trackNotFound())
	}
	if err := s.validateTrackCreditArtists(ctx, command.Credits); err != nil {
		return nil, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[[]trackCreditAudit]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&model.Track{}, "id = ?", command.TrackID).Error
		},
		Access: s.trackAccess(command.TrackID, trackActionEdit),
		Change: func(ctx context.Context, tx *gorm.DB) ([]trackCreditAudit, error) {
			if err := requireObservedRelationSnapshot(command.Observed); err != nil {
				return nil, err
			}
			var existing []model.TrackCredit
			if err := tx.Where("track_id = ?", command.TrackID).Order("sort_order ASC, id ASC").Find(&existing).Error; err != nil {
				return nil, err
			}
			return mergeAndPersistObservedTrackCredits(ctx, tx, track.ID, existing, command.Observed.Items, command.Credits)
		},
		Audit: func(audits []trackCreditAudit) mutation.Audit {
			if len(audits) == 0 {
				return mutation.Unchanged()
			}
			records := make([]mutation.Audit, 0, len(audits))
			for _, audit := range audits {
				records = append(records, releaseTrackAudit(track.ReleaseID, audit.creditID, audit.op))
			}
			return mutation.All(records...)
		},
	}); err != nil {
		return nil, err
	}
	credits, err := s.getCreditsForTrack(ctx, command.TrackID)
	if err != nil {
		return nil, err
	}
	return &managev1.TrackWithCredits{Track: trackMessage(ctx, &track), Credits: credits}, nil
}

// validateTrackCreditArtists rejects a null credit or a credit of an Artist
// that does not exist, at the first one in request order.
func (s *Service) validateTrackCreditArtists(ctx context.Context, credits []*CreditInput) error {
	for index, credit := range credits {
		if credit == nil {
			return domainerr.InvalidField("credits", "must not contain null")
		}
		if credit.ArtistID == nil || *credit.ArtistID == "" {
			continue
		}
		artists, err := s.artists.LoadArtistSummaries(ctx, []string{*credit.ArtistID})
		if err != nil {
			return err
		}
		if _, exists := artists[*credit.ArtistID]; !exists {
			field := fmt.Sprintf("credit[%d].artist_id", index)
			return domainerr.InvalidField(field, fmt.Sprintf("not found: %s", *credit.ArtistID))
		}
	}
	return nil
}

// mergeAndPersistObservedTrackCredits validates the desired credits, merges
// them with the current ones against the observation and stores the result,
// returning the audit of each credit it removed, changed or added.
func mergeAndPersistObservedTrackCredits(
	ctx context.Context,
	tx *gorm.DB,
	trackID string,
	current []model.TrackCredit,
	observed, desired []*CreditInput,
) ([]trackCreditAudit, error) {
	currentByID := make(map[string]model.TrackCredit, len(current))
	for _, row := range current {
		currentByID[row.ID] = row
	}
	if err := validateDesiredTrackCredits(trackID, currentByID, observed, desired); err != nil {
		return nil, err
	}
	if err := authorizationtarget.LockReferences(ctx, tx, trackCreditMemberReferences(desired)); err != nil {
		return nil, err
	}
	// Existing rows retain their order unless membership changed. New rows append in the
	// request's desired order; a stale full snapshot never moves rows added by another tab.
	next := mergeTrackCredits(trackID, current, observed, desired)
	return persistTrackCredits(tx, current, currentByID, next)
}

// validateDesiredTrackCredits checks each desired credit and numbers it by
// its request position.
func validateDesiredTrackCredits(
	trackID string,
	currentByID map[string]model.TrackCredit,
	observed, desired []*CreditInput,
) error {
	observedIDs := make(map[string]struct{}, len(observed))
	for _, input := range observed {
		if id := input.id(); id != "" {
			observedIDs[id] = struct{}{}
		}
	}
	seen := make(map[string]bool, len(desired))
	for order, input := range desired {
		if input == nil {
			return domainerr.InvalidField("credits", "must not contain null")
		}
		if err := checkDesiredTrackCreditID(input.id(), seen, currentByID, observedIDs); err != nil {
			return err
		}
		input.SortOrder = int32(order)
		if _, err := newTrackCredit(trackID, input); err != nil {
			return domainerr.InvalidField("credit", err.Error())
		}
	}
	return nil
}

// checkDesiredTrackCreditID accepts a new credit (empty ID) or the unique ID
// of a credit the Track has or the client observed.
func checkDesiredTrackCreditID(id string, seen map[string]bool, currentByID map[string]model.TrackCredit, observedIDs map[string]struct{}) error {
	if id == "" {
		return nil
	}
	if !isValidUUID(id) {
		return domainerr.InvalidField("credit.id", "must be a UUID")
	}
	if seen[id] {
		return domainerr.InvalidField("credit.id", "must be unique")
	}
	seen[id] = true
	if _, belongsToTrack := currentByID[id]; belongsToTrack {
		return nil
	}
	if _, wasObserved := observedIDs[id]; !wasObserved {
		return domainerr.InvalidField("credit.id", "does not belong to track")
	}
	return nil
}

// persistTrackCredits stores next in place of current: removed credits
// first, then changed and added credits in their new order.
func persistTrackCredits(
	tx *gorm.DB,
	current []model.TrackCredit,
	currentByID map[string]model.TrackCredit,
	next []model.TrackCredit,
) ([]trackCreditAudit, error) {
	var audits []trackCreditAudit
	nextByID := make(map[string]model.TrackCredit, len(next))
	for _, row := range next {
		if row.ID != "" {
			nextByID[row.ID] = row
		}
	}
	for _, row := range current {
		if _, keep := nextByID[row.ID]; keep {
			continue
		}
		if err := tx.Delete(&model.TrackCredit{}, "id = ?", row.ID).Error; err != nil {
			return nil, err
		}
		audits = append(audits, trackCreditAudit{row.ID, sharedtelemetry.AuditItemOperationDeleted})
	}
	for _, row := range next {
		if previous, exists := currentByID[row.ID]; exists && row.ID != "" {
			if sameTrackCredit(previous, row) {
				continue
			}
			updates := creditUpdateMap(
				creditValues{previous.ArtistID, previous.MemberID, previous.CreditedName, previous.CreditRole, previous.SortOrder},
				creditValues{row.ArtistID, row.MemberID, row.CreditedName, row.CreditRole, row.SortOrder},
			)
			if err := tx.Model(&model.TrackCredit{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
				return nil, err
			}
			audits = append(audits, trackCreditAudit{row.ID, sharedtelemetry.AuditItemOperationUpdated})
			continue
		}
		if err := tx.Clauses(clause.Returning{}).Create(&row).Error; err != nil {
			return nil, err
		}
		audits = append(audits, trackCreditAudit{row.ID, sharedtelemetry.AuditItemOperationCreated})
	}
	return audits, nil
}

func sameTrackCredit(left, right model.TrackCredit) bool {
	return sameOptionalString(left.CreditRole, right.CreditRole) && left.SortOrder == right.SortOrder && sameOptionalString(left.ArtistID, right.ArtistID) && sameOptionalString(left.MemberID, right.MemberID) && sameOptionalString(left.CreditedName, right.CreditedName)
}

func trackCreditMemberReferences(inputs []*CreditInput) []authorizationtarget.Reference {
	references := make([]authorizationtarget.Reference, 0, len(inputs))
	for index, input := range inputs {
		if input.MemberID == nil || *input.MemberID == "" {
			continue
		}
		references = append(references, authorizationtarget.Reference{
			MemberID: *input.MemberID,
			Field:    fmt.Sprintf("credit[%d].member_id", index),
		})
	}
	return references
}

func newTrackCredit(trackID string, input *CreditInput) (model.TrackCredit, error) {
	if !trackCreditHasAttribution(input) {
		return model.TrackCredit{}, fmt.Errorf("credit must have artist_id, user_id, or credited_name")
	}
	credit := model.TrackCredit{
		TrackID:    trackID,
		CreditRole: input.CreditRole,
		SortOrder:  int(input.SortOrder),
	}
	if input.ArtistID != nil && *input.ArtistID != "" {
		credit.ArtistID = input.ArtistID
	}
	if input.MemberID != nil && *input.MemberID != "" {
		credit.MemberID = input.MemberID
	}
	if input.CreditedName != nil && *input.CreditedName != "" {
		credit.CreditedName = input.CreditedName
	}
	return credit, nil
}

func trackCreditHasAttribution(input *CreditInput) bool {
	return input.ArtistID != nil && *input.ArtistID != "" ||
		input.MemberID != nil && *input.MemberID != "" ||
		input.CreditedName != nil && *input.CreditedName != ""
}
