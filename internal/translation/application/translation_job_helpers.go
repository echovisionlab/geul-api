package application

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// validateTranslationJobRequestArtifact keeps the event contract's rule
// for a job's request artifact: it is named by a digest.
func validateTranslationJobRequestArtifact(job *Job) error {
	if job == nil {
		return fmt.Errorf("translation job is required")
	}
	if strings.TrimSpace(job.RequestArtifactDigest) == "" {
		return errors.New("translation job request artifact digest is required")
	}
	return nil
}

// ValidateJobRequester rejects a job whose requester is not a canonical
// Member UUID.
func ValidateJobRequester(job *Job) error {
	if job == nil {
		return fmt.Errorf("translation job is required")
	}
	rawMemberID := job.RequestedByMemberID
	memberID := strings.TrimSpace(rawMemberID)
	parsed, err := uuid.Parse(memberID)
	if rawMemberID != memberID || err != nil || parsed == uuid.Nil || parsed.String() != memberID {
		return fmt.Errorf("translation requester Member id is not a canonical UUID")
	}
	return nil
}

func buildTranslationProviderRequest(
	job *Job,
	plan *translation.ExtractionPlan,
) (translation.ProviderRequest, error) {
	if job == nil {
		return translation.ProviderRequest{}, fmt.Errorf("translation job is required")
	}
	if plan == nil {
		return translation.ProviderRequest{}, fmt.Errorf("translation extraction plan is required")
	}
	if len(plan.Bundles) == 0 {
		return translation.ProviderRequest{}, fmt.Errorf("translation extraction plan does not contain translatable bundles")
	}

	profile := buildDefaultTranslationGenerationProfile(
		job.EntityType,
		job.SourceLocale,
		job.TargetLocale,
		hasHTMLContentUnits(plan.Units),
		plan.ProtectedTerms,
	)
	document, err := translation.BuildXLIFFDocument(plan)
	if err != nil {
		return translation.ProviderRequest{}, err
	}
	document.File.ID = translationXLIFFFileID(job)

	return translation.ProviderRequest{
		RequestID:   job.ID,
		OperationID: job.OperationID,
		Profile:     profile,
		Document:    *document,
	}, nil
}

func translationXLIFFFileID(job *Job) string {
	parts := []string{
		job.EntityType,
		job.EntityID,
		job.SourceLocale,
		job.TargetLocale,
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "source:" + hex.EncodeToString(digest[:])
}

func buildTranslationCandidateContent(
	plans DomainPlans,
	plan *translation.ExtractionPlan,
	source *translation.SourceDocument,
	response *translation.ProviderResponse,
) (*translation.Candidate, error) {
	if response == nil {
		return nil, fmt.Errorf("translation provider response is required")
	}
	if plan == nil {
		return nil, fmt.Errorf("translation extraction plan is required")
	}
	if source == nil {
		return nil, fmt.Errorf("translation source document is required")
	}

	if plans == nil {
		return nil, fmt.Errorf("translation domain registry is required")
	}
	return plans.BuildCandidate(plan, source, translation.FlattenResponse(*response))
}

// BuildGenericCandidate applies provider results for revision-backed domains
// whose owning adapter supplies the source document and persistence policy but
// does not need a specialized typed Content Block candidate builder.
func BuildGenericCandidate(
	plan *translation.ExtractionPlan,
	source *translation.SourceDocument,
	results map[string]translation.UnitResult,
) (*translation.Candidate, error) {
	if plan == nil || source == nil {
		return nil, fmt.Errorf("translation plan and source document are required")
	}
	candidate := &translation.Candidate{}
	if err := applyTranslationCandidateBodies(candidate, plan, source, results); err != nil {
		return nil, err
	}
	translation.ApplyCandidateFields(candidate, plan.Bundles, results)
	return candidate, nil
}

func applyTranslationCandidateBodies(
	candidate *translation.Candidate,
	plan *translation.ExtractionPlan,
	source *translation.SourceDocument,
	results map[string]translation.UnitResult,
) error {
	if hasStructuredContentUnits(plan.Units) && len(source.ContentJSON) > 0 {
		return fmt.Errorf("structured translation candidate requires an owning typed adapter")
	}
	if hasHTMLContentUnits(plan.Units) && source.ContentHTML != nil {
		contentHTML, contentText, err := applyTranslationCandidateHTML(plan.EntityType, source, results)
		if err != nil {
			return err
		}
		candidate.ContentHTML = contentHTML
		if contentText != nil {
			candidate.ContentText = contentText
		}
	}
	return nil
}

func applyTranslationCandidateHTML(
	entityType string,
	source *translation.SourceDocument,
	resultByUnit map[string]translation.UnitResult,
) (*string, *string, error) {
	if entityType == "email_layout" {
		sourceHTML := ""
		if source.ContentHTML != nil {
			sourceHTML = *source.ContentHTML
		}
		return email.ApplyLayoutHTMLTranslationCandidate(sourceHTML, resultByUnit)
	}
	return applyHTMLTranslationCandidate(source, resultByUnit)
}

func translationJobStartedAt(job *Job, fallback time.Time) time.Time {
	if job == nil {
		return time.Time{}
	}
	if job.StartedAt != nil && !job.StartedAt.IsZero() {
		return job.StartedAt.UTC()
	}
	return fallback
}
