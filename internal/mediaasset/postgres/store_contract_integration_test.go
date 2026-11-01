//go:build integration

package mediaassetpostgres_test

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaasset/mediaassettest"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPostgresStoreContractIntegration(t *testing.T) {
	t.Parallel()
	mediaassettest.RunStoreContract(t, func(t *testing.T) mediaassettest.Harness {
		db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
		return mediaassettest.Harness{Harness: mutationtest.PostgresHarness(db, mediaassetpostgres.NewUnits(db)),
			Cleanup: mutationtest.PostgresHarness(db, mediaassetpostgres.NewCleanupUnits(db)), SeedFile: func(t *testing.T, pending *time.Time) string {
				id := uuid.NewString()
				digest := sha256.Sum256([]byte(id))
				require.NoError(t, db.Create(&model.File{ID: id, FileName: "source", MimeType: "image/webp", FileSize: 1024, Extension: "webp", SHA256: digest[:], CreatedAt: time.Now().UTC(), DeleteRequestedAt: pending}).Error)
				return id
			}, SeedGeneration: func(t *testing.T, value mediaasset.Generation) {
				require.NoError(t, db.Create(&model.MediaGeneration{ID: value.ID, FileID: value.FileID, Kind: value.Kind, ObjectPrefix: value.ObjectPrefix, ManifestName: value.ManifestName, ManifestSHA256: value.ManifestSHA256, ObjectCount: value.ObjectCount, TotalSize: value.TotalSize, Status: string(value.Status), ReadyAt: value.ReadyAt, RetiredAt: value.RetiredAt, DeleteAfter: value.DeleteAfter, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}).Error)
			}}
	})
}
