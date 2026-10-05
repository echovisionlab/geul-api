package release

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ReleaseService implements the ReleaseService Connect handler
func (s *ReleaseService) SetReleaseArtists(
	ctx context.Context,
	req *connect.Request[managev1.SetReleaseArtistsRequest],
) (*connect.Response[managev1.SuccessResponse], error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockReleaseForUpdate(ctx, tx, req.Msg.ReleaseId); err != nil {
			return err
		}
		if err := requireActiveReleaseAction(ctx, tx, s.spiceDB, req.Msg.ReleaseId, releaseActionManage); err != nil {
			return err
		}
		if err := requireObservedRelationSnapshot(req.Msg.Observed); err != nil {
			return err
		}
		var existing []model.ReleaseArtist
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).Order("sort_order ASC, artist_id ASC").Find(&existing).Error; err != nil {
			return err
		}
		for _, artist := range req.Msg.Artists {
			if artist == nil {
				return errs.InvalidArgument("artists", "must not contain null")
			}
		}
		next := mergeReleaseArtists(existing, req.Msg.Observed.Artists, req.Msg.Artists, req.Msg.OrderIntent)
		if sameReleaseArtistRows(existing, next) {
			return nil
		}
		if err := persistReleaseArtists(tx, req.Msg.ReleaseId, existing, next); err != nil {
			return err
		}

		if err := tx.Model(&model.Release{}).Where("id = ?", req.Msg.ReleaseId).
			Update("updated_at", time.Now()).Error; err != nil {
			return err
		}

		return s.appendReleaseMetadataAudit(ctx, tx, req.Msg.ReleaseId, []string{"artists"})
	})
	if err != nil {
		return nil, classifyReleaseMutationError(err, req.Msg.ReleaseId)
	}

	publishContentUpdatedEvent(
		ctx,
		s.asyncPublisher,
		buildReleaseRelationMutationContentUpdatedEvent(
			req.Msg.ReleaseId,
			[]string{"relations.artists"},
		),
	)

	return connect.NewResponse(&managev1.SuccessResponse{Success: true}), nil
}

// SetReleaseLabels replaces all labels for a release
func (s *ReleaseService) SetReleaseLabels(
	ctx context.Context,
	req *connect.Request[managev1.SetReleaseLabelsRequest],
) (*connect.Response[managev1.SuccessResponse], error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockReleaseForUpdate(ctx, tx, req.Msg.ReleaseId); err != nil {
			return err
		}
		if err := requireActiveReleaseAction(ctx, tx, s.spiceDB, req.Msg.ReleaseId, releaseActionManage); err != nil {
			return err
		}
		if err := requireObservedRelationSnapshot(req.Msg.Observed); err != nil {
			return err
		}
		var existing []model.ReleaseLabel
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).Order("sort_order ASC, label_id ASC").Find(&existing).Error; err != nil {
			return err
		}
		for _, label := range req.Msg.Labels {
			if label == nil {
				return errs.InvalidArgument("labels", "must not contain null")
			}
		}
		next := mergeReleaseLabels(existing, req.Msg.Observed.Labels, req.Msg.Labels, req.Msg.OrderIntent)
		if sameReleaseLabelRows(existing, next) {
			return nil
		}
		if err := persistReleaseLabels(tx, req.Msg.ReleaseId, existing, next); err != nil {
			return err
		}

		// Update release updated_at
		if err := tx.Model(&model.Release{}).Where("id = ?", req.Msg.ReleaseId).
			Update("updated_at", time.Now()).Error; err != nil {
			return err
		}

		return s.appendReleaseMetadataAudit(ctx, tx, req.Msg.ReleaseId, []string{"labels"})
	})

	if err != nil {
		return nil, classifyReleaseMutationError(err, req.Msg.ReleaseId)
	}

	publishContentUpdatedEvent(
		ctx,
		s.asyncPublisher,
		buildReleaseRelationMutationContentUpdatedEvent(
			req.Msg.ReleaseId,
			[]string{"relations.labels"},
		),
	)

	return connect.NewResponse(&managev1.SuccessResponse{Success: true}), nil
}

// SetReleaseCategories replaces all categories for a release
func (s *ReleaseService) SetReleaseCategories(
	ctx context.Context,
	req *connect.Request[managev1.SetReleaseCategoriesRequest],
) (*connect.Response[managev1.SuccessResponse], error) {
	return setReleaseReferenceSet(ctx, s, req.Msg.ReleaseId, "categories", req.Msg.CategoryIds, req.Msg.Observed,
		func(releaseID, categoryID string) *model.ReleaseCategory {
			return &model.ReleaseCategory{ReleaseID: releaseID, CategoryID: categoryID}
		})
}

// SetReleaseGenres replaces all genres for a release
func (s *ReleaseService) SetReleaseGenres(
	ctx context.Context,
	req *connect.Request[managev1.SetReleaseGenresRequest],
) (*connect.Response[managev1.SuccessResponse], error) {
	return setReleaseReferenceSet(ctx, s, req.Msg.ReleaseId, "genres", req.Msg.GenreIds, req.Msg.Observed,
		func(releaseID, genreID string) *model.ReleaseGenre {
			return &model.ReleaseGenre{ReleaseID: releaseID, GenreID: genreID}
		})
}

