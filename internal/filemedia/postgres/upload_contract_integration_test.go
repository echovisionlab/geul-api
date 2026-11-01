//go:build integration

package filemediapostgres_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestPostgresUploadStoreContractIntegration(t *testing.T) {
	t.Parallel()
	filemediatest.RunUploadContract(t, func(t *testing.T) filemediatest.UploadHarness {
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return filemediatest.UploadHarness{Harness: mutationtest.PostgresHarness(db, filemediapostgres.NewUnits(db, filemediapostgres.Ports{})), Seed: func(t *testing.T, row domain.UploadSession, parts []domain.UploadPart) {
			r := filemediapostgres.UploadSessionRow(row)
			require.NoError(t, db.Omit("EntityID").Create(&r).Error)
			for _, part := range parts {
				p := model.UploadPart(part)
				require.NoError(t, db.Create(&p).Error)
			}
		}}
	})
}
