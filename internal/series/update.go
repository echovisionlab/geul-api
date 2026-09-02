package series

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type seriesUpdatePlan struct {
	fields   structured.Fields
	nextSlug *string
	now      time.Time
}

func (s *SeriesService) UpdateSeries(
	ctx context.Context,
	req *connect.Request[managev1.UpdateSeriesRequest],
) (*connect.Response[managev1.Series], error) {
	plan, err := buildSeriesUpdatePlan(req.Msg)
	if err != nil {
		return nil, err
	}
	series, err := s.applySeriesUpdate(ctx, req.Msg, plan)
	if err != nil {
		return nil, err
	}
	if err := s.overlaySeriesSourceLocaleDocument(ctx, &series); err != nil {
		return nil, err
	}
	ogAsset, err := s.media.ReadyAsset(ctx, series.OgAssetID)
	if err != nil {
		return nil, err
	}
	response := s.toProtoSeries(&series, ogAsset)
	s.setSeriesFeaturedImageAsset(ctx, response)
	return connect.NewResponse(response), nil
}

func buildSeriesUpdatePlan(request *managev1.UpdateSeriesRequest) (seriesUpdatePlan, error) {
	plan := seriesUpdatePlan{now: time.Now().UTC(), fields: structured.Fields{}}
	if request.Slug != nil {
		normalized, err := validateSeriesSlug(*request.Slug)
		if err != nil {
			return seriesUpdatePlan{}, err
		}
		plan.nextSlug = &normalized
		plan.fields["slug"] = normalized
	}
	if request.Status != nil {
		if err := validateSeriesStatus(*request.Status); err != nil {
			return seriesUpdatePlan{}, err
		}
		plan.fields["status"] = *request.Status
	}
	return plan, nil
}

func (s *SeriesService) applySeriesUpdate(
	ctx context.Context,
	request *managev1.UpdateSeriesRequest,
	plan seriesUpdatePlan,
) (model.Series, error) {
	var series model.Series
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.requireSeriesPermissionAndLock(ctx, tx, request.Id, seriesUpdateAction(request)); err != nil {
			return err
		}
		if err := loadSeriesForUpdate(tx, request.Id, &series); err != nil {
			return err
		}
		oldSlug := series.Slug
		if err := validateSeriesUpdateSlug(ctx, tx, request.Id, plan.nextSlug); err != nil {
			return err
		}
		previousStatus := series.Status
		metadataFields, lifecycleChanged := seriesAuditChanges(series, plan)
		if err := applySeriesFields(tx, &series, plan, metadataFields, lifecycleChanged); err != nil {
			return err
		}
		nextSlug := plan.nextSlug
		if !containsSeriesAuditField(metadataFields, "slug") {
			nextSlug = nil
		}
		if err := s.updateSeriesMenuSlug(ctx, tx, series.ID, oldSlug, nextSlug); err != nil {
			return err
		}
		sourceChanged, err := s.updateSeriesSourceDocument(ctx, tx, series.ID, request, plan.now)
		if err != nil {
			return err
		}
		if !sourceChanged && (len(metadataFields) != 0 || lifecycleChanged) {
			contentDocument, err := loadSeriesContentDocumentState(ctx, tx, series.ID, false)
			if err != nil {
				return err
			}
			if _, err := advanceSeriesContentDocument(
				ctx, tx, series.ID, contentDocument.ID, contentDocument.Revision, plan.now,
			); err != nil {
				return err
			}
		}
		if err := s.appendPostSeriesSourceMetadataAudit(ctx, tx, series.ID, appendSourceCopyAuditField(metadataFields, sourceChanged)); err != nil {
			return err
		}
		if lifecycleChanged {
			if err := s.appendPostSeriesLifecycleAudit(ctx, tx, series.ID, postSeriesAuditState(previousStatus), postSeriesAuditState(plan.fields["status"].(string))); err != nil {
				return err
			}
		}
		if err := tx.First(&series, "id = ?", request.Id).Error; err != nil {
			return errs.Internal(err)
		}
		return nil
	})
	return series, err
}

func seriesUpdateAction(request *managev1.UpdateSeriesRequest) seriesAction {
	if request != nil && request.Status != nil {
		return policyv1.PostSeries.Publish
	}
	return policyv1.PostSeries.Edit
}

