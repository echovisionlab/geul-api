package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/translation"
)

func loadTranslationRequestArtifact(
	ctx context.Context,
	store JobStore,
	jobID string,
) (translation.RequestArtifact, error) {
	row, err := store.RequestArtifact(ctx, jobID)
	if errors.Is(err, ErrJobNotFound) {
		return translation.RequestArtifact{}, fmt.Errorf("translation job %q is missing", jobID)
	}
	if err != nil {
		return translation.RequestArtifact{}, err
	}
	manifest, err := translation.CanonicalizeRequestManifest(row.Manifest)
	if err != nil {
		return translation.RequestArtifact{}, fmt.Errorf("translation job %q request manifest is invalid: %w", jobID, err)
	}
	if row.Digest != translation.RequestArtifactDigest(row.XLIFF, manifest) {
		return translation.RequestArtifact{}, fmt.Errorf("translation job %q request artifact digest does not match", jobID)
	}
	return translation.RequestArtifact{XLIFF: row.XLIFF, Manifest: manifest, Digest: row.Digest}, nil
}

func buildPersistedTranslationRequest(
	ctx context.Context,
	store JobStore,
	plans DomainPlans,
	job *Job,
	source *translation.SourceDocument,
) (translation.RequestArtifact, error) {
	plan, err := buildTranslationExtractionPlan(plans, job, source)
	if err != nil {
		return translation.RequestArtifact{}, err
	}
	request, err := buildTranslationProviderRequest(job, plan)
	if err != nil {
		return translation.RequestArtifact{}, err
	}
	request, err = loadTranslationGenerationResources(ctx, store, job, request)
	if err != nil {
		return translation.RequestArtifact{}, err
	}
	return translation.BuildRequestArtifact(request, plan)
}

func (m *TranslationJobManager) loadRequestArtifact(ctx context.Context, jobID string) (translation.RequestArtifact, error) {
	var artifact translation.RequestArtifact
	err := m.units.Read(ctx, func(store JobStore) error {
		var err error
		artifact, err = loadTranslationRequestArtifact(ctx, store, jobID)
		return err
	})
	return artifact, err
}
