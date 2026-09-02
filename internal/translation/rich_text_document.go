package translation

import (
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/proto"
)

// RichTextDocumentFields declares the entity-level locale fields owned by a
// typed Rich Text translation adapter. Block extraction itself is shared.
type RichTextDocumentFields struct {
	Title   bool
	Summary bool
}

// BuildRichTextExtractionPlan extracts entity fields and locale-owned Rich
// Text values while leaving structure and shared Block props untouched.
func BuildRichTextExtractionPlan(
	job *model.TranslationJob,
	source *SourceDocument,
	fields RichTextDocumentFields,
) (*ExtractionPlan, error) {
	if job == nil || source == nil || source.ContentBlockDocument == nil {
		return nil, fmt.Errorf("typed Rich Text translation source is required")
	}
	if strings.TrimSpace(job.EntityType) == "" || strings.TrimSpace(job.EntityID) == "" ||
		strings.TrimSpace(job.SourceLocale) == "" || strings.TrimSpace(job.TargetLocale) == "" {
		return nil, fmt.Errorf("typed Rich Text translation job identity is required")
	}
	if source.ContentBlockDocument.GetLocale() != job.SourceLocale {
		return nil, fmt.Errorf("typed Rich Text translation source locale does not match the job")
	}
	overlay := source.ContentBlockDocument.GetLocaleOverlay()
	if overlay == nil || overlay.GetLocale() != job.SourceLocale {
		return nil, fmt.Errorf("typed Rich Text source overlay is required")
	}

	units := make([]Unit, 0, 16)
	if fields.Title {
		if title := strings.TrimSpace(source.Title); title != "" {
			units = append(units, NewEntityUnit(
				job.EntityType, job.EntityID, job.SourceLocale, "title", title,
			))
		}
	}
	if fields.Summary {
		if summary := strings.TrimSpace(stringValue(source.Summary)); summary != "" {
			units = append(units, NewEntityUnit(
				job.EntityType, job.EntityID, job.SourceLocale, "summary", summary,
			))
		}
	}
	for _, block := range overlay.GetBlocks() {
		if block == nil || strings.TrimSpace(block.GetBlockId()) == "" {
			return nil, fmt.Errorf("typed Rich Text translation Block ID is required")
		}
		prefix := "block:" + block.GetBlockId()
		extracted, err := ExtractRichTextUnits(block, RichTextUnitScope{
			EntityType: job.EntityType, EntityID: job.EntityID, SourceLocale: job.SourceLocale,
			ContainerID: block.GetBlockId(), UnitPrefix: prefix, PathPrefix: prefix,
		})
		if err != nil {
			return nil, err
		}
		units = append(units, extracted...)
	}
	if !HasNonEmptyUnit(units) {
		return nil, ErrNoTranslatableUnits
	}

	return &ExtractionPlan{
		EntityType: job.EntityType, EntityID: job.EntityID,
		SourceLocale: job.SourceLocale, TargetLocale: job.TargetLocale,
		ContextTitle:   NonBlankString(strings.TrimSpace(source.Title)),
		ProtectedTerms: NormalizeProtectedTerms(source.ProtectedTerms),
		Units:          units,
		Bundles: BuildBundles(
			job.EntityType, job.EntityID, job.SourceLocale, job.TargetLocale,
			units, NonBlankString(strings.TrimSpace(stringValue(source.Summary))),
		),
	}, nil
}

// HasNonEmptyUnit reports whether at least one extracted unit contains text
// that a translation provider can translate. Explicit-empty stable units may
// remain in the same request graph, but cannot make an all-empty document
// translatable on their own.
func HasNonEmptyUnit(units []Unit) bool {
	for _, unit := range units {
		if strings.TrimSpace(unit.SourceText) != "" {
			return true
		}
	}
	return false
}

// BuildRichTextCandidate applies validated results to a cloned target locale
// overlay. The source-owned graph and stable Block identities are preserved.
func BuildRichTextCandidate(
	plan *ExtractionPlan,
	source *SourceDocument,
	results map[string]UnitResult,
) (*Candidate, error) {
	if plan == nil || strings.TrimSpace(plan.EntityType) == "" ||
		strings.TrimSpace(plan.TargetLocale) == "" || source == nil ||
		source.ContentBlockDocument == nil || source.ContentBlockDocument.GetLocaleOverlay() == nil {
		return nil, fmt.Errorf("typed Rich Text translation source is required")
	}

	document := proto.Clone(source.ContentBlockDocument).(*contentv1.LocalizedRichTextDocument)
	document.Locale = plan.TargetLocale
	document.LocaleOverlay.Locale = plan.TargetLocale
	for _, block := range document.LocaleOverlay.GetBlocks() {
		if block == nil || strings.TrimSpace(block.GetBlockId()) == "" {
			return nil, fmt.Errorf("typed Rich Text translation Block ID is required")
		}
		if err := ApplyRichTextResults(block, "block:"+block.GetBlockId(), results); err != nil {
			return nil, err
		}
	}

	candidate := &Candidate{
		ContentBlockLocaleOverlay: document.LocaleOverlay,
		ContentDocumentRevision:   source.ContentDocumentRevision,
	}
	ApplyCandidateFields(candidate, plan.Bundles, results)
	return candidate, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
