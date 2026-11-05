package work

import (
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/translation"
)

// BuildTranslationExtractionPlan extracts Work-owned locale fields and typed
// Rich Text units from the canonical source document.
func BuildTranslationExtractionPlan(
	job *TranslationJob,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	if job == nil || job.EntityType != "work" || source == nil || source.ContentBlockDocument == nil {
		return nil, fmt.Errorf("typed Work translation source is required")
	}
	if strings.TrimSpace(job.SourceLocale) == "" || source.ContentBlockDocument.GetLocale() != job.SourceLocale {
		return nil, fmt.Errorf("typed Work translation source locale does not match the job")
	}
	overlay := source.ContentBlockDocument.GetLocaleOverlay()
	if overlay == nil || overlay.GetLocale() != job.SourceLocale {
		return nil, fmt.Errorf("typed Work source overlay is required")
	}

	units := make([]translation.Unit, 0, 16)
	if title := strings.TrimSpace(source.Title); title != "" {
		units = append(units, translation.NewEntityUnit(
			job.EntityType, job.EntityID, job.SourceLocale, "title", title,
		))
	}
	if summary := strings.TrimSpace(optionalString(source.Summary)); summary != "" {
		units = append(units, translation.NewEntityUnit(
			job.EntityType, job.EntityID, job.SourceLocale, "summary", summary,
		))
	}
	for _, block := range overlay.GetBlocks() {
		if block == nil || strings.TrimSpace(block.GetBlockId()) == "" {
			return nil, fmt.Errorf("typed Work translation Block ID is required")
		}
		blockID := block.GetBlockId()
		prefix := "block:" + blockID
		extracted, err := translation.ExtractRichTextUnits(block, translation.RichTextUnitScope{
			EntityType: job.EntityType, EntityID: job.EntityID, SourceLocale: job.SourceLocale,
			ContainerID: blockID, UnitPrefix: prefix, PathPrefix: prefix,
		})
		if err != nil {
			return nil, err
		}
		units = append(units, extracted...)
	}
	if !translation.HasNonEmptyUnit(units) {
		return nil, translation.ErrNoTranslatableUnits
	}

	return &translation.ExtractionPlan{
		EntityType:   job.EntityType,
		EntityID:     job.EntityID,
		SourceLocale: job.SourceLocale,
		TargetLocale: job.TargetLocale,
		ContextTitle: translation.NonBlankString(strings.TrimSpace(source.Title)),
		Units:        units,
		Bundles: translation.BuildBundles(
			job.EntityType,
			job.EntityID,
			job.SourceLocale,
			job.TargetLocale,
			units,
			translation.NonBlankString(strings.TrimSpace(optionalString(source.Summary))),
		),
	}, nil
}
