package series

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type seriesUpdatePlan struct {
	nextSlug   *string
	nextStatus *Status
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

func (s *Service) UpdateSeries(ctx context.Context, command UpdateSeriesCommand) (Detail, error) {
	plan, err := buildSeriesUpdatePlan(command)
	if err != nil {
		return Detail{}, err
	}
	var audits seriesAudits
	series, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Series, Store]{
		Access: lockedSeries(command.ID, seriesUpdateAction(command)),
		Change: func(ctx context.Context, store Store) (Series, error) {
			return applySeriesUpdate(ctx, store, command, plan, &audits)
		},
		Audit: func(Series) mutation.Audit { return audits.audit() },
	})
	if err != nil {
		return Detail{}, err
	}
	if err := s.units.Read(ctx, func(store Store) error { return overlaySourceCopy(ctx, store, &series) }); err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, series)
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
		status, err := validateSeriesStatus(*command.Status)
		if err != nil {
			return seriesUpdatePlan{}, err
		}
		plan.nextStatus = &status
	}
	return plan, nil
}

func applySeriesUpdate(
	ctx context.Context,
	store Store,
	command UpdateSeriesCommand,
	plan seriesUpdatePlan,
	audits *seriesAudits,
) (Series, error) {
	series, err := loadSeries(ctx, store, command.ID)
	if err != nil {
		return Series{}, err
	}
	previousStatus := series.Status
	metadataFields, lifecycleChanged, err := writeSeriesRoot(ctx, store, series, plan)
	if err != nil {
		return Series{}, err
	}
	sourceChanged, err := updateSeriesSourceDocument(ctx, store, series.ID, command, plan.now)
	if err != nil {
		return Series{}, err
	}
	if !sourceChanged && (len(metadataFields) != 0 || lifecycleChanged) {
		contentDocument, err := loadSeriesContentDocumentState(ctx, store, series.ID, false)
		if err != nil {
			return Series{}, err
		}
		if _, err := advanceSeriesContentDocument(ctx, store, series.ID, contentDocument.ID, contentDocument.Revision, plan.now); err != nil {
			return Series{}, err
		}
	}
	audits.sourceMetadata(series.ID, appendSourceCopyAuditField(metadataFields, sourceChanged))
	if lifecycleChanged {
		audits.lifecycle(series.ID, postSeriesAuditState(previousStatus), postSeriesAuditState(*plan.nextStatus))
	}
	return store.Series(ctx, command.ID)
}

// writeSeriesRoot writes the requested slug and status to the locked series
// and moves Menu targets to a changed slug. It returns the changed metadata
// audit fields and whether the status changed.
func writeSeriesRoot(
	ctx context.Context,
	store Store,
	series Series,
	plan seriesUpdatePlan,
) ([]string, bool, error) {
	oldSlug := series.Slug
	if err := validateSeriesUpdateSlug(ctx, store, series.ID, plan.nextSlug); err != nil {
		return nil, false, err
	}
	metadataFields, lifecycleChanged := seriesAuditChanges(series, plan)
	if err := applySeriesFields(ctx, store, series.ID, plan, metadataFields, lifecycleChanged); err != nil {
		return nil, false, err
	}
	nextSlug := plan.nextSlug
	if !containsSeriesAuditField(metadataFields, "slug") {
		nextSlug = nil
	}
	if err := updateSeriesMenuSlug(ctx, store, series.ID, oldSlug, nextSlug); err != nil {
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

func validateSeriesUpdateSlug(ctx context.Context, store Store, seriesID string, slug *string) error {
	if slug == nil {
		return nil
	}
	if err := ensureSlugAvailable(ctx, store, *slug, seriesID); err != nil {
		return err
	}
	return store.EnsureRouteAvailable(ctx, *slug, true)
}

func seriesAuditChanges(series Series, plan seriesUpdatePlan) ([]string, bool) {
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

func applySeriesFields(ctx context.Context, store Store, seriesID string, plan seriesUpdatePlan, metadataFields []string, lifecycleChanged bool) error {
	var slug *string
	if containsSeriesAuditField(metadataFields, "slug") {
		slug = plan.nextSlug
	}
	var status *Status
	if lifecycleChanged {
		status = plan.nextStatus
	}
	if slug == nil && status == nil {
		return nil
	}
	err := store.UpdateSeriesRoot(ctx, seriesID, slug, status, plan.now)
	if errors.Is(err, ErrSlugTaken) && plan.nextSlug != nil {
		return domainerr.Duplicate("series", "slug", *plan.nextSlug).WithCause(err)
	}
	return err
}

func updateSeriesMenuSlug(ctx context.Context, store Store, seriesID, oldSlug string, nextSlug *string) error {
	if nextSlug == nil || strings.TrimSpace(*nextSlug) == oldSlug {
		return nil
	}
	return store.RenameMenuTargets(ctx, seriesID, oldSlug, *nextSlug)
}

func updateSeriesSourceDocument(
	ctx context.Context,
	store Store,
	seriesID string,
	command UpdateSeriesCommand,
	now time.Time,
) (bool, error) {
	if command.Title == nil && command.Description == nil {
		return false, nil
	}
	current, err := loadRequiredSourceCopy(ctx, store, seriesID)
	if err != nil {
		return false, err
	}
	contentDocument, err := loadSeriesContentDocumentState(ctx, store, seriesID, false)
	if err != nil {
		return false, err
	}
	title, err := resolveSeriesUpdateTitle(current.Title, command.Title)
	if err != nil {
		return false, err
	}
	description := resolveSeriesUpdateDescription(current.Summary, command.Description)
	if equalOptionalString(title, current.Title) && equalOptionalString(description, current.Summary) {
		return false, nil
	}
	if err := saveSourceCopy(
		ctx, store, seriesID, contentDocument.SourceLocale,
		title, description, now.UTC(),
	); err != nil {
		return false, err
	}
	if _, err := advanceSeriesContentDocument(
		ctx, store, seriesID, contentDocument.ID, contentDocument.Revision, now.UTC(),
	); err != nil {
		return false, err
	}
	if command.Title == nil {
		return true, nil
	}
	_, err = store.RequestOG(
		ctx, seriesID, contentDocument.SourceLocale, false, "series_title_updated",
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
