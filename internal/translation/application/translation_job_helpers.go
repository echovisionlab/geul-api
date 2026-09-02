package application

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventcontract "github.com/echovisionlab/geul-event-contracts/go/event"
)

func translationTargetDefinition(entityType string) (translation.Definition, error) {
	definition, ok := translation.DefinitionForKind(entityType)
	if !ok {
		return translation.Definition{}, errs.InvalidArgument(
			"target.entity_type",
			fmt.Sprintf("unsupported translation entity type %q", entityType),
		)
	}
	return definition, nil
}

func validateTranslationJobRequestArtifact(job *model.TranslationJob) error {
	if job == nil {
		return fmt.Errorf("translation job is required")
	}
	definition, ok := translation.DefinitionForKind(job.EntityType)
	if !ok {
		return eventcontract.ValidateTranslationJobRequestArtifact(&managev1.TranslationJob{
			Target:                &managev1.TranslationTarget{EntityType: managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_UNSPECIFIED, EntityId: job.EntityID},
			RequestArtifactDigest: job.RequestArtifactDigest,
		})
	}
	return eventcontract.ValidateTranslationJobRequestArtifact(&managev1.TranslationJob{
		Target: &managev1.TranslationTarget{
			EntityType: definition.Proto,
			EntityId:   job.EntityID,
		},
		RequestArtifactDigest: job.RequestArtifactDigest,
	})
}

func validateTranslationJobRequester(job *model.TranslationJob) error {
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
	job *model.TranslationJob,
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

func translationXLIFFFileID(job *model.TranslationJob) string {
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
	domains DomainRegistry,
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

	if domains == nil {
		return nil, fmt.Errorf("translation domain registry is required")
	}
	return domains.BuildCandidate(plan, source, translation.FlattenResponse(*response))
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

func translationJobStartedAt(job *model.TranslationJob, fallback time.Time) time.Time {
	if job == nil {
		return time.Time{}
	}
	if job.StartedAt != nil && !job.StartedAt.IsZero() {
		return job.StartedAt.UTC()
	}
	return fallback
}
