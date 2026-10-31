package release

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ReleaseService implements the ReleaseService Connect handler
func (s *ReleaseService) UpdateRelease(
	ctx context.Context,
	req *connect.Request[managev1.UpdateReleaseRequest],
) (*connect.Response[managev1.UpdateReleaseResponse], error) {
	updates, err := s.buildReleaseUpdates(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	changed := false
	var release *model.Release
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		changed, release, err = s.updateReleaseWithDB(ctx, tx, req.Msg, updates)
		if err != nil || !changed {
			return err
		}
		return publishContentUpdatedEvent(ctx, tx, s.asyncPublisher, buildManageReleaseContentUpdatedEvent(req.Msg))
	}); err != nil {
		if dberrors.IsUniqueViolation(err) {
			return nil, errs.WithCause(errs.SlugAlreadyExists("release", "slug"), err)
		}
		return nil, classifyReleaseMutationError(err, req.Msg.Id)
	}

	response := &managev1.UpdateReleaseResponse{
		Id:        release.ID,
		Changed:   changed,
		UpdatedAt: timestamppb.New(release.UpdatedAt),
	}
	if release.ReleaseDate != nil {
		response.ReleaseDate = timestamppb.New(*release.ReleaseDate)
	}
	return connect.NewResponse(response), nil
}

// releaseUpdate is a validated Release metadata update. Nil fields are left
// alone; releaseDateSet with a nil releaseDate clears the date.
type releaseUpdate struct {
	slug            *string
	releaseType     *string
	releaseDateSet  bool
	releaseDate     *time.Time
	spotifyURL      *string
	appleMusicURL   *string
	bandcampURL     *string
	youtubeMusicURL *string
	catalogNumber   *string
}

func (s *ReleaseService) buildReleaseUpdates(
	ctx context.Context,
	request *managev1.UpdateReleaseRequest,
) (releaseUpdate, error) {
	update := releaseUpdate{
		spotifyURL:      request.SpotifyUrl,
		appleMusicURL:   request.AppleMusicUrl,
		bandcampURL:     request.BandcampUrl,
		youtubeMusicURL: request.YoutubeMusicUrl,
		catalogNumber:   request.CatalogNumber,
	}
	if request.Slug != nil {
		if err := validateSlugWithoutSlash(*request.Slug); err != nil {
			return releaseUpdate{}, err
		}
		if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "release", "releases", *request.Slug); err != nil {
			return releaseUpdate{}, err
		}
		update.slug = request.Slug
	}
	if request.Type != nil {
		releaseType := releaseTypeToString(*request.Type)
		update.releaseType = &releaseType
	}
	switch change := request.ReleaseDateChange.(type) {
	case *managev1.UpdateReleaseRequest_SetReleaseDate:
		if change.SetReleaseDate == nil || !change.SetReleaseDate.IsValid() {
			return releaseUpdate{}, errs.InvalidArgument("release_date", "must be a valid timestamp")
		}
		releaseDate := change.SetReleaseDate.AsTime()
		update.releaseDateSet, update.releaseDate = true, &releaseDate
	case *managev1.UpdateReleaseRequest_ClearReleaseDate:
		update.releaseDateSet = true
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
		columns["type"] = *u.releaseType
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

func (s *ReleaseService) updateReleaseWithDB(
	ctx context.Context,
	tx *gorm.DB,
	request *managev1.UpdateReleaseRequest,
	update releaseUpdate,
) (bool, *model.Release, error) {
	release, err := lockReleaseForUpdate(ctx, tx, request.Id)
	if err != nil {
		return false, nil, err
	}
	if err := requireActiveReleaseAction(ctx, tx, s.spiceDB, request.Id, releaseActionEdit); err != nil {
		return false, nil, err
	}
	if request.Slug != nil {
		if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "release", "releases", *request.Slug); err != nil {
			return false, nil, err
		}
	}
	changed := update.changedFields(release)
	if len(changed) == 0 {
		return false, release, nil
	}
	mutationNow := dbtime.Now()
	result := tx.Model(&model.Release{}).Where("id = ?", release.ID).Updates(update.columns(mutationNow))
	if result.Error != nil {
		return false, nil, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil, gorm.ErrRecordNotFound
	}
	if err := s.appendReleaseMetadataAudit(ctx, tx, release.ID, changed); err != nil {
		return false, nil, err
	}
	release.UpdatedAt = mutationNow
	if update.releaseDateSet {
		release.ReleaseDate = update.releaseDate
	}
	return true, release, nil
}

// DeleteRelease deletes a release (admin only)
