package release

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
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
		var existing []model.ReleaseArtist
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).Order("sort_order ASC, artist_id ASC").Find(&existing).Error; err != nil {
			return err
		}
		if sameReleaseArtists(existing, req.Msg.Artists) {
			return nil
		}
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).Delete(&model.ReleaseArtist{}).Error; err != nil {
			return err
		}

		for _, artist := range req.Msg.Artists {
			if strings.TrimSpace(artist.ArtistId) == "" {
				continue
			}
			if err := tx.Create(&model.ReleaseArtist{
				ReleaseID: req.Msg.ReleaseId,
				ArtistID:  artist.ArtistId,
				SortOrder: int(artist.SortOrder),
			}).Error; err != nil {
				return err
			}
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
		var existing []model.ReleaseLabel
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).Order("sort_order ASC, label_id ASC").Find(&existing).Error; err != nil {
			return err
		}
		if sameReleaseLabels(existing, req.Msg.Labels) {
			return nil
		}
		// Delete existing labels
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).Delete(&model.ReleaseLabel{}).Error; err != nil {
			return err
		}

		// Insert new labels
		for _, label := range req.Msg.Labels {
			rl := model.ReleaseLabel{
				ReleaseID: req.Msg.ReleaseId,
				LabelID:   label.LabelId,
				SortOrder: int(label.SortOrder),
			}
			if label.CatalogNumber != nil {
				rl.CatalogNumber = label.CatalogNumber
			}
			if err := tx.Create(&rl).Error; err != nil {
				return err
			}
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
	return setReleaseReferenceSet(ctx, s, req.Msg.ReleaseId, "categories", req.Msg.CategoryIds,
		func(releaseID, categoryID string) *model.ReleaseCategory {
			return &model.ReleaseCategory{ReleaseID: releaseID, CategoryID: categoryID}
		})
}

// SetReleaseGenres replaces all genres for a release
func (s *ReleaseService) SetReleaseGenres(
	ctx context.Context,
	req *connect.Request[managev1.SetReleaseGenresRequest],
) (*connect.Response[managev1.SuccessResponse], error) {
	return setReleaseReferenceSet(ctx, s, req.Msg.ReleaseId, "genres", req.Msg.GenreIds,
		func(releaseID, genreID string) *model.ReleaseGenre {
			return &model.ReleaseGenre{ReleaseID: releaseID, GenreID: genreID}
		})
}

// SetReleaseStyles replaces all styles for a release
func (s *ReleaseService) SetReleaseStyles(
	ctx context.Context,
	req *connect.Request[managev1.SetReleaseStylesRequest],
) (*connect.Response[managev1.SuccessResponse], error) {
	return setReleaseReferenceSet(ctx, s, req.Msg.ReleaseId, "styles", req.Msg.StyleIds,
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
		var existing []model.ReleaseFormat
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).Order("format_id ASC").Find(&existing).Error; err != nil {
			return err
		}
		if sameReleaseFormats(existing, req.Msg.Formats) {
			return nil
		}
		// Delete existing formats
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).Delete(&model.ReleaseFormat{}).Error; err != nil {
			return err
		}

		// Insert new formats
		for _, format := range req.Msg.Formats {
			rf := model.ReleaseFormat{
				ReleaseID: req.Msg.ReleaseId,
				FormatID:  format.FormatId,
			}
			if format.FormatDescription != nil {
				rf.FormatDescription = format.FormatDescription
			}
			if err := tx.Create(&rf).Error; err != nil {
				return err
			}
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
		var existing []model.ReleaseCredit
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).Order("sort_order ASC, id ASC").Find(&existing).Error; err != nil {
			return err
		}
		if sameReleaseCredits(existing, req.Msg.Credits) {
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
		// Delete existing credits
		if err := tx.Where("release_id = ?", req.Msg.ReleaseId).Delete(&model.ReleaseCredit{}).Error; err != nil {
			return err
		}

		// Insert new credits
		for _, credit := range req.Msg.Credits {
			rc := model.ReleaseCredit{
				ReleaseID:  req.Msg.ReleaseId,
				CreditRole: credit.CreditRole,
				SortOrder:  int(credit.SortOrder),
			}
			// Use provided ID or let DB generate one
			if credit.Id != nil && *credit.Id != "" {
				rc.ID = *credit.Id
			}
			if credit.ArtistId != nil {
				rc.ArtistID = credit.ArtistId
			}
			if credit.MemberId != nil {
				rc.MemberID = credit.MemberId
			}
			if credit.CreditedName != nil {
				rc.CreditedName = credit.CreditedName
			}
			if err := tx.Create(&rc).Error; err != nil {
				return err
			}
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

func sameReleaseArtists(existing []model.ReleaseArtist, requested []*managev1.ReleaseArtistInput) bool {
	actual := make([]struct {
		id   string
		sort int
	}, 0, len(existing))
	for _, row := range existing {
		actual = append(actual, struct {
			id   string
			sort int
		}{row.ArtistID, row.SortOrder})
	}
	next := make([]struct {
		id   string
		sort int
	}, 0, len(requested))
	for _, row := range requested {
		if row != nil && strings.TrimSpace(row.ArtistId) != "" {
			next = append(next, struct {
				id   string
				sort int
			}{row.ArtistId, int(row.SortOrder)})
		}
	}
	return reflect.DeepEqual(actual, next)
}

func sameReleaseLabels(existing []model.ReleaseLabel, requested []*managev1.ReleaseLabelInput) bool {
	actual := make([]struct {
		id      string
		catalog *string
		sort    int
	}, 0, len(existing))
	for _, row := range existing {
		actual = append(actual, struct {
			id      string
			catalog *string
			sort    int
		}{row.LabelID, row.CatalogNumber, row.SortOrder})
	}
	next := make([]struct {
		id      string
		catalog *string
		sort    int
	}, 0, len(requested))
	for _, row := range requested {
		if row != nil {
			next = append(next, struct {
				id      string
				catalog *string
				sort    int
			}{row.LabelId, row.CatalogNumber, int(row.SortOrder)})
		}
	}
	return reflect.DeepEqual(actual, next)
}

func sameReleaseFormats(existing []model.ReleaseFormat, requested []*managev1.ReleaseFormatInput) bool {
	actual := make([]struct {
		id          string
		description *string
	}, 0, len(existing))
	for _, row := range existing {
		actual = append(actual, struct {
			id          string
			description *string
		}{row.FormatID, row.FormatDescription})
	}
	next := make([]struct {
		id          string
		description *string
	}, 0, len(requested))
	for _, row := range requested {
		if row != nil {
			next = append(next, struct {
				id          string
				description *string
			}{row.FormatId, row.FormatDescription})
		}
	}
	return reflect.DeepEqual(actual, next)
}

func sameReleaseCredits(existing []model.ReleaseCredit, requested []*managev1.ReleaseCreditInput) bool {
	if len(existing) != len(requested) {
		return false
	}
	for i, row := range existing {
		next := requested[i]
		if next == nil || row.CreditRole != next.CreditRole || row.SortOrder != int(next.SortOrder) || !sameOptionalString(row.ArtistID, next.ArtistId) || !sameOptionalString(row.MemberID, next.MemberId) || !sameOptionalString(row.CreditedName, next.CreditedName) {
			return false
		}
	}
	return true
}
