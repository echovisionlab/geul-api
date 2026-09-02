//go:build integration

package application

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/require"
)

func TestLoadTranslationRequestArtifactCanonicalizesJSONBRoundTrip(t *testing.T) {
	db := newServiceIntegrationDB(t)
	now := time.Now().UTC()
	memberID := testutil.IntegrationUUID()
	testutil.InsertDocumentContributor(t, db, memberID)

	entityID := generateUUID()
	unit := translation.Unit{
		UnitID: "u1", EntityType: "post", EntityID: entityID,
		Path: "entity:title", ContainerType: translation.ContainerTypeEntity,
		ContainerID: entityID, FieldName: "title",
		SourceText: "Source title", SourceFormat: translation.SourceFormatPlainText,
		SourceLocale: "en",
	}
	plan := &translation.ExtractionPlan{
		EntityType: "post", EntityID: unit.EntityID, SourceLocale: "en", TargetLocale: "ja",
		Units: []translation.Unit{unit},
		Bundles: []translation.Bundle{{
			BundleID: "entity:main", EntityType: "post", EntityID: unit.EntityID,
			SourceLocale: "en", TargetLocale: "ja", BundleType: translation.BundleTypeEntity,
			SequenceTotal: 1, Units: []translation.Unit{unit},
		}},
	}
	document, err := translation.BuildXLIFFDocument(plan)
	require.NoError(t, err)
	artifact, err := translation.BuildRequestArtifact(translation.ProviderRequest{
		RequestID: "job-1", OperationID: "operation-1",
		Profile:  translation.GenerationProfile{SourceLocale: "en", TargetLocale: "ja"},
		Document: *document,
	}, plan)
	require.NoError(t, err)

	job := &model.TranslationJob{
		ID: generateUUID(), EntityType: "post", EntityID: unit.EntityID,
		TargetLocale: "ja", SourceLocale: "en",
		RequestArtifactDigest: artifact.Digest,
		OperationID:           generateUUID(),
		Status:                translationJobStatusQueued,
		RequestedByMemberID:   memberID,
		RequestXLIFF:          artifact.XLIFF,
		RequestManifest:       artifact.Manifest,
		RequestedAt:           now,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	require.NoError(t, db.Create(job).Error)

	var stored struct {
		Manifest []byte `gorm:"column:request_manifest"`
	}
	require.NoError(t, db.Raw(
		`SELECT request_manifest FROM translation_job WHERE id = ?`, job.ID,
	).Take(&stored).Error)
	require.NotEqual(t, artifact.Manifest, stored.Manifest)

	loaded, err := loadTranslationRequestArtifact(context.Background(), db, job.ID)
	require.NoError(t, err)
	require.Equal(t, artifact, loaded)
}
