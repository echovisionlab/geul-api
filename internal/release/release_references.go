package release

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// SetReleaseArtistsCommand applies the desired Artists against the observed
// ones.
type SetReleaseArtistsCommand struct {
	ReleaseID   string
	Artists     []*ReleaseArtistInput
	Observed    *Snapshot[*ReleaseArtistInput]
	OrderIntent *RelationOrderIntent
}

// SetReleaseLabelsCommand applies the desired Labels against the observed
// ones.
type SetReleaseLabelsCommand struct {
	ReleaseID   string
	Labels      []*ReleaseLabelInput
	Observed    *Snapshot[*ReleaseLabelInput]
	OrderIntent *RelationOrderIntent
}

// SetReleaseFormatsCommand applies the desired Formats against the observed
// ones.
type SetReleaseFormatsCommand struct {
	ReleaseID string
	Formats   []*ReleaseFormatInput
	Observed  *Snapshot[*ReleaseFormatInput]
}

// SetReleaseCreditsCommand applies the desired credits against the observed
// ones.
type SetReleaseCreditsCommand struct {
	ReleaseID   string
	Credits     []*CreditInput
	Observed    *Snapshot[*CreditInput]
	OrderIntent *RelationOrderIntent
}

// SetReleaseArtists applies an Artist set edit.
func (s *Service) SetReleaseArtists(ctx context.Context, command SetReleaseArtistsCommand) error {
	return s.setReleaseRelation(ctx, command.ReleaseID, "artists", func(_ context.Context, tx *gorm.DB) (bool, error) {
		if err := requireObservedRelationSnapshot(command.Observed); err != nil {
			return false, err
		}
		var existing []model.ReleaseArtist
		if err := tx.Where("release_id = ?", command.ReleaseID).Order("sort_order ASC, artist_id ASC").Find(&existing).Error; err != nil {
			return false, err
		}
		if err := requireNoNullEntries("artists", command.Artists); err != nil {
			return false, err
		}
		next := mergeReleaseArtists(existing, command.Observed.Items, command.Artists, command.OrderIntent)
		if sameReleaseArtistRows(existing, next) {
			return false, nil
		}
		return true, persistReleaseArtists(tx, command.ReleaseID, existing, next)
	})
}

// SetReleaseLabels applies a Label set edit.
func (s *Service) SetReleaseLabels(ctx context.Context, command SetReleaseLabelsCommand) error {
	return s.setReleaseRelation(ctx, command.ReleaseID, "labels", func(_ context.Context, tx *gorm.DB) (bool, error) {
		if err := requireObservedRelationSnapshot(command.Observed); err != nil {
			return false, err
		}
		var existing []model.ReleaseLabel
		if err := tx.Where("release_id = ?", command.ReleaseID).Order("sort_order ASC, label_id ASC").Find(&existing).Error; err != nil {
			return false, err
		}
		if err := requireNoNullEntries("labels", command.Labels); err != nil {
			return false, err
		}
		next := mergeReleaseLabels(existing, command.Observed.Items, command.Labels, command.OrderIntent)
		if sameReleaseLabelRows(existing, next) {
			return false, nil
		}
		return true, persistReleaseLabels(tx, command.ReleaseID, existing, next)
	})
}

// SetReleaseFormats applies a Format set edit.
func (s *Service) SetReleaseFormats(ctx context.Context, command SetReleaseFormatsCommand) error {
	return s.setReleaseRelation(ctx, command.ReleaseID, "formats", func(_ context.Context, tx *gorm.DB) (bool, error) {
		if err := requireObservedRelationSnapshot(command.Observed); err != nil {
			return false, err
		}
		var existing []model.ReleaseFormat
		if err := tx.Where("release_id = ?", command.ReleaseID).Order("format_id ASC").Find(&existing).Error; err != nil {
			return false, err
		}
		if err := requireNoNullEntries("formats", command.Formats); err != nil {
			return false, err
		}
		next := mergeReleaseFormats(existing, command.Observed.Items, command.Formats)
		if sameReleaseFormatRows(existing, next) {
			return false, nil
		}
		return true, persistReleaseFormats(tx, command.ReleaseID, existing, next)
	})
}

// SetReleaseCredits applies a credit set edit. A desired credit without an
// ID is new and gets one here.
func (s *Service) SetReleaseCredits(ctx context.Context, command SetReleaseCreditsCommand) error {
	return s.setReleaseRelation(ctx, command.ReleaseID, "credits", func(ctx context.Context, tx *gorm.DB) (bool, error) {
		if err := requireObservedRelationSnapshot(command.Observed); err != nil {
			return false, err
		}
		var existing []model.ReleaseCredit
		if err := tx.Where("release_id = ?", command.ReleaseID).Order("sort_order ASC, id ASC").Find(&existing).Error; err != nil {
			return false, err
		}
		if err := requireNoNullEntries("credits", command.Credits); err != nil {
			return false, err
		}
		for _, credit := range command.Credits {
			if credit.id() == "" {
				id := uuid.NewString()
				credit.ID = &id
			}
		}
		next := mergeReleaseCredits(existing, command.Observed.Items, command.Credits, command.OrderIntent)
		if sameReleaseCreditRows(existing, next) {
			return false, nil
		}
		if err := authorizationtarget.LockReferences(ctx, tx, releaseCreditMemberReferences(command.Credits)); err != nil {
			return false, err
		}
		return true, persistReleaseCredits(tx, command.ReleaseID, existing, next)
	})
}

// releaseCreditMemberReferences names each credited Member, which must stay
// a live Member while the credit is written.
func releaseCreditMemberReferences(credits []*CreditInput) []authorizationtarget.Reference {
	references := make([]authorizationtarget.Reference, 0, len(credits))
	for i, credit := range credits {
		if credit.MemberID == nil {
			continue
		}
		references = append(references, authorizationtarget.Reference{
			MemberID: *credit.MemberID,
			Field:    fmt.Sprintf("credit[%d].member_id", i),
		})
	}
	return references
}

// setReleaseRelation runs one Release relation edit. change reports whether
// the relation changed; relation names it in the audit record and in the
// content.updated path.
func (s *Service) setReleaseRelation(
	ctx context.Context,
	releaseID string,
	relation string,
	change func(context.Context, *gorm.DB) (bool, error),
) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[bool]{
		Lock:   lockReleaseRow(releaseID, nil),
		Access: s.releaseAccess(releaseID, releaseActionManage),
		Change: func(ctx context.Context, tx *gorm.DB) (bool, error) {
			changed, err := change(ctx, tx)
			if err != nil || !changed {
				return false, err
			}
			return true, touchRelease(tx, releaseID)
		},
		Audit: func(changed bool) mutation.Audit {
			if !changed {
				return mutation.Unchanged()
			}
			return releaseMetadataAudit(releaseID, []string{relation})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, changed bool) error {
			if !changed {
				return nil
			}
			return s.publishContentUpdated(ctx, tx, buildReleaseRelationMutationContentUpdatedEvent(
				releaseID, []string{"relations." + relation},
			))
		},
	})
	return missingAs(err, releaseNotFound(releaseID))
}

func requireObservedRelationSnapshot[T any](observed *Snapshot[T]) error {
	if observed == nil {
		return domainerr.InvalidField("observed", "is required")
	}
	return nil
}

func requireNoNullEntries[T any](field string, entries []*T) error {
	for _, entry := range entries {
		if entry == nil {
			return domainerr.InvalidField(field, "must not contain null")
		}
	}
	return nil
}
