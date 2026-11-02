package release

import (
	"context"
	"time"

	"errors"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ReleaseDateChange is what an update does to the release date.
type ReleaseDateChange uint8

const (
	ReleaseDateUnchanged ReleaseDateChange = iota
	// ReleaseDateSet sets the date to UpdateReleaseCommand.ReleaseDate; a
	// missing date is rejected.
	ReleaseDateSet
	ReleaseDateCleared
)

// UpdateReleaseCommand updates Release settings. Nil fields are left alone.
type UpdateReleaseCommand struct {
	ID                string
	Slug              *string
	Type              *Type
	ReleaseDateChange ReleaseDateChange
	ReleaseDate       *time.Time
	SpotifyURL        *string
	AppleMusicURL     *string
	BandcampURL       *string
	YoutubeMusicURL   *string
	CatalogNumber     *string
}

// ReleaseUpdate is the outcome of UpdateRelease.
type ReleaseUpdate struct {
	ID          string
	Changed     bool
	UpdatedAt   time.Time
	ReleaseDate *time.Time
}

// UpdateRelease updates Release settings (Release editors).
func (s *Service) UpdateRelease(ctx context.Context, command UpdateReleaseCommand) (ReleaseUpdate, error) {
	update, err := s.buildReleaseUpdates(ctx, command)
	if err != nil {
		return ReleaseUpdate{}, err
	}
	var locked Release
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[releaseMetadataChange, Store]{
		Lock:   lockReleaseRow(command.ID, &locked),
		Access: s.releaseAccess(command.ID, releaseActionEdit),
		Change: func(ctx context.Context, store Store) (releaseMetadataChange, error) {
			return updateRelease(ctx, store, &locked, update)
		},
		Audit: func(change releaseMetadataChange) mutation.Audit {
			if len(change.fields) == 0 {
				return mutation.Unchanged()
			}
			return releaseMetadataAudit(change.release.ID, change.fields)
		},
		Outbox: func(ctx context.Context, store Store, change releaseMetadataChange) error {
			if len(change.fields) == 0 {
				return nil
			}
			return publishContentUpdated(ctx, store, buildReleaseContentUpdatedEvent(command.ID, releaseUpdatedFields(command)))
		},
	})
	if err != nil {
		if errors.Is(err, ErrSlugTaken) {
			return ReleaseUpdate{}, slugTaken(err)
		}
		return ReleaseUpdate{}, releaseMissingAs(err, releaseNotFound(command.ID))
	}
	return ReleaseUpdate{
		ID: result.release.ID, Changed: len(result.fields) != 0,
		UpdatedAt: result.release.UpdatedAt, ReleaseDate: result.release.ReleaseDate,
	}, nil
}

// releaseUpdate is a validated Release metadata update.
type releaseUpdate = Settings

// releaseMetadataChange is the Release after an update and the audit fields
// that changed.
type releaseMetadataChange struct {
	release *Release
	fields  []string
}

func (s *Service) buildReleaseUpdates(ctx context.Context, command UpdateReleaseCommand) (releaseUpdate, error) {
	update := releaseUpdate{
		Type:            command.Type,
		SpotifyURL:      command.SpotifyURL,
		AppleMusicURL:   command.AppleMusicURL,
		BandcampURL:     command.BandcampURL,
		YoutubeMusicURL: command.YoutubeMusicURL,
		CatalogNumber:   command.CatalogNumber,
	}
	if command.Slug != nil {
		if err := pathslug.Validate(*command.Slug); err != nil {
			return releaseUpdate{}, err
		}
		if err := s.checkResourceRoute(ctx, *command.Slug); err != nil {
			return releaseUpdate{}, err
		}
		update.Slug = command.Slug
	}
	switch command.ReleaseDateChange {
	case ReleaseDateSet:
		if command.ReleaseDate == nil {
			return releaseUpdate{}, domainerr.InvalidField("release_date", "must be a valid timestamp")
		}
		update.ReleaseDateSet, update.ReleaseDate = true, command.ReleaseDate
	case ReleaseDateCleared:
		update.ReleaseDateSet = true
	case ReleaseDateUnchanged:
	}
	return update, nil
}

// changedFields names the audit fields whose requested value differs from
// release.
func changedFields(u releaseUpdate, release *Release) []string {
	fields := make([]string, 0, 4)
	if u.Slug != nil && !sameOptionalString(release.Slug, u.Slug) {
		fields = append(fields, "slug")
	}
	if u.Type != nil && release.Type != *u.Type {
		fields = append(fields, "type")
	}
	if u.ReleaseDateSet && !sameOptionalTime(release.ReleaseDate, u.ReleaseDate) {
		fields = append(fields, "date")
	}
	for _, link := range []struct{ current, next *string }{
		{release.SpotifyURL, u.SpotifyURL},
		{release.AppleMusicURL, u.AppleMusicURL},
		{release.BandcampURL, u.BandcampURL},
		{release.YoutubeMusicURL, u.YoutubeMusicURL},
		{release.CatalogNumber, u.CatalogNumber},
	} {
		if link.next != nil && !sameOptionalString(link.current, link.next) {
			fields = append(fields, "links")
			break
		}
	}
	return fields
}

func sameOptionalTime(left, right *time.Time) bool {
	return left == nil && right == nil || left != nil && right != nil && left.Equal(*right)
}

func updateRelease(
	ctx context.Context,
	store Store,
	release *Release,
	update releaseUpdate,
) (releaseMetadataChange, error) {
	if update.Slug != nil {
		if err := store.LockResourceRoute(ctx, *update.Slug); err != nil {
			return releaseMetadataChange{}, err
		}
	}
	changed := changedFields(update, release)
	if len(changed) == 0 {
		return releaseMetadataChange{release: release}, nil
	}
	mutationNow := dbtime.Now()
	if err := store.UpdateReleaseSettings(ctx, release.ID, update, mutationNow); err != nil {
		return releaseMetadataChange{}, err
	}
	release.UpdatedAt = mutationNow
	if update.ReleaseDateSet {
		release.ReleaseDate = update.ReleaseDate
	}
	return releaseMetadataChange{release: release, fields: changed}, nil
}

// releaseMetadataAudit records which Release settings or relations changed.
func releaseMetadataAudit(releaseID string, fields []string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseMetadataAuditRecord(m, releaseID, fields)
	})
}
