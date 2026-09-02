package legal

import (
	"fmt"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// BuildTranslationExtractionPlan extracts the policy title and locale-owned
// Rich Text values while preserving the source-owned Block graph.
func BuildTranslationExtractionPlan(
	job *model.TranslationJob,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	if job == nil || (job.EntityType != "privacy" && job.EntityType != "terms") {
		return nil, fmt.Errorf("legal translation requires privacy or terms")
	}
	return translation.BuildRichTextExtractionPlan(
		job, source, translation.RichTextDocumentFields{Title: true},
	)
}

// BuildTranslationCandidate applies validated translation results to a cloned
// policy locale overlay without changing source-owned Block structure.
func BuildTranslationCandidate(
	plan *translation.ExtractionPlan,
	source *translation.SourceDocument,
	results map[string]translation.UnitResult,
) (*translation.Candidate, error) {
	if plan == nil || (plan.EntityType != "privacy" && plan.EntityType != "terms") {
		return nil, fmt.Errorf("legal translation requires privacy or terms")
	}
	return translation.BuildRichTextCandidate(plan, source, results)
}