// SetReleaseStyles replaces all styles for a release
func (s *ReleaseService) SetReleaseStyles(
	ctx context.Context,
	req *connect.Request[managev1.SetReleaseStylesRequest],
) (*connect.Response[managev1.SuccessResponse], error) {
	return setReleaseReferenceSet(ctx, s, req.Msg.ReleaseId, "styles", req.Msg.StyleIds, req.Msg.Observed,
		func(releaseID, styleID string) *model.ReleaseStyle {
			return &model.ReleaseStyle{ReleaseID: releaseID, StyleID: styleID}
		})
}

// SetReleaseFormats replaces all formats for a release
func (s *ReleaseService) SetReleaseFormats(
	ctx context.Context,
	req *connect.Request[managev1.SetReleaseFormatsRequest],
) (*connect.Response[managev1.SuccessResponse], error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockReleaseForUpdate(ctx, tx, req.Msg.ReleaseId); err != nil {
			return err
		}
		if err := requireActiveReleaseAction(ctx, tx, s.spiceDB, req.Msg.ReleaseId, releaseActionManage); err != nil {
			return err
		}
		if err := requireObservedRelationSnapshot(req.Msg.Observed); err != nil {
			return err
		}
		var existing []model.ReleaseFormat
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).Order("format_id ASC").Find(&existing).Error; err != nil {
			return err
		}
		for _, format := range req.Msg.Formats {
			if format == nil {
				return errs.InvalidArgument("formats", "must not contain null")
			}
		}
		next := mergeReleaseFormats(existing, req.Msg.Observed.Formats, req.Msg.Formats)
		if sameReleaseFormatRows(existing, next) {
			return nil
		}
		if err := persistReleaseFormats(tx, req.Msg.ReleaseId, existing, next); err != nil {
			return err
		}

		// Update release updated_at
		if err := tx.Model(&model.Release{}).Where("id = ?", req.Msg.ReleaseId).
			Update("updated_at", time.Now()).Error; err != nil {
			return err
		}

		return s.appendReleaseMetadataAudit(ctx, tx, req.Msg.ReleaseId, []string{"formats"})
	})

	if err != nil {
		return nil, classifyReleaseMutationError(err, req.Msg.ReleaseId)
	}

	publishContentUpdatedEvent(
		ctx,
		s.asyncPublisher,
		buildReleaseRelationMutationContentUpdatedEvent(
			req.Msg.ReleaseId,
			[]string{"relations.formats"},
		),
	)

	return connect.NewResponse(&managev1.SuccessResponse{Success: true}), nil
}

// SetReleaseCredits replaces all credits for a release
func (s *ReleaseService) SetReleaseCredits(
	ctx context.Context,
	req *connect.Request[managev1.SetReleaseCreditsRequest],
) (*connect.Response[managev1.SuccessResponse], error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockReleaseForUpdate(ctx, tx, req.Msg.ReleaseId); err != nil {
			return err
		}
		if err := requireActiveReleaseAction(ctx, tx, s.spiceDB, req.Msg.ReleaseId, releaseActionManage); err != nil {
			return err
		}
		if err := requireObservedRelationSnapshot(req.Msg.Observed); err != nil {
			return err
		}
		var existing []model.ReleaseCredit
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).Order("sort_order ASC, id ASC").Find(&existing).Error; err != nil {
			return err
		}
		for _, credit := range req.Msg.Credits {
			if credit == nil {
				return errs.InvalidArgument("credits", "must not contain null")
			}
			if credit.GetId() == "" {
				id := uuid.NewString()
				credit.Id = &id
			}
		}
		next := mergeReleaseCredits(existing, req.Msg.Observed.Credits, req.Msg.Credits, req.Msg.OrderIntent)
		if sameReleaseCreditRows(existing, next) {
			return nil
		}
		references := make([]authorizationtarget.Reference, 0, len(req.Msg.Credits))
		for i, credit := range req.Msg.Credits {
			if credit.MemberId == nil {
				continue
			}
			references = append(references, authorizationtarget.Reference{
				MemberID: *credit.MemberId,
				Field:    fmt.Sprintf("credit[%d].member_id", i),
			})
		}
		if err := authorizationtarget.LockReferences(ctx, tx, references); err != nil {
			return err
		}
		if err := persistReleaseCredits(tx, req.Msg.ReleaseId, existing, next); err != nil {
			return err
		}

		// Update release updated_at
		if err := tx.Model(&model.Release{}).Where("id = ?", req.Msg.ReleaseId).
			Update("updated_at", time.Now()).Error; err != nil {
			return err
		}

		return s.appendReleaseMetadataAudit(ctx, tx, req.Msg.ReleaseId, []string{"credits"})
	})

	if err != nil {
		return nil, classifyReleaseMutationError(err, req.Msg.ReleaseId)
	}

	publishContentUpdatedEvent(
		ctx,
		s.asyncPublisher,
		buildReleaseRelationMutationContentUpdatedEvent(
			req.Msg.ReleaseId,
			[]string{"relations.credits"},
		),
	)

	return connect.NewResponse(&managev1.SuccessResponse{Success: true}), nil
}
