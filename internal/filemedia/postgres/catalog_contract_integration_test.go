//go:build integration

package filemediapostgres_test

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPostgresCatalogStoreContractIntegration(t *testing.T) {
	t.Parallel()
	filemediatest.RunCatalogContract(t, func(t *testing.T) filemediatest.CatalogHarness {
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return filemediatest.CatalogHarness{Harness: mutationtest.PostgresHarness(db, filemediapostgres.NewUnits(db, filemediapostgres.Ports{})), SeedFile: func(t *testing.T, row domain.CatalogRow, pending, output bool) {
			digest := sha256.Sum256([]byte(row.ID))
			duration := int(*row.DurationSeconds)
			var deletion *time.Time
			if pending {
				now := time.Now().UTC()
				deletion = &now
			}
			require.NoError(t, db.Create(&model.File{ID: row.ID, FileName: row.Name, Extension: *row.Extension, MimeType: *row.MimeType, FileSize: *row.FileSize, DurationSeconds: &duration, SHA256: digest[:], CreatedAt: row.CreatedAt, DeleteRequestedAt: deletion}).Error)
			require.NoError(t, db.Table("file").Where("id = ?", row.ID).Updates(structured.Fields{"folder_id": row.ParentID, "updated_at": row.UpdatedAt}).Error)
			if output {
				require.NoError(t, db.Create(&model.MeshOptimizationCandidate{ID: uuid.NewString(), SourceFileID: row.ID, OutputObjectID: uuid.NewString(), OutputFileID: &row.ID, TargetRatioPercent: 50, Method: model.MeshOptimizationMethodDraco, PipelineVersion: model.MeshOptimizationPipelineVersionDracoWebpV1, CacheKey: uuid.NewString(), Status: model.MeshOptimizationCandidateStatusReady}).Error)
			}
		}}
	})
}
