package work

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
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
	request *intrav1.UpdateWorkLocaleMetadataRequest,
	sourceLocale string,
) (workLocaleMetadataMutationPlan, error) {
	if request == nil {
		return workLocaleMetadataMutationPlan{}, errs.InvalidArgument("request", "is required")
	}
	locale, err := normalizeWorkDocumentLocale(request.Locale)
	if err != nil {
		return workLocaleMetadataMutationPlan{}, err
	}
	plan := workLocaleMetadataMutationPlan{Locale: locale}
	if request.Title != nil {
		plan.UpdateTitle = true
		plan.Title = cloneOptionalString(request.Title)
		if locale == sourceLocale && strings.TrimSpace(*plan.Title) == "" {
			return workLocaleMetadataMutationPlan{}, errs.InvalidArgument("source_title", "cannot be empty")
		}
	}
	switch summary := request.SummaryUpdate.(type) {
	case nil:
	case *intrav1.UpdateWorkLocaleMetadataRequest_Summary:
		plan.UpdateSummary = true
		plan.Summary = cloneOptionalString(&summary.Summary)
	case *intrav1.UpdateWorkLocaleMetadataRequest_ClearSummary:
		plan.UpdateSummary = true
		plan.Summary = nil
	default:
		return workLocaleMetadataMutationPlan{}, errs.InvalidArgument("summary_update", "is invalid")
	}
	if !plan.UpdateTitle && !plan.UpdateSummary {
		return workLocaleMetadataMutationPlan{}, errs.InvalidArgument("metadata", "source_title or summary_update is required")
	}
	return plan, nil
}

func applyWorkLocaleMetadataMutation(
	ctx context.Context,
	tx *gorm.DB,
	workID string,
	plan workLocaleMetadataMutationPlan,
	now time.Time,
) (workLocaleMetadataMutationResult, error) {
	var root struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := tx.WithContext(ctx).
		Table("work").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("source_locale").
		Where("id = ?", workID).
		Take(&root).Error; err != nil {
		return workLocaleMetadataMutationResult{}, errs.Internal(err)
	}
	var row struct {
		Title   sql.NullString `gorm:"column:title"`
		Summary sql.NullString `gorm:"column:summary"`
	}
	result := tx.WithContext(ctx).Raw(`
		SELECT title, summary
		FROM work_translation
		WHERE entity_id = ? AND locale = ?
		FOR UPDATE
	`, workID, plan.Locale).Scan(&row)
	if result.Error != nil {
		return workLocaleMetadataMutationResult{}, errs.Internal(result.Error)
	}
	if result.RowsAffected == 0 {
		return workLocaleMetadataMutationResult{}, errs.NotFound("work_translation", workID+":"+plan.Locale)
	}

	currentTitle := ""
	if row.Title.Valid {
		currentTitle = row.Title.String
	}
	currentSummary := nullableStringFromSQL(row.Summary)
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
	sourceChanged := changed && plan.Locale == root.SourceLocale
	if !changed {
		return workLocaleMetadataMutationResult{
			Title:   currentTitle,
			Summary: currentSummary,
		}, nil
	}

	updates := structured.Fields{"updated_at": now}
	if plan.UpdateTitle {
		updates["title"] = nextTitle
	}
	if plan.UpdateSummary {
		updates["summary"] = nextSummary
	}
	update := tx.WithContext(ctx).
		Table("work_translation").
		Where("entity_id = ? AND locale = ?", workID, plan.Locale).
		Updates(updates)
	if update.Error != nil {
		return workLocaleMetadataMutationResult{}, errs.Internal(update.Error)
	}
	if update.RowsAffected != 1 {
		return workLocaleMetadataMutationResult{}, errs.FailedPrecondition(fmt.Sprintf("work locale %s changed; reload before saving", plan.Locale))
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

func nullableStringFromSQL(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
