package release

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
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
	Type              *model.ReleaseType
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
	var locked model.Release
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[releaseMetadataChange]{
		Lock:   lockReleaseRow(command.ID, &locked),
		Access: s.releaseAccess(command.ID, releaseActionEdit),
		Change: func(ctx context.Context, tx *gorm.DB) (releaseMetadataChange, error) {
			return updateReleaseWithDB(ctx, tx, &locked, update)
		},
		Audit: func(change releaseMetadataChange) mutation.Audit {
			if len(change.fields) == 0 {
				return mutation.Unchanged()
			}
			return releaseMetadataAudit(change.release.ID, change.fields)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, change releaseMetadataChange) error {
			if len(change.fields) == 0 {
				return nil
			}
			return s.publishContentUpdated(ctx, tx, buildReleaseContentUpdatedEvent(command.ID, releaseUpdatedFields(command)))
		},
	})
	if err != nil {
		if dberrors.IsUniqueViolation(err) {
			return ReleaseUpdate{}, slugTaken(err)
		}
		return ReleaseUpdate{}, missingAs(err, releaseNotFound(command.ID))
	}
	return ReleaseUpdate{
		ID: result.release.ID, Changed: len(result.fields) != 0,
		UpdatedAt: result.release.UpdatedAt, ReleaseDate: result.release.ReleaseDate,
	}, nil
}

// releaseUpdate is a validated Release metadata update. Nil fields are left
// alone; releaseDateSet with a nil releaseDate clears the date.
type releaseUpdate struct {
	slug            *string
	releaseType     *model.ReleaseType
	releaseDateSet  bool
	releaseDate     *time.Time
	spotifyURL      *string
	appleMusicURL   *string
	bandcampURL     *string
	youtubeMusicURL *string
	catalogNumber   *string
}

// releaseMetadataChange is the Release after an update and the audit fields
// that changed.
type releaseMetadataChange struct {
	release *model.Release
	fields  []string
}

func (s *Service) buildReleaseUpdates(ctx context.Context, command UpdateReleaseCommand) (releaseUpdate, error) {
	update := releaseUpdate{
		releaseType:     command.Type,
		spotifyURL:      command.SpotifyURL,
		appleMusicURL:   command.AppleMusicURL,
		bandcampURL:     command.BandcampURL,
		youtubeMusicURL: command.YoutubeMusicURL,
		catalogNumber:   command.CatalogNumber,
	}
	if command.Slug != nil {
		if err := pathslug.Validate(*command.Slug); err != nil {
			return releaseUpdate{}, err
		}
		if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "release", "releases", *command.Slug); err != nil {
			return releaseUpdate{}, err
		}
		update.slug = command.Slug
	}
	switch command.ReleaseDateChange {
	case ReleaseDateSet:
		if command.ReleaseDate == nil {
			return releaseUpdate{}, domainerr.InvalidField("release_date", "must be a valid timestamp")
		}
		update.releaseDateSet, update.releaseDate = true, command.ReleaseDate
	case ReleaseDateCleared:
		update.releaseDateSet = true
	case ReleaseDateUnchanged:
	}
	return update, nil
}

// columns is the Release row update for the fields the request sets.
func (u releaseUpdate) columns(updatedAt time.Time) structured.Fields {
	columns := structured.Fields{"updated_at": updatedAt}
	if u.slug != nil {
		columns["slug"] = *u.slug
	}
	if u.releaseType != nil {
		columns["type"] = string(*u.releaseType)
	}
	if u.releaseDateSet {
		columns["release_date"] = nil
		if u.releaseDate != nil {
			columns["release_date"] = *u.releaseDate
		}
	}
	for column, value := range map[string]*string{
		"spotify_url":       u.spotifyURL,
		"apple_music_url":   u.appleMusicURL,
		"bandcamp_url":      u.bandcampURL,
		"youtube_music_url": u.youtubeMusicURL,
		"catalog_number":    u.catalogNumber,
	} {
		if value != nil {
			columns[column] = *value
		}
	}
	return columns
}

// changedFields names the audit fields whose requested value differs from
// release.
func (u releaseUpdate) changedFields(release *model.Release) []string {
	fields := make([]string, 0, 4)
	if u.slug != nil && !sameOptionalString(release.Slug, u.slug) {
		fields = append(fields, "slug")
	}
	if u.releaseType != nil && release.Type != *u.releaseType {
		fields = append(fields, "type")
	}
	if u.releaseDateSet && !sameOptionalTime(release.ReleaseDate, u.releaseDate) {
		fields = append(fields, "date")
	}
	for _, link := range []struct{ current, next *string }{
		{release.SpotifyURL, u.spotifyURL},
		{release.AppleMusicURL, u.appleMusicURL},
		{release.BandcampURL, u.bandcampURL},
		{release.YoutubeMusicURL, u.youtubeMusicURL},
		{release.CatalogNumber, u.catalogNumber},
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

func updateReleaseWithDB(
	ctx context.Context,
	tx *gorm.DB,
	release *model.Release,
	update releaseUpdate,
) (releaseMetadataChange, error) {
	if update.slug != nil {
		if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "release", "releases", *update.slug); err != nil {
			return releaseMetadataChange{}, err
		}
	}
	changed := update.changedFields(release)
	if len(changed) == 0 {
		return releaseMetadataChange{release: release}, nil
	}
	mutationNow := dbtime.Now()
	result := tx.Model(&model.Release{}).Where("id = ?", release.ID).Updates(update.columns(mutationNow))
	if result.Error != nil {
		return releaseMetadataChange{}, result.Error
	}
	if result.RowsAffected == 0 {
		return releaseMetadataChange{}, gorm.ErrRecordNotFound
	}
	release.UpdatedAt = mutationNow
	if update.releaseDateSet {
		release.ReleaseDate = update.releaseDate
	}
	return releaseMetadataChange{release: release, fields: changed}, nil
}

// releaseMetadataAudit records which Release settings or relations changed.
func releaseMetadataAudit(releaseID string, fields []string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseMetadataAuditRecord(m, releaseID, fields)
	})
}
