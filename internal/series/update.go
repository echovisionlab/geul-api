package series

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type seriesUpdatePlan struct {
	nextSlug   *string
	nextStatus *string
	now        time.Time
}

// UpdateSeriesCommand edits a Series. A nil field is left alone; setting
// Status publishes or unpublishes it.
type UpdateSeriesCommand struct {
	ID          string
	Slug        *string
	Status      *string
	Title       *string
	Description *string
}

func (s *Service) UpdateSeries(ctx context.Context, command UpdateSeriesCommand) (*managev1.Series, error) {
	plan, err := buildSeriesUpdatePlan(command)
	if err != nil {
		return nil, err
	}
	var audits seriesAudits
	series, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.Series]{
		Access: lockedSeries(command.ID, seriesUpdateAction(command)),
		Change: func(ctx context.Context, tx *gorm.DB) (model.Series, error) {
			return s.applySeriesUpdate(ctx, tx, command, plan, &audits)
		},
		Audit: func(model.Series) mutation.Audit { return audits.audit() },
	})
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
	return response, nil
}

func buildSeriesUpdatePlan(command UpdateSeriesCommand) (seriesUpdatePlan, error) {
	plan := seriesUpdatePlan{now: time.Now().UTC()}
	if command.Slug != nil {
		normalized, err := validateSeriesSlug(*command.Slug)
		if err != nil {
			return seriesUpdatePlan{}, err
		}
		plan.nextSlug = &normalized
	}
	if command.Status != nil {
		if err := validateSeriesStatus(*command.Status); err != nil {
			return seriesUpdatePlan{}, err
		}
		plan.nextStatus = command.Status
	}
	return plan, nil
}

func (s *Service) applySeriesUpdate(
	ctx context.Context,
	tx *gorm.DB,
	command UpdateSeriesCommand,
	plan seriesUpdatePlan,
	audits *seriesAudits,
) (model.Series, error) {
	var series model.Series
	if err := loadSeriesForUpdate(tx, command.ID, &series); err != nil {
		return model.Series{}, err
	}
	previousStatus := series.Status
	metadataFields, lifecycleChanged, err := s.writeSeriesRoot(ctx, tx, &series, plan)
	if err != nil {
		return model.Series{}, err
	}
	sourceChanged, err := s.updateSeriesSourceDocument(ctx, tx, series.ID, command, plan.now)
	if err != nil {
		return model.Series{}, err
	}
	if !sourceChanged && (len(metadataFields) != 0 || lifecycleChanged) {
		contentDocument, err := loadSeriesContentDocumentState(ctx, tx, series.ID, false)
		if err != nil {
			return model.Series{}, err
		}
		if _, err := advanceSeriesContentDocument(ctx, tx, series.ID, contentDocument.ID, contentDocument.Revision, plan.now); err != nil {
			return model.Series{}, err
		}
	}
	audits.sourceMetadata(series.ID, appendSourceCopyAuditField(metadataFields, sourceChanged))
	if lifecycleChanged {
		audits.lifecycle(series.ID, postSeriesAuditState(previousStatus), postSeriesAuditState(*plan.nextStatus))
	}
	if err := tx.First(&series, "id = ?", command.ID).Error; err != nil {
		return model.Series{}, err
	}
	return series, nil
}

// writeSeriesRoot writes the requested slug and status to the locked series
// and moves Menu targets to a changed slug. It returns the changed metadata
// audit fields and whether the status changed.
func (s *Service) writeSeriesRoot(
	ctx context.Context,
	tx *gorm.DB,
	series *model.Series,
	plan seriesUpdatePlan,
) ([]string, bool, error) {
	oldSlug := series.Slug
	if err := validateSeriesUpdateSlug(ctx, tx, series.ID, plan.nextSlug); err != nil {
		return nil, false, err
	}
	metadataFields, lifecycleChanged := seriesAuditChanges(*series, plan)
	if err := applySeriesFields(tx, series, plan, metadataFields, lifecycleChanged); err != nil {
		return nil, false, err
	}
	nextSlug := plan.nextSlug
	if !containsSeriesAuditField(metadataFields, "slug") {
		nextSlug = nil
	}
	if err := s.updateSeriesMenuSlug(ctx, tx, series.ID, oldSlug, nextSlug); err != nil {
		return nil, false, err
	}
	return metadataFields, lifecycleChanged, nil
}

func seriesUpdateAction(command UpdateSeriesCommand) seriesAction {
	if command.Status != nil {
		return policyv1.PostSeries.Publish
	}
	return policyv1.PostSeries.Edit
}

func loadSeriesForUpdate(tx *gorm.DB, seriesID string, series *model.Series) error {
	err := tx.First(series, "id = ?", seriesID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerr.ResourceNotFound("series", seriesID)
	}
	if err != nil {
		return err
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
	return fields, plan.nextStatus != nil && *plan.nextStatus != series.Status
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
		updates["status"] = *plan.nextStatus
	}
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = plan.now
	err := tx.Model(series).Updates(updates).Error
	if dberrors.IsUniqueViolation(err) && plan.nextSlug != nil {
		return domainerr.Duplicate("series", "slug", *plan.nextSlug).WithCause(err)
	}
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) updateSeriesMenuSlug(ctx context.Context, tx *gorm.DB, seriesID, oldSlug string, nextSlug *string) error {
	if nextSlug == nil || strings.TrimSpace(*nextSlug) == oldSlug {
		return nil
	}
	return s.menuTargets.UpdateSlug(ctx, tx, "series", seriesID, oldSlug, *nextSlug)
}

func (s *Service) updateSeriesSourceDocument(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
	command UpdateSeriesCommand,
	now time.Time,
) (bool, error) {
	if command.Title == nil && command.Description == nil {
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
	title, err := resolveSeriesUpdateTitle(current.Title, command.Title)
	if err != nil {
		return false, err
	}
	description := resolveSeriesUpdateDescription(current.Summary, command.Description)
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
	if command.Title == nil {
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
		return nil, domainerr.Required("title")
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
