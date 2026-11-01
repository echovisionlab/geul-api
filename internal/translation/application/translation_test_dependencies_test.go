package application

import (
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// testTranslationDomains builds plans and candidates the way the owning
// adapters of Post, Form, Post Series and Email Layout do.
type testTranslationDomains struct{}

func translationJobStringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copy := value
	return &copy
}

func (testTranslationDomains) BuildExtractionPlan(
	job Job,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	switch job.EntityType {
	case "post":
		return translation.BuildRichTextExtractionPlan(
			&model.TranslationJob{
				EntityType: job.EntityType, EntityID: job.EntityID,
				SourceLocale: job.SourceLocale, TargetLocale: job.TargetLocale,
			},
			source,
			translation.RichTextDocumentFields{Title: true, Summary: true},
		)
	case "form":
		return form.BuildTranslationExtractionPlan(job.EntityID, job.SourceLocale, job.TargetLocale, source)
	case "series":
		return series.BuildTranslationExtractionPlan(job.EntityID, job.SourceLocale, job.TargetLocale, source)
	case "email_layout":
		return email.BuildLayoutTranslationExtractionPlan(job.EntityID, job.SourceLocale, job.TargetLocale, source)
	default:
		return nil, fmt.Errorf("unsupported test translation entity type %q", job.EntityType)
	}
}

func (testTranslationDomains) BuildCandidate(
	plan *translation.ExtractionPlan,
	source *translation.SourceDocument,
	results map[string]translation.UnitResult,
) (*translation.Candidate, error) {
	switch plan.EntityType {
	case "post":
		return translation.BuildRichTextCandidate(plan, source, results)
	case "form":
		return form.ApplyTranslationCandidate(source, results)
	case "series", "email_layout":
		return BuildGenericCandidate(plan, source, results)
	default:
		return nil, fmt.Errorf("unsupported test translation entity type %q", plan.EntityType)
	}
}
