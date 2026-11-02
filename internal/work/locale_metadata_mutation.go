package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
)

type workLocaleMetadataMutationPlan struct {
	Locale        string
	UpdateTitle   bool
	Title         *string
	UpdateSummary bool
	Summary       *string
}

type workLocaleMetadataMutationResult struct {
	Effect         contentblock.MetadataEffect
	Title          string
	Summary        *string
	TitleChanged   bool
	SummaryChanged bool
}

func planWorkLocaleMetadataMutation(
	command UpdateWorkLocaleMetadataCommand,
	sourceLocale string,
) (workLocaleMetadataMutationPlan, error) {
	locale, err := normalizeWorkDocumentLocale(command.Locale)
	if err != nil {
		return workLocaleMetadataMutationPlan{}, err
	}
	plan := workLocaleMetadataMutationPlan{Locale: locale}
	if command.Title != nil {
		plan.UpdateTitle = true
		plan.Title = cloneOptionalString(command.Title)
		if locale == sourceLocale && strings.TrimSpace(*plan.Title) == "" {
			return workLocaleMetadataMutationPlan{}, domainerr.InvalidField("source_title", "cannot be empty")
		}
	}
	if command.SetSummary {
		plan.UpdateSummary = true
		plan.Summary = cloneOptionalString(command.Summary)
	}
	if !plan.UpdateTitle && !plan.UpdateSummary {
		return workLocaleMetadataMutationPlan{}, domainerr.InvalidField("metadata", "source_title or summary_update is required")
	}
	return plan, nil
}

func applyWorkLocaleMetadataMutation(
	ctx context.Context,
	store Store,
	workID string,
	plan workLocaleMetadataMutationPlan,
	now time.Time,
) (workLocaleMetadataMutationResult, error) {
	sourceLocale, err := store.LockSourceLocale(ctx, workID)
	if err != nil {
		return workLocaleMetadataMutationResult{}, err
	}
	row, err := store.LockLocaleText(ctx, workID, plan.Locale)
	if errors.Is(err, ErrLocaleNotFound) {
		return workLocaleMetadataMutationResult{}, domainerr.ResourceNotFound("work_translation", workID+":"+plan.Locale)
	}
	if err != nil {
		return workLocaleMetadataMutationResult{}, err
	}

	currentTitle := optionalString(row.Title)
	currentSummary := cloneOptionalString(row.Summary)
	nextTitle := currentTitle
	nextSummary := currentSummary
	if plan.UpdateTitle {
		nextTitle = strings.TrimSpace(*plan.Title)
	}
	if plan.UpdateSummary {
		nextSummary = cloneOptionalString(plan.Summary)
	}
	titleChanged := plan.UpdateTitle && nextTitle != currentTitle
	summaryChanged := plan.UpdateSummary && !nullableStringEqual(nextSummary, currentSummary)
	changed := titleChanged || summaryChanged
	sourceChanged := changed && plan.Locale == sourceLocale
	if !changed {
		return workLocaleMetadataMutationResult{
			Title:   currentTitle,
			Summary: currentSummary,
		}, nil
	}

	change := LocaleTextChange{UpdatedAt: now}
	if plan.UpdateTitle {
		change.SetTitle, change.Title = true, &nextTitle
	}
	if plan.UpdateSummary {
		change.SetSummary, change.Summary = true, nextSummary
	}
	if err := store.UpdateLocaleText(ctx, workID, plan.Locale, change); err != nil {
		if errors.Is(err, ErrRowChanged) {
			return workLocaleMetadataMutationResult{}, domainerr.Precondition(fmt.Sprintf("work locale %s changed; reload before saving", plan.Locale))
		}
		return workLocaleMetadataMutationResult{}, err
	}
	return workLocaleMetadataMutationResult{
		Effect: contentblock.MetadataEffect{
			Changed:                  true,
			AffectsTranslationSource: sourceChanged,
		},
		Title:          nextTitle,
		Summary:        nextSummary,
		TitleChanged:   titleChanged,
		SummaryChanged: summaryChanged,
	}, nil
}

// changedFields names the changed metadata for a content update event.
func (result workLocaleMetadataMutationResult) changedFields() []string {
	fields := make([]string, 0, 2)
	if result.TitleChanged {
		fields = append(fields, "title")
	}
	if result.SummaryChanged {
		fields = append(fields, "summary")
	}
	return fields
}
