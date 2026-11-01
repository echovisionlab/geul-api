//go:build integration

package postgres

import (
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/series/seriestest"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

// contractUnits binds the Series rows only; the contract never reaches
// another domain.
func contractUnits(db *gorm.DB) mutation.UnitOfWork[series.Store] {
	return mutation.Postgres(db, func(tx *gorm.DB) series.Store {
		return store{translationStore: translationStore{tx: tx}}
	})
}

func TestPostgresStoreKeepsTheStoreContractIntegration(t *testing.T) {
	t.Parallel()
	seriestest.RunStoreContract(t, func(t *testing.T) seriestest.Fixture {
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return seriestest.Fixture{
			Harness: mutationtest.PostgresHarness(db, contractUnits(db)),
			Member: func(t *testing.T) string {
				memberID := uuid.NewString()
				testutil.InsertDocumentContributor(t, db, memberID)
				return memberID
			},
			File: func(t *testing.T) string {
				fileID := uuid.NewString()
				digest := sha256.Sum256([]byte(fileID))
				require.NoError(t, db.Create(&model.File{
					ID: fileID, FileName: "series-" + fileID, MimeType: "image/webp", FileSize: 1, Extension: "webp", SHA256: digest[:],
				}).Error)
				return fileID
			},
		}
	})
}
