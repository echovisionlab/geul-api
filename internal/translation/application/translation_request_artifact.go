package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
)

func loadTranslationRequestArtifact(
	ctx context.Context,
	db *gorm.DB,
	jobID string,
) (translation.RequestArtifact, error) {
	if db == nil {
		return translation.RequestArtifact{}, fmt.Errorf("translation database is required")
	}
	var row struct {
		XLIFF    []byte `gorm:"column:request_xliff"`
		Manifest []byte `gorm:"column:request_manifest"`
		Digest   string `gorm:"column:request_artifact_digest"`
	}
	err := db.WithContext(ctx).Raw(
		`SELECT request_xliff, request_manifest, request_artifact_digest FROM translation_job WHERE id = ?`,
		jobID,
	).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
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
	db *gorm.DB,
	domains DomainRegistry,
	job *model.TranslationJob,
	source *translation.SourceDocument,
) (translation.RequestArtifact, error) {
	plan, err := buildTranslationExtractionPlan(domains, job, source)
	if err != nil {
		return translation.RequestArtifact{}, err
	}
	request, err := buildTranslationProviderRequest(job, plan)
	if err != nil {
		return translation.RequestArtifact{}, err
	}
	request, err = loadTranslationGenerationResources(ctx, db, job, request)
	if err != nil {
		return translation.RequestArtifact{}, err
	}
	return translation.BuildRequestArtifact(request, plan)
}
