package release

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dberrors"
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
		return err
	}); err != nil {
		if dberrors.IsUniqueViolation(err) {
			return nil, errs.SlugAlreadyExists("release", "slug")
		}
		return nil, classifyReleaseMutationError(err, req.Msg.Id)
	}

	if changed {
		publishContentUpdatedEvent(ctx, s.asyncPublisher, buildManageReleaseContentUpdatedEvent(req.Msg))
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

func (s *ReleaseService) buildReleaseUpdates(
	ctx context.Context,
	request *managev1.UpdateReleaseRequest,
) (structured.Fields, error) {
	updates := structured.Fields{}
	if request.Slug != nil {
		if err := validateSlugWithoutSlash(*request.Slug); err != nil {
			return nil, err
		}
		if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "release", "releases", *request.Slug); err != nil {
			return nil, err
		}
		updates["slug"] = *request.Slug
	}
	if request.Type != nil {
		updates["type"] = releaseTypeToString(*request.Type)
	}
	switch change := request.ReleaseDateChange.(type) {
	case *managev1.UpdateReleaseRequest_SetReleaseDate:
		if change.SetReleaseDate == nil || !change.SetReleaseDate.IsValid() {
			return nil, errs.InvalidArgument("release_date", "must be a valid timestamp")
		}
		updates["release_date"] = change.SetReleaseDate.AsTime()
	case *managev1.UpdateReleaseRequest_ClearReleaseDate:
		updates["release_date"] = nil
	}
	assignOptionalStringUpdate(updates, "spotify_url", request.SpotifyUrl)
	assignOptionalStringUpdate(updates, "apple_music_url", request.AppleMusicUrl)
	assignOptionalStringUpdate(updates, "bandcamp_url", request.BandcampUrl)
	assignOptionalStringUpdate(updates, "youtube_music_url", request.YoutubeMusicUrl)
	assignOptionalStringUpdate(updates, "catalog_number", request.CatalogNumber)
	return updates, nil
}

func assignOptionalStringUpdate(updates structured.Fields, field string, value *string) {
	if value != nil {
		updates[field] = *value
	}
}

func (s *ReleaseService) updateReleaseWithDB(
	ctx context.Context,
	tx *gorm.DB,
	request *managev1.UpdateReleaseRequest,
	updates structured.Fields,
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
	changed := releaseMetadataChangedFields(release, updates)
	if len(changed) == 0 {
		return false, release, nil
	}
	mutationNow := time.Now()
	updates["updated_at"] = mutationNow
	result := tx.Model(&model.Release{}).Where("id = ?", release.ID).Updates(updates)
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
	if value, ok := updates["release_date"]; ok {
		switch value := value.(type) {
		case time.Time:
			release.ReleaseDate = &value
		case nil:
			release.ReleaseDate = nil
		}
	}
	return true, release, nil
}

func releaseMetadataChangedFields(release *model.Release, updates structured.Fields) []string {
	fields := make([]string, 0, len(updates))
	for field, next := range updates {
		var current any
		switch field {
		case "slug":
			current = release.Slug
		case "type":
			current = release.Type
		case "release_date":
			current = release.ReleaseDate
		case "spotify_url":
			current = release.SpotifyURL
		case "apple_music_url":
			current = release.AppleMusicURL
		case "bandcamp_url":
			current = release.BandcampURL
		case "youtube_music_url":
			current = release.YoutubeMusicURL
		case "catalog_number":
			current = release.CatalogNumber
		default:
			continue
		}
		if !releaseAuditMetadataValuesEqual(current, next) {
			fields = append(fields, releaseAuditMetadataField(field))
		}
	}
	return fields
}

func releaseAuditMetadataValuesEqual(current, next any) bool {
	switch value := current.(type) {
	case *string:
		if next == nil {
			return value == nil
		}
		nextValue, ok := next.(string)
		return ok && value != nil && *value == nextValue
	case *time.Time:
		nextValue, ok := next.(time.Time)
		return ok && value != nil && value.Equal(nextValue)
	default:
		return current == next
	}
}

func releaseAuditMetadataField(field string) string {
	switch field {
	case "release_date":
		return "date"
	case "spotify_url", "apple_music_url", "bandcamp_url", "youtube_music_url", "catalog_number":
		return "links"
	default:
		return field
	}
}

// DeleteRelease deletes a release (admin or owner only)
