package release

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SetTrackCredits applies a Track credit set edit (Track editors) and
// returns the Track with its credits.
func (s *Service) SetTrackCredits(ctx context.Context, command SetTrackCreditsCommand) (TrackWithCredits, error) {
	if command.TrackID == "" {
		return TrackWithCredits{}, domainerr.Required("track_id")
	}
	var track Track
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		track, err = store.FindTrack(ctx, command.TrackID)
		return err
	}); err != nil {
		return TrackWithCredits{}, missingAs(err, ErrTrackNotFound, trackNotFound())
	}
	if err := s.validateTrackCreditArtists(ctx, command.Credits); err != nil {
		return TrackWithCredits{}, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[[]TrackCreditChange, Store]{
		Lock: func(ctx context.Context, store Store) error {
			_, err := store.LockTrack(ctx, command.TrackID)
			return err
		},
		Access: s.trackAccess(command.TrackID, trackActionEdit),
		Change: func(ctx context.Context, store Store) ([]TrackCreditChange, error) {
			if err := requireObservedRelationSnapshot(command.Observed); err != nil {
				return nil, err
			}
			existing, err := store.TrackCredits(ctx, command.TrackID)
			if err != nil {
				return nil, err
			}
			return mergeAndPersistObservedTrackCredits(ctx, store, track.ID, existing, command.Observed.Items, command.Credits)
		},
		Audit: func(changes []TrackCreditChange) mutation.Audit {
			if len(changes) == 0 {
				return mutation.Unchanged()
			}
			records := make([]mutation.Audit, 0, len(changes))
			for _, change := range changes {
				records = append(records, releaseTrackAudit(track.ReleaseID, change.CreditID, change.Kind.auditOperation()))
			}
			return mutation.Audits(records...)
		},
	}); err != nil {
		return TrackWithCredits{}, err
	}
	credits, err := s.getCreditsForTrack(ctx, command.TrackID)
	if err != nil {
		return TrackWithCredits{}, err
	}
	return TrackWithCredits{Track: track, Credits: credits}, nil
}

// auditOperation is the audit operation of a relation row change.
func (kind ChangeKind) auditOperation() sharedtelemetry.AuditItemOperation {
	switch kind {
	case ChangeCreated:
		return sharedtelemetry.AuditItemOperationCreated
	case ChangeDeleted:
		return sharedtelemetry.AuditItemOperationDeleted
	default:
		return sharedtelemetry.AuditItemOperationUpdated
	}
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
		artists, err := s.readArtistSummaries(ctx, []string{*credit.ArtistID})
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
	store Store,
	trackID string,
	current []TrackCredit,
	observed, desired []*CreditInput,
) ([]TrackCreditChange, error) {
	currentByID := make(map[string]TrackCredit, len(current))
	for _, row := range current {
		currentByID[row.ID] = row
	}
	if err := validateDesiredTrackCredits(trackID, currentByID, observed, desired); err != nil {
		return nil, err
	}
	if err := store.LockMemberReferences(ctx, trackCreditMemberReferences(desired)); err != nil {
		return nil, err
	}
	// Existing rows retain their order unless membership changed. New rows append in the
	// request's desired order; a stale full snapshot never moves rows added by another tab.
	next := mergeTrackCredits(trackID, current, observed, desired)
	return store.ReplaceTrackCredits(ctx, current, next)
}

// validateDesiredTrackCredits checks each desired credit and numbers it by
// its request position.
func validateDesiredTrackCredits(
	trackID string,
	currentByID map[string]TrackCredit,
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
func checkDesiredTrackCreditID(id string, seen map[string]bool, currentByID map[string]TrackCredit, observedIDs map[string]struct{}) error {
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

func trackCreditMemberReferences(inputs []*CreditInput) []MemberReference {
	references := make([]MemberReference, 0, len(inputs))
	for index, input := range inputs {
		if input.MemberID == nil || *input.MemberID == "" {
			continue
		}
		references = append(references, MemberReference{
			MemberID: *input.MemberID,
			Field:    fmt.Sprintf("credit[%d].member_id", index),
		})
	}
	return references
}

func newTrackCredit(trackID string, input *CreditInput) (TrackCredit, error) {
	if !trackCreditHasAttribution(input) {
		return TrackCredit{}, fmt.Errorf("credit must have artist_id, user_id, or credited_name")
	}
	credit := TrackCredit{
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