func loadSeriesForUpdate(tx *gorm.DB, seriesID string, series *model.Series) error {
	err := tx.First(series, "id = ?", seriesID).Error
	if err == gorm.ErrRecordNotFound {
		return errs.NotFound("series", seriesID)
	}
	if err != nil {
		return errs.Internal(err)
	}
	return nil
}

func validateSeriesUpdateSlug(ctx context.Context, tx *gorm.DB, seriesID string, slug *string) error {
	if slug == nil {
		return nil
	}
	if err := ensureSlugAvailable(ctx, tx, &model.Series{}, "series", *slug, seriesID); err != nil {
		return err
	}
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "series", "series", *slug)
}

func seriesAuditChanges(series model.Series, plan seriesUpdatePlan) ([]string, bool) {
	fields := make([]string, 0, 1)
	if plan.nextSlug != nil && *plan.nextSlug != series.Slug {
		fields = append(fields, "slug")
	}
	status, statusPresent := plan.fields["status"].(string)
	return fields, statusPresent && status != series.Status
}

func containsSeriesAuditField(fields []string, expected string) bool {
	for _, field := range fields {
		if field == expected {
			return true
		}
	}
	return false
}

func appendSourceCopyAuditField(fields []string, sourceChanged bool) []string {
	if !sourceChanged {
		return fields
	}
	return append(fields, "source_copy")
}

func applySeriesFields(tx *gorm.DB, series *model.Series, plan seriesUpdatePlan, metadataFields []string, lifecycleChanged bool) error {
	updates := structured.Fields{}
	if containsSeriesAuditField(metadataFields, "slug") {
		updates["slug"] = *plan.nextSlug
	}
	if lifecycleChanged {
		updates["status"] = plan.fields["status"]
	}
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = plan.now
	err := tx.Model(series).Updates(updates).Error
	if dberrors.IsUniqueViolation(err) && plan.nextSlug != nil {
		return errs.SlugAlreadyExists("series", *plan.nextSlug)
	}
	if err != nil {
		return errs.Internal(err)
	}
	return nil
}

func (s *SeriesService) updateSeriesMenuSlug(ctx context.Context, tx *gorm.DB, seriesID, oldSlug string, nextSlug *string) error {
	if nextSlug == nil || strings.TrimSpace(*nextSlug) == oldSlug {
		return nil
	}
	return s.menuTargets.UpdateSlug(ctx, tx, "series", seriesID, oldSlug, *nextSlug)
}

func (s *SeriesService) updateSeriesSourceDocument(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
	request *managev1.UpdateSeriesRequest,
	now time.Time,
) (bool, error) {
	if request.Title == nil && request.Description == nil {
		return false, nil
	}
	current, err := LoadRequiredSourceLocaleDocument(ctx, tx, seriesID)
	if err != nil {
		return false, err
	}
	contentDocument, err := loadSeriesContentDocumentState(ctx, tx, seriesID, false)
	if err != nil {
		return false, err
	}
	title, err := resolveSeriesUpdateTitle(current.Title, request.Title)
	if err != nil {
		return false, err
	}
	description := resolveSeriesUpdateDescription(current.Summary, request.Description)
	if nullableStringEqual(title, current.Title) && nullableStringEqual(description, current.Summary) {
		return false, nil
	}
	if err := SaveSourceLocaleDocument(
		ctx, tx, seriesID, contentDocument.SourceLocale,
		title, description, now.UTC(),
	); err != nil {
		return false, err
	}
	if _, err := advanceSeriesContentDocument(
		ctx, tx, seriesID, contentDocument.ID, contentDocument.Revision, now.UTC(),
	); err != nil {
		return false, err
	}
	if request.Title == nil {
		return true, nil
	}
	_, err = s.ogRefresh.RequestCurrent(
		ctx, tx, seriesID, contentDocument.SourceLocale, false, "series_title_updated",
	)
	return true, err
}

func resolveSeriesUpdateTitle(current, requested *string) (*string, error) {
	if requested == nil {
		return current, nil
	}
	value := strings.TrimSpace(*requested)
	if value == "" {
		return nil, errs.Required("title")
	}
	return &value, nil
}

func resolveSeriesUpdateDescription(current, requested *string) *string {
	if requested == nil {
		return current
	}
	value := strings.TrimSpace(*requested)
	if value == "" {
		return nil
	}
	return &value
}
